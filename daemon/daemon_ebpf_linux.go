//go:build linux
// +build linux

package daemon

import (
	"context"
	"fmt"
	"os"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"gitlab.msxf.com/mscloud/lira/pkg/ebpf"
	"gitlab.msxf.com/mscloud/lira/pkg/k8s"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"
	"gitlab.msxf.com/mscloud/lira/pkg/policyagent"
	"gitlab.msxf.com/mscloud/lira/pkg/types"
)

// newPolicyBackend 同步初始化策略数据面（加载/复用 pinned map/program 并写节点配置）。
// 失败返回 nil（功能禁用），CNI 主流程不受影响（设计 §7 降级原则）。
func newPolicyBackend(k8sObj k8s.Kubernetes) *ebpf.Manager {
	mgr, err := ebpf.NewManager(ebpf.BPFObjectPath)
	if err != nil {
		llog.Logger.WithError(err).Error("ebpf: init policy backend failed, network policy disabled")
		return nil
	}
	// 节点 IP 用于数据面节点流量旁路（kubelet 探活等）。
	// 拿不到节点 IP 时禁用功能：数据面会把节点流量当未知源丢弃，探活会失败。
	nodeIP := k8sObj.NodeIP()
	if nodeIP == nil {
		llog.Logger.Error("ebpf: node ip unavailable, network policy disabled")
		return nil
	}
	if err := mgr.WriteNodeConfig(nodeIP); err != nil {
		llog.Logger.WithError(err).Error("ebpf: write node config failed, network policy disabled")
		return nil
	}
	return mgr
}

// runNetworkPolicy 异步部分：策略 reconciler（controller-runtime Manager）、ENI 挂载定时、事件消费。
func runNetworkPolicy(ctx context.Context, mgr *ebpf.Manager, k8sObj k8s.Kubernetes) {
	// 与仓库现有 controller 统一走 controller-runtime Manager（缓存 client，首次全量 List 后走 watch）。
	// daemon 为 hostNetwork DaemonSet：必须关闭默认 metrics/probe 监听端口避免宿主端口冲突；
	// 策略每节点都需本地执行，不能开 leader election。
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		llog.Logger.WithError(err).Error("ebpf: load rest config failed, network policy disabled")
		return
	}
	ctrlMgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:                 types.Scheme,
		MetricsBindAddress:     "0",
		HealthProbeBindAddress: "0",
	})
	if err != nil {
		llog.Logger.WithError(err).Error("ebpf: init policy manager failed, network policy disabled")
		return
	}
	if err := policyagent.Register(ctrlMgr, mgr); err != nil {
		llog.Logger.WithError(err).Error("ebpf: register policy reconciler failed, network policy disabled")
		return
	}
	go func() {
		if err := ctrlMgr.Start(ctx); err != nil {
			llog.Logger.WithError(err).Error("ebpf: policy manager exited")
		}
	}()

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		llog.Logger.Error("ebpf: NODE_NAME unavailable, ENI attach reconcile disabled")
	} else {
		go func() {
			timer := time.NewTimer(0)
			defer timer.Stop()
			for {
				if err := attachENIProgs(ctx, mgr, k8sObj, nodeName); err != nil {
					llog.Logger.WithError(err).Error("ebpf: ENI attach reconcile failed")
				}
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
				timer.Reset(30 * time.Second)
			}
		}()
	}

	go mgr.DrainEvents(ctx)
}

// attachENIProgs 收集本节点 PodENI 的 ENI MAC，确保 ①② 程序幂等挂载（multi-ENIIP 共享 ENI，MAC 去重）
func attachENIProgs(ctx context.Context, mgr *ebpf.Manager, k8sObj k8s.Kubernetes, nodeName string) error {
	podENIs, err := k8sObj.ListPodENIs(ctx)
	if err != nil {
		return fmt.Errorf("error list podenis, %w", err)
	}
	seen := make(map[string]bool)
	var macs []string
	for _, e := range podENIs {
		if e.Status.NodeName != nodeName || e.Spec.ENI.MACAddress == "" || seen[e.Spec.ENI.MACAddress] {
			continue
		}
		seen[e.Spec.ENI.MACAddress] = true
		macs = append(macs, e.Spec.ENI.MACAddress)
	}
	return mgr.EnsureENIAttached(macs)
}
