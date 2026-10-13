package cni

import (
	"context"
	"encoding/json"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/containernetworking/cni/pkg/skel"
	cniTypes "github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/plugins/pkg/ns"

	"gitlab.msxf.com/mscloud/lira/pkg/rpc"
	"gitlab.msxf.com/mscloud/lira/pkg/types"
)

type CniCmdArgs struct {
	Conf      *types.CNIConf
	NetNS     ns.NetNS
	K8sArgs   *types.K8SArgs
	InputArgs *skel.CmdArgs
}

func GetCmdArgs(args *skel.CmdArgs) (*CniCmdArgs, error) {
	netNS, err := ns.GetNS(args.Netns)
	if err != nil {
		return nil, err
	}

	var conf types.CNIConf
	if err = json.Unmarshal(args.StdinData, &conf); err != nil {
		return nil, cniTypes.NewError(cniTypes.ErrDecodingFailure, "failed to parse network config", err.Error())
	}

	var k8sArgs types.K8SArgs
	if err = cniTypes.LoadArgs(args.Args, &k8sArgs); err != nil {
		return nil, cniTypes.NewError(cniTypes.ErrDecodingFailure, "failed to parse args", err.Error())
	}

	return &CniCmdArgs{
		Conf:      &conf,
		NetNS:     netNS,
		K8sArgs:   &k8sArgs,
		InputArgs: args,
	}, nil
}

func (args *CniCmdArgs) GetCNIConf() *types.CNIConf {
	if args == nil || args.Conf == nil {
		return nil
	}
	return args.Conf
}

func (args *CniCmdArgs) GetK8SConfig() *types.K8SArgs {
	if args == nil || args.K8sArgs == nil {
		return nil
	}
	return args.K8sArgs
}

func (args *CniCmdArgs) GetNetNSPath() string {
	if args == nil || args.NetNS == nil {
		return ""
	}
	return args.NetNS.Path()
}

func (args *CniCmdArgs) Close() error {
	if args == nil || args.NetNS == nil {
		return nil
	}
	return args.NetNS.Close()
}

func GetNetworkClient(ctx context.Context) (rpc.LiraBackendClient, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient("passthrough:"+types.DefaultSocketPath, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(
		func(ctx context.Context, s string) (net.Conn, error) {
			unixAddr, err := net.ResolveUnixAddr("unix", s)
			if err != nil {
				return nil, err
			}
			d := net.Dialer{}
			return d.DialContext(ctx, "unix", unixAddr.String())
		}),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  time.Second,
				Multiplier: 1,
				MaxDelay:   time.Second,
			},
		}),
	)

	if err != nil {
		return nil, nil, cniTypes.NewError(cniTypes.ErrTryAgainLater, "failed connect to daemon", err.Error())
	}

	client := rpc.NewLiraBackendClient(conn)
	return client, conn, nil
}
