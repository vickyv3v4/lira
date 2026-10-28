package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"time"

	"github.com/containernetworking/cni/pkg/skel"
	cniTypes "github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/cni/pkg/version"

	"gitlab.msxf.com/mscloud/lira/pkg/cni"
	"gitlab.msxf.com/mscloud/lira/pkg/ebpf"
	"gitlab.msxf.com/mscloud/lira/pkg/link"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"
	"gitlab.msxf.com/mscloud/lira/pkg/rpc"
	"gitlab.msxf.com/mscloud/lira/pkg/utils"
)

const (
	defaultCniTimeout       = 120 * time.Second
	defaultEventTimeout     = 10 * time.Second
	defaultVethPrefix       = "lira"
	defaultIPVlanPrefix     = "ipvlan"
	defaultIPVlanHostPrefix = "ipvl_"
)

func init() {
	runtime.LockOSThread()
}

func main() {
	skel.PluginMain(cmdAdd, cmdCheck, cmdDel, version.PluginSupports("0.3.0", "0.3.1", "0.4.0", "1.0.0"), "CNI lira plugin version v0.1")
}

type AllocIPResult struct {
	Mac         string
	PodIP       string
	PodCidr     string
	ServiceCidr string
	GatewayIP   string
	NodeIP      string
}

func cmdAdd(args *skel.CmdArgs) error {
	log := llog.NewLoggerForCNI()
	log.Info("cmd add pod ip")
	cmdArgs, err := cni.GetCmdArgs(args)
	if err != nil {
		return err
	}
	defer cmdArgs.Close()

	cniConf := cmdArgs.GetCNIConf()
	k8sConfig := cmdArgs.GetK8SConfig()

	log.Infof("args: %s , ns: %s , k8sInfo: %s , cni: %s , ifname: %s", utils.JSONStr(args), cmdArgs.GetNetNSPath(), utils.JSONStr(k8sConfig), utils.JSONStr(cniConf), args.IfName)

	ctx, cancel := context.WithTimeout(context.Background(), defaultCniTimeout)
	defer cancel()

	client, conn, err := cni.GetNetworkClient(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var replyRes *AllocIPResult
	if replyRes, err = doRPCAdd(ctx, client, cmdArgs); err != nil {
		return err
	}
	log.Infof("reply: %+v", replyRes)
	var index int
	if index, err = link.GetDeviceNumber(replyRes.Mac); err != nil {
		log.Error(err, "failed to get device, mac ", replyRes.Mac)
		return nil
	}
	podIPNet := net.ParseIP(replyRes.PodIP)
	_, podIP, err := net.ParseCIDR(replyRes.PodCidr)
	if err != nil {
		log.Error(err, "parse pod cidr.")
		return err
	}
	podRoute := cniTypes.Route{
		Dst: *podIP,
	}
	podIP.IP = podIPNet
	_, s, err := net.ParseCIDR(replyRes.ServiceCidr)
	if err != nil {
		log.Error(err, "parse service cidr.")
		return err
	}
	serviceRoute := cniTypes.Route{
		Dst: *s,
	}

	hostVethName, err := link.VethNameForPod(string(k8sConfig.K8S_POD_NAME), string(k8sConfig.K8S_POD_NAMESPACE), cmdArgs.InputArgs.IfName, defaultVethPrefix)
	if err != nil {
		log.Error(err, "generate veth name.")
		return err
	}
	ipVlanTmpName, err := link.IPVlanTmpNameForPod(string(k8sConfig.K8S_POD_NAME), string(k8sConfig.K8S_POD_NAMESPACE), defaultIPVlanPrefix)
	if err != nil {
		log.Error(err, "generate ipvlan name.")
		return err
	}
	log.Infof("hostipvlanName: %s", hostVethName)
	config := &link.LinkConfig{
		ENIIndex:     index,
		HostVethName: hostVethName,
		PodIfName:    cmdArgs.InputArgs.IfName,
		PodIPNet:     podIP,
		Route:        &podRoute,
		ServiceRoute: &serviceRoute,
		GatewayIP:    replyRes.GatewayIP,
		Mac:          replyRes.Mac,
		HostIP:       replyRes.NodeIP,
		TmpPodIfName: ipVlanTmpName,
	}
	mode := cniConf.RunMode
	switch utils.LiraIPMode(mode) {
	case utils.MultiENIIP:
		err = link.NewMultiIPvlanENIDriver().Install(ctx, config, cmdArgs.NetNS)
		if err != nil {
			log.Error(err, "failed to setup MultiIPvlanENIDriver")
			return err
		}
	case utils.ExclusiveENI:
		err = link.NewExclusiveENIDriver().Install(ctx, config, cmdArgs.NetNS)
		if err != nil {
			log.Error(err, "failed to setup ExclusiveENIDriver")
			return err
		}
	default:
		return errors.New("cni ip mode is invalid")
	}

	err = link.EnsureHostNsConfig()
	if err != nil {
		log.Error(err, "failed to EnsureHostNsConfig")
		return err
	}

	// eBPF 网络策略（设计 §5.1）：挂载 ③④ 程序 + 写 slave_src 防伪造白名单。
	// 失败不阻断建链 —— ENI 主口 ①② 程序仍在强制执行策略，仅缺少 pod 口本地过滤。
	if err := ebpf.AttachPod(cmdArgs.GetNetNSPath(), config.PodIfName, hostVethName, podIPNet); err != nil {
		log.Warn("ebpf: attach pod filters failed, policy still enforced at ENI main port, ", err)
	}

	result := &current.Result{}
	result.Interfaces = append(result.Interfaces, &current.Interface{
		Name:    args.IfName,
		Sandbox: string(k8sConfig.K8S_POD_INFRA_CONTAINER_ID),
		Mac:     config.Mac,
	})
	result.IPs = append(result.IPs, &current.IPConfig{
		Address:   *config.PodIPNet,
		Gateway:   net.ParseIP(config.GatewayIP),
		Interface: current.Int(0),
	})
	return cniTypes.PrintResult(result, cniConf.CNIVersion)
}

func doRPCAdd(ctx context.Context, client rpc.LiraBackendClient, cmdArgs *cni.CniCmdArgs) (res *AllocIPResult, err error) {
	log := llog.Logger
	var k8sConfig, args = cmdArgs.K8sArgs, cmdArgs.InputArgs
	var podIp string

	start := time.Now()
	defer func() {
		eventCtx, cancel := context.WithTimeout(context.Background(), defaultEventTimeout)
		defer cancel()
		if err != nil {
			_, _ = client.RecordEvent(eventCtx, &rpc.EventRequest{
				EventTarget:     rpc.EventTarget_EventTargetPod,
				K8SPodName:      string(k8sConfig.K8S_POD_NAME),
				K8SPodNamespace: string(k8sConfig.K8S_POD_NAMESPACE),
				EventType:       rpc.EventType_EventTypeWarning,
				Reason:          "AllocIPFailed",
				Message:         err.Error(),
			})
		} else {
			_, _ = client.RecordEvent(eventCtx, &rpc.EventRequest{
				EventTarget:     rpc.EventTarget_EventTargetPod,
				K8SPodName:      string(k8sConfig.K8S_POD_NAME),
				K8SPodNamespace: string(k8sConfig.K8S_POD_NAMESPACE),
				EventType:       rpc.EventType_EventTypeNormal,
				Reason:          "AllocIPSucceed",
				Message:         fmt.Sprintf("Alloc IP %s took %s", podIp, time.Since(start).String()),
			})
		}
	}()

	allocResult, err := client.AllocIP(ctx, &rpc.AllocIPRequest{
		Netns:                  args.Netns,
		K8SPodName:             string(k8sConfig.K8S_POD_NAME),
		K8SPodNamespace:        string(k8sConfig.K8S_POD_NAMESPACE),
		K8SPodInfraContainerId: string(k8sConfig.K8S_POD_INFRA_CONTAINER_ID),
	})
	if err != nil {
		err = fmt.Errorf("cmdAdd: error alloc ip %w", err)
		return nil, err
	}
	if !allocResult.Success {
		err = fmt.Errorf("cmdAdd: alloc ip return not success")
		return nil, err
	}

	defer func() {
		if err != nil {
			log.Error(err, "failed to allocate IP")
			releaseCtx, cancel := context.WithTimeout(context.Background(), defaultCniTimeout)
			defer cancel()
			_, _ = client.ReleaseIP(releaseCtx, &rpc.ReleaseIPRequest{
				K8SPodName:             string(k8sConfig.K8S_POD_NAME),
				K8SPodNamespace:        string(k8sConfig.K8S_POD_NAMESPACE),
				K8SPodInfraContainerId: string(k8sConfig.K8S_POD_INFRA_CONTAINER_ID),
			})
		}
	}()

	netConf := allocResult.NetConfs
	podIp = netConf.BasicInfo.GetPodIP().IPv4
	podCidr := netConf.BasicInfo.GetPodCIDR().IPv4
	mac := netConf.BasicInfo.GetMAC()
	gatewayIP := netConf.BasicInfo.GetGatewayIP().IPv4
	serviceCidr := netConf.BasicInfo.GetServiceCIDR().IPv4
	nodeIP := netConf.BasicInfo.GetNodeIP()

	return &AllocIPResult{
		PodIP:       podIp,
		Mac:         mac,
		PodCidr:     podCidr,
		GatewayIP:   gatewayIP,
		ServiceCidr: serviceCidr,
		NodeIP:      nodeIP,
	}, nil
}
func cmdCheck(args *skel.CmdArgs) error {
	return nil
}

func cmdDel(args *skel.CmdArgs) error {
	log := llog.NewLoggerForCNI()
	log.Info("cmd release pod ip")
	cmdArgs, err := cni.GetCmdArgs(args)
	if err != nil {
		return err
	}
	defer cmdArgs.Close()

	cniConf := cmdArgs.GetCNIConf()
	k8sConfig := cmdArgs.GetK8SConfig()

	log.Infof("args: %s , ns: %s , k8sInfo: %s , cni: %s , ifname: %s", utils.JSONStr(args), cmdArgs.GetNetNSPath(), utils.JSONStr(k8sConfig), utils.JSONStr(cniConf), args.IfName)
	ctx, cancel := context.WithTimeout(context.Background(), defaultCniTimeout)
	defer cancel()
	err = doCmdDel(ctx, cmdArgs)
	if err != nil {
		log.WithError(err).Error("error cmd delete.")
		return cniTypes.NewError(cniTypes.ErrTryAgainLater, "cmd del failed", err.Error())
	}
	return cniTypes.PrintResult(&current.Result{
		CNIVersion: cniConf.CNIVersion,
	}, cniConf.CNIVersion)
}

func doCmdDel(ctx context.Context, cmdArgs *cni.CniCmdArgs) error {
	log := llog.Logger
	var cniNetns, k8sConf, cniConf = cmdArgs.NetNS, cmdArgs.K8sArgs, cmdArgs.Conf
	ip, err := link.GetLinkAddrByName(cmdArgs.InputArgs.IfName)
	if err != nil {
		log.Error(err, "error GetLinkAddrByName")
		return err
	}

	hostVethName, err := link.VethNameForPod(string(k8sConf.K8S_POD_NAME), string(k8sConf.K8S_POD_NAMESPACE), cmdArgs.InputArgs.IfName, defaultVethPrefix)
	if err != nil {
		log.Error(err, "generate veth name.")
		return err
	}
	log.Infof("hostVethName: %s", hostVethName)

	// eBPF 清理（尽力而为：netns 已销毁时自动跳过；slave_src 由 ifindex 键控须显式删除）
	ebpf.DetachPod(cmdArgs.GetNetNSPath(), cmdArgs.InputArgs.IfName, hostVethName)

	runMode := cniConf.RunMode
	switch utils.LiraIPMode(runMode) {
	case utils.ExclusiveENI:
		err = link.GenericCleanup(ctx, cniNetns)
		if err != nil {
			log.Error(err, "error GenericCleanup")
			return err
		}

		err = link.DeleteVethRoute(hostVethName, ip)
		if err != nil {
			log.Error(err, "delete veth route.")
			return err
		}
	case utils.MultiENIIP:
	default:
		return errors.New("cni ip mode is invalid")
	}
	err = link.DeleteVethNeigh(hostVethName, ip)
	if err != nil {
		log.Error(err, "delete veth neigh.")
		return err
	}
	err = link.DeleteVeth(hostVethName)
	if err != nil {
		log.Error(err, "delete veth.")
		return err
	}
	return nil
}
