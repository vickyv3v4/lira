package link

import (
	"context"

	"gitlab.msxf.com/mscloud/lira/windows/containernetworking/plugins/pkg/ns"
)

type ExclusiveENI struct{}

func NewExclusiveENIDriver() *ExclusiveENI {
	return &ExclusiveENI{}
}
func (r *ExclusiveENI) Install(ctx context.Context, cfg *LinkConfig, netNS ns.NetNS) error {
	return nil
}
