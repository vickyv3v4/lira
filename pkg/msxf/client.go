package msxf

import (
	"context"

	"golang.org/x/time/rate"
)

type PropagationType string

const (
	CtxPropagationType     PropagationType = "ctx-type"
	PPortalPlatform        PropagationType = "portal"
	PlatformPropagationKey PropagationType = "platform-ctx"
)

var defaultLimit = map[string]int{
	"":                          3,
	"AttachNetworkInterface":    2,
	"CreateNetworkInterface":    2,
	"DeleteNetworkInterface":    2,
	"DescribeNetworkInterfaces": 2,
}

type RateLimiter struct {
	rateList map[string]*rate.Limiter
}

func NewRateLimiter() *RateLimiter {
	r := &RateLimiter{
		rateList: make(map[string]*rate.Limiter),
	}
	for k, v := range defaultLimit {
		r.rateList[k] = rate.NewLimiter(rate.Limit(float64(v)/60), v)
	}

	return r
}

type MsxfClient struct {
	APIUrl string
}

func NewMsxfClient(url string) MsxfClientAPI {
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

	if subnetDetail, err = DescribeNetworkInterface(ctx, m.APIUrl, req); err != nil {
		return nil, err
	}
	return subnetDetail, nil
}

func (m *MsxfClient) HandleENISecondaryIP(ctx context.Context, req *HandleENISecondaryIPRequest) (*SecondaryIPsInfo, error) {
	var err error
	var ipsInfo *SecondaryIPsInfo
	if ipsInfo, err = HandleMultiENISecondaryIP(ctx, m.APIUrl, req); err != nil {
		return nil, err
	}
	return ipsInfo, nil
}
