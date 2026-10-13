package main

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"time"

	"github.com/containernetworking/cni/pkg/skel"
	cniTypes "github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/cni/pkg/version"

	"gitlab.msxf.com/mscloud/lira/pkg/cni"
	"gitlab.msxf.com/mscloud/lira/pkg/link"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"
	"gitlab.msxf.com/mscloud/lira/pkg/rpc"
	"gitlab.msxf.com/mscloud/lira/pkg/utils"
)

const (
	defaultCniTimeout   = 120 * time.Second
	defaultEventTimeout = 10 * time.Second
	defaultVethPrefix   = "lira"
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
	log := llog.Logger
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
	hostVethName, err := link.VethNameForPod(string(k8sConfig.K8S_POD_NAME), string(k8sConfig.K8S_POD_NAMESPACE), link.DefaultVethForENI, defaultVethPrefix)
	if err != nil {
		log.Error(err, "generate veth name.")
		return err
	}
	log.Infof("hostVethName: %s", hostVethName)
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
	}
	err = link.NewExclusiveENIDriver().Install(ctx, config, cmdArgs.NetNS)
	if err != nil {
		log.Error(err, "failed to setup ExclusiveENIDriver")
		return err
	}

	err = link.EnsureHostNsConfig()
	if err != nil {
		log.Error(err, "failed to EnsureHostNsConfig")
		return err
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
	log := llog.Logger
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
	var cniNetns, k8sConf = cmdArgs.NetNS, cmdArgs.K8sArgs
	ip, err := link.GetLinkAddrByName(cmdArgs.InputArgs.IfName)
	if err != nil {
		log.Error(err, "error GetLinkAddrByName")
		return err
	}
	err = link.GenericCleanup(ctx, cniNetns)
	if err != nil {
		log.Error(err, "error GenericCleanup")
		return err
	}
	hostVethName, err := link.VethNameForPod(string(k8sConf.K8S_POD_NAME), string(k8sConf.K8S_POD_NAMESPACE), cmdArgs.InputArgs.IfName, defaultVethPrefix)
	if err != nil {
		log.Error(err, "generate veth name.")
		return err
	}
	log.Infof("hostVethName: %s", hostVethName)
	err = link.DeleteVethRoute(hostVethName, ip)
	if err != nil {
		log.Error(err, "delete veth route.")
		return err
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
