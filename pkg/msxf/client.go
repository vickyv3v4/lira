package msxf

import (
	"context"

	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/lib/xcontext"
)

type MsxfClient struct {
	APIUrl string
}

func NewMsxfClient(url string) *MsxfClient {
	return &MsxfClient{
		APIUrl: url,
	}
}

func (m *MsxfClient) AllocateENI(ctx context.Context, req *CreateNetworkInterfaceRequest) (*ENI, error) {

	var eni *ENI
	var err error

	if eni, err = CreateNetworkInterface(ctx, m.APIUrl, req); err != nil {
		return nil, err
	}
	return eni, nil
}
func (m *MsxfClient) DeleteENI(ctx context.Context, req *DeleteNetworkInterfaceRequest) error {
	if err := DeleteNetworkInterface(ctx, m.APIUrl, req); err != nil {
		return err
	}
	return nil
}
func (m *MsxfClient) AttachENI(ctx context.Context, req *AttachNetworkInterfaceRequest) error {
	if err := AttachNetworkInterface(ctx, m.APIUrl, req); err != nil {
		return err
	}
	return nil
}
func (m *MsxfClient) DetachENI(ctx context.Context, req *DetachNetworkInterfaceRequest) error {
	if err := DetachNetworkInterface(ctx, m.APIUrl, req); err != nil {
		return err
	}
	return nil
}

func (m *MsxfClient) GetENIDetail(ctx context.Context, req *DescribeNetworkInterfaceRequest) (*ENI, error) {
	var subnetDetail *ENI
	var err error

	data := &xcontext.CloudPlatform{
		Type: xcontext.PortalPlatform,
		Portal: &xcontext.Portal{
			RequestID:      req.RequestId,
			CloudAccountID: req.CloudAccountID,
			TenantID:       req.TenantID,
		},
	}
	nctx := xcontext.MockContext(ctx, data)
	nctx = context.WithValue(nctx, xcontext.PropagationKey, xcontext.MockPortalHeader(req.CloudAccountID, "", req.TenantID, req.RequestId))
	nctx = context.WithValue(nctx, xcontext.PropagationType, xcontext.PortalPlatform)

	if subnetDetail, err = DescribeNetworkInterface(nctx, m.APIUrl, req); err != nil {
		return nil, err
	}
	return subnetDetail, nil
}
