package msxf

import "context"

type MsxfClientAPI interface {
	AllocateENI(ctx context.Context, req *CreateNetworkInterfaceRequest) (*ENI, error)
	DeleteENI(ctx context.Context, req *DeleteNetworkInterfaceRequest) error
	AttachENI(ctx context.Context, req *AttachNetworkInterfaceRequest) error
	DetachENI(ctx context.Context, req *DetachNetworkInterfaceRequest) error
	HandleENISecondaryIP(ctx context.Context, req *HandleENISecondaryIPRequest) (*SecondaryIPsInfo, error)
	GetENIDetail(ctx context.Context, req *DescribeNetworkInterfaceRequest) (*ENI, error)
}
