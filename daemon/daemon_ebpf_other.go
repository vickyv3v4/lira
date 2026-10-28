//go:build !linux
// +build !linux

package daemon

import (
	"context"

	"gitlab.msxf.com/mscloud/lira/pkg/ebpf"
	"gitlab.msxf.com/mscloud/lira/pkg/k8s"
)

// 非 linux 平台桩：网络策略功能不可用
func newPolicyBackend(k8sObj k8s.Kubernetes) *ebpf.Manager { return nil }

func runNetworkPolicy(ctx context.Context, mgr *ebpf.Manager, k8sObj k8s.Kubernetes) {}
