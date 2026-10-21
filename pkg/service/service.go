package service

import (
	"context"
	"errors"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	networkv1beta1 "gitlab.msxf.com/mscloud/lira/pkg/api/v1beta1"
	"gitlab.msxf.com/mscloud/lira/pkg/k8s"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"
	"gitlab.msxf.com/mscloud/lira/pkg/rpc"
	"gitlab.msxf.com/mscloud/lira/pkg/utils"
)

var _ rpc.LiraBackendServer = (*rpcService)(nil)

type rpcService struct {
	//daemonMode     string

	k8s k8s.Kubernetes
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

	return reply, nil
}
func (rs *rpcService) ReleaseIP(ctx context.Context, r *rpc.ReleaseIPRequest) (*rpc.ReleaseIPReply, error) {
	return nil, nil
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
