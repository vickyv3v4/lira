package link

import (
	"context"

	"gitlab.msxf.com/mscloud/lira/windows/containernetworking/plugins/pkg/ns"
)

type MultiIPvlanENI struct{}

func NewMultiIPvlanENIDriver() *MultiIPvlanENI {
	return &MultiIPvlanENI{}
}
func (r *MultiIPvlanENI) Install(ctx context.Context, cfg *LinkConfig, netNS ns.NetNS) error {
	return nil
}
