package service

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	networkv1beta1 "gitlab.msxf.com/mscloud/lira/pkg/api/v1beta1"
	"gitlab.msxf.com/mscloud/lira/pkg/ebpf"
	"gitlab.msxf.com/mscloud/lira/pkg/k8s"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"
	"gitlab.msxf.com/mscloud/lira/pkg/policyagent"
	"gitlab.msxf.com/mscloud/lira/pkg/rpc"
	"gitlab.msxf.com/mscloud/lira/pkg/utils"
)

var _ rpc.LiraBackendServer = (*rpcService)(nil)

// GetK8s 返回 kubernetes 封装（daemon ebpf 子系统初始化用）
func (rs *rpcService) GetK8s() k8s.Kubernetes { return rs.k8s }

// SetPolicyBackend 注入策略数据面后端（daemon 启动时注入；nil = 功能未启用）
func (rs *rpcService) SetPolicyBackend(b ebpf.Backend) { rs.policy = b }

type rpcService struct {
	//daemonMode     string

	k8s k8s.Kubernetes
	// policy 策略数据面后端（daemon 启动时注入；nil = 网络策略功能未启用）
	policy ebpf.Backend
	sync.RWMutex

	rpc.UnimplementedLiraBackendServer
}

func (rs *rpcService) AllocIP(ctx context.Context, r *rpc.AllocIPRequest) (*rpc.AllocIPReply, error) {
	log := llog.Logger
	podID := utils.PodInfoKey(r.K8SPodNamespace, r.K8SPodName)
	log.Info("alloc ", podID, " ip")

	rs.RLock()
	defer rs.RUnlock()

	var podENI *networkv1beta1.PodENI
	var innerErr error

	var readyPhase networkv1beta1.PhaseStr

	switch utils.GConfig.RunMode {
	case utils.ExclusiveENI:
		readyPhase = networkv1beta1.ENIPhaseBind
	case utils.MultiENIIP:
		readyPhase = networkv1beta1.ENIPhaseIPAllocated
	}

	err := wait.ExponentialBackoffWithContext(ctx, wait.Backoff{
		Duration: time.Second * 10,
		Factor:   2,
		Jitter:   0.3,
		Steps:    5},
		func(ctx context.Context) (bool, error) {
			podENI, innerErr = rs.k8s.GetPodENI(ctx, r.K8SPodNamespace, r.K8SPodName)
			if innerErr != nil {
				return false, innerErr
			}
			if !podENI.DeletionTimestamp.IsZero() {
				return false, errors.New("podENI is deleting")
			}
			if podENI.Status.Phase != readyPhase {
				return false, nil
			}
			return true, nil
		})
	if err != nil {
		log.WithError(err).Error("rpcService: allocIP failed.")
		return nil, err
	}
	reply := &rpc.AllocIPReply{
		Success: true,
		IPv4:    true,
	}
	netConf := &rpc.NetConf{
		BasicInfo: &rpc.BasicInfo{
			MAC: podENI.Spec.ENI.MACAddress,
			PodIP: &rpc.IPSet{
				IPv4: podENI.Spec.PodIPInfo.IPAddress,
			},
			PodCIDR: &rpc.IPSet{
				IPv4: podENI.Spec.ENI.PodCidr,
			},
			GatewayIP: &rpc.IPSet{
				IPv4: podENI.Spec.ENI.GatewayIP,
			},
			ServiceCIDR: &rpc.IPSet{
				IPv4: podENI.Spec.ServiceCidr,
			},
			NodeIP: podENI.Spec.NodeIP,
		},
	}
	reply.NetConfs = netConf

	// eBPF 网络策略（设计 §5.1 建链）：pod_ip → 身份映射立即落表，
	// 覆盖 CNI ADD 与 agent watch 事件之间的窗口；失败不阻断建链，agent 全量同步会收敛
	if rs.policy != nil {
		podIP := net.ParseIP(podENI.Spec.PodIPInfo.IPAddress)
		if pod, podErr := rs.k8s.GetPodObj(ctx, r.K8SPodNamespace, r.K8SPodName); podErr != nil {
			llog.Logger.WithError(podErr).Warn("ebpf: get pod for identity failed, agent will converge")
		} else if podIP == nil {
			llog.Logger.Warn("ebpf: podENI has invalid pod ip, skip identity upsert")
		} else {
			identity := policyagent.IdentityFor(pod.Namespace, pod.Labels)
			if err := rs.policy.UpsertPodIP(podIP, identity); err != nil {
				llog.Logger.WithError(err).Warn("ebpf: upsert pod_ip identity failed, agent will converge")
			}
		}
	}

	return reply, nil
}
func (rs *rpcService) ReleaseIP(ctx context.Context, r *rpc.ReleaseIPRequest) (*rpc.ReleaseIPReply, error) {
	// pod_ip 映射的正常删除由 agent watch 全量收敛；此处仅当策略后端可用时尽力清理
	//（覆盖 agent 未运行 / 功能降级场景），失败仅记日志
	if rs.policy != nil {
		if podENI, err := rs.k8s.GetPodENI(ctx, r.K8SPodNamespace, r.K8SPodName); err == nil {
			if ip := net.ParseIP(podENI.Spec.PodIPInfo.IPAddress); ip != nil {
				if err := rs.policy.DeletePodIP(ip); err != nil {
					llog.Logger.WithError(err).Warn("ebpf: delete pod_ip failed (agent will converge)")
				}
			}
		}
	}
	return &rpc.ReleaseIPReply{
		Success: true,
		IPv4:    true,
	}, nil
}
func (rs *rpcService) RecordEvent(ctx context.Context, r *rpc.EventRequest) (*rpc.EventReply, error) {
	eventType := corev1.EventTypeNormal
	if r.EventType == rpc.EventType_EventTypeWarning {
		eventType = corev1.EventTypeWarning
	}

	reply := &rpc.EventReply{
		Succeed: true,
		Error:   "",
	}
	// Pod
	err := rs.k8s.RecordPodEvent(ctx, r.K8SPodName, r.K8SPodNamespace, eventType, r.Reason, r.Message)
	if err != nil {
		reply.Succeed = false
		reply.Error = err.Error()

		return reply, err
	}
	return reply, nil
}
