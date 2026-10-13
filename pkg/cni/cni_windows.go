package cni

import (
	"context"

	"github.com/containernetworking/cni/pkg/skel"
	"google.golang.org/grpc"

	"gitlab.msxf.com/mscloud/lira/pkg/rpc"
	"gitlab.msxf.com/mscloud/lira/pkg/types"
	"gitlab.msxf.com/mscloud/lira/windows/containernetworking/plugins/pkg/ns"
)

type CniCmdArgs struct {
	Conf      *types.CNIConf
	NetNS     ns.NetNS
	K8sArgs   *types.K8SArgs
	InputArgs *skel.CmdArgs
}

func GetCmdArgs(_ *skel.CmdArgs) (*CniCmdArgs, error) {
	return nil, nil
}

func (args *CniCmdArgs) Close() error {
	return nil
}

func (args *CniCmdArgs) GetCNIConf() *types.CNIConf {
	return nil
}

func (args *CniCmdArgs) GetK8SConfig() *types.K8SArgs {
	return nil
}

func (args *CniCmdArgs) GetNetNSPath() string {
	return ""
}

func GetNetworkClient(ctx context.Context) (rpc.LiraBackendClient, *grpc.ClientConn, error) {
	return nil, nil, nil
}
