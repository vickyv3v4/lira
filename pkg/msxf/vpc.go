package msxf

import (
	"context"
	"fmt"
	"time"

	"gitlab.msxf.com/icloud/cloud-common-go/core/xerrors"
	"gitlab.msxf.com/icloud/cloud-common-go/core/xhttp"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/core/xhttpclient"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"

	"github.com/google/go-querystring/query"
)

type AttachNetworkInterfaceRequest struct {
	ID             string `json:"-"`
	AZoneCode      int    `json:"azoneCode"`
	ServerID       string `json:"serverID"`
	DeviceID       string `json:"deviceID"`
	TargetType     string `json:"targetType"`
	TargetID       string `json:"targetID"`
	TenantID       string `json:"tenantID"`
	CloudAccountID string `json:"cloudAccountId"`
	RequestId      string `json:"requestId"`
}

type CommonResponse struct {
	xhttp.Rsp
}

func AttachNetworkInterface(ctx context.Context, apiHost string, request *AttachNetworkInterfaceRequest) (err error) {
	log := llog.Logger
	result := new(CommonResponse)
	response := xhttpclient.Post(ctx, &xhttp.Request{
		Url:         fmt.Sprintf("%s/portal/vpc-service/web/v1/network-interface/%s/attach-instance", apiHost, request.ID),
		ContentType: xhttp.JSON,
		Context:     ctx,
		Data:        request,
		Result:      &result,
	})
	log.Infof("AttachNetworkInterface req body: %+v , res: %+v, eni: %s, %+v", request, response, request.ID, ctx)
	if response.Error != nil {
		log.Errorf("AttachNetworkInterface, 调用vpc接口异常: %s", response.Error.Error())
		err = response.Error
		return
	}
	if result.Code != 0 {
		log.Errorf("AttachNetworkInterface, %+v 调用vpc接口响应异常: %s", request, result.Msg)
		err = xerrors.NewCustomError(result.Code, result.Msg)
		return
	}
	return
}

type DetachNetworkInterfaceRequest struct {
	ID             string `json:"-"`
	TenantID       string `json:"tenantID"`
	CloudAccountID string `json:"cloudAccountId"`
	RequestId      string `json:"requestId"`
}

func DetachNetworkInterface(ctx context.Context, apiHost string, request *DetachNetworkInterfaceRequest) (err error) {
	log := llog.Logger
	result := new(CommonResponse)
	response := xhttpclient.Post(ctx, &xhttp.Request{
		Url:         fmt.Sprintf("%s/portal/vpc-service/web/v1/network-interface/%s/detach-instance", apiHost, request.ID),
		ContentType: xhttp.JSON,
		Context:     ctx,
		Data:        request,
		Result:      &result,
	})
	log.Infof("DetachNetworkInterface req body: %+v , res: %+v,eni: %s", request, response, request.ID)
	if response.Error != nil {
		log.Errorf("DetachNetworkInterface, 调用vpc接口异常: %s", response.Error.Error())
		err = response.Error
		return
	}
	if result.Code != 0 {
		log.Errorf("DetachNetworkInterface, 调用vpc接口响应异常: %s", result.Msg)
		err = xerrors.NewCustomError(result.Code, result.Msg)
		return
	}
	return
}

type CreateNetworkInterfaceRequest struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	RegionCode      int      `json:"regionCode"`
	AZoneCode       int      `json:"azoneCode"`
	TenantID        string   `json:"tenantID"`
	CloudAccountID  string   `json:"cloudAccountId"`
	Type            int      `json:"type"`
	NetworkID       string   `json:"networkID"`
	SubnetID        string   `json:"subnetID"`
	SecurityGroups  []string `json:"securityGroups"`
	IPAddress       string   `json:"ipAddress"`
	OwnerType       string   `json:"ownerType"`
	HostIP          string   `json:"hostIP"`
	RequestId       string   `json:"requestId"`
	GrUniqueID      string   `json:"grUniqueId"`
	ResourceGroupID string   `json:"resourceGroupID"`
}

type ENI struct {
	ID                  string                  `json:"id"`
	OSID                string                  `json:"osID"`
	Name                string                  `json:"name"`
	Description         string                  `json:"description"`
	RegionID            string                  `json:"regionID"`
	RegionCode          int                     `json:"regionCode"`
	AZoneCode           int                     `json:"azoneCode"`
	RegionName          string                  `json:"regionName"`
	AZoneName           string                  `json:"azoneName"`
	TenantID            string                  `json:"tenantID"`
	ProjectID           string                  `json:"projectID"`
	IPAddress           string                  `json:"ipAddress"`
	MacAddress          string                  `json:"macAddress"`
	NetworkID           string                  `json:"networkID"`
	SubnetID            string                  `json:"subnetID"`
	Status              string                  `json:"status"`
	Type                int                     `json:"type"`
	ServerID            string                  `json:"serverID"`
	DeviceID            string                  `json:"deviceID"`
	TargetType          string                  `json:"targetType"`
	TargetID            string                  `json:"targetID"`
	SecondaryPrivateIPs []ENISecondaryPrivateIP `json:"secondaryPrivateIPs"`
	SecurityGroups      []string                `json:"securityGroups"`
	Subnet              *SubnetBrief            `json:"subnet"`
	ElasticIP           *ElasticIPBrief         `json:"eip"`
	OwnerType           string                  `json:"ownerType"`
	CreatedAt           string                  `json:"createdAt"`
	UpdatedAt           string                  `json:"updatedAt"`
}
type ElasticIPBrief struct {
	ID                string `json:"id"`
	OSID              string `json:"osID"`
	Name              string `json:"name"`
	FloatingIPAddress string `json:"floatingIpAddress"`
	Status            string `json:"status"`
}

type SubnetBrief struct {
	ID        string `json:"id"`
	OSID      string `json:"osID"`
	Name      string `json:"name"`
	CIDR      string `json:"cidr"`
	GatewayIP string `json:"gatewayIP"`
}

type ENISecondaryPrivateIP struct {
	SubnetID  string `json:"subnetID"`
	IPAddress string `json:"ipAddress"`
}
type CreateNetworkInterfaceResponse struct {
	xhttp.Rsp
	Data ENI `json:"data"`
}

func CreateNetworkInterface(ctx context.Context, apiHost string, request *CreateNetworkInterfaceRequest) (eni *ENI, err error) {
	log := llog.Logger

	result := new(CreateNetworkInterfaceResponse)
	response := xhttpclient.Post(ctx, &xhttp.Request{
		Url:         fmt.Sprintf("%s/portal/vpc-service/web/v2/network-interface", apiHost),
		ContentType: xhttp.JSON,
		Context:     ctx,
		Data:        request,
		Result:      &result,
		Timeout:     time.Duration(600) * time.Second,
	})
	log.Infof("CreateNetworkInterface req body: %+v , res: %+v ,ctx: %+v", request, response, ctx)
	if response.Error != nil {
		log.Errorf("CreateNetworkInterface, 调用vpc接口异常: %s", response.Error.Error())
		err = response.Error
		return
	}
	if result.Code != 0 {
		log.Errorf("CreateNetworkInterface, %+v 调用vpc接口响应异常: %s", request, result.Msg)
		err = xerrors.NewCustomError(result.Code, result.Msg)
		return
	}
	eni = &result.Data
	return
}

type DeleteNetworkInterfaceRequest struct {
	ID             string `json:"-"`
	TenantID       string `json:"tenantID"`
	CloudAccountID string `json:"cloudAccountId"`
	RequestId      string `json:"requestId"`
}

func DeleteNetworkInterface(ctx context.Context, apiHost string, request *DeleteNetworkInterfaceRequest) (err error) {
	log := llog.Logger

	result := new(CommonResponse)
	response := xhttpclient.Delete(ctx, &xhttp.Request{
		Url:         fmt.Sprintf("%s/portal/vpc-service/web/v1/network-interface/%s", apiHost, request.ID),
		ContentType: xhttp.JSON,
		Context:     ctx,
		Data:        request,
		Result:      &result,
		Timeout:     time.Duration(600) * time.Second,
	})
	log.Infof("DeleteNetworkInterface req body: %+v , res: %+v, eni: %s", request, response, request.ID)
	if response.Error != nil {
		log.Errorf("DeleteNetworkInterface, 调用vpc接口异常: %s", response.Error.Error())
		err = response.Error
		return
	}
	// 0:SUCCESS
	// -11005000:NotFound
	if result.Code != 0 && result.Code != -11005000 {
		log.Errorf("DeleteNetworkInterface, %+v 调用vpc接口响应异常: %s", request, result.Msg)
		err = xerrors.NewCustomError(result.Code, result.Msg)
		return
	}
	return
}

type GetSubNetDetailReq struct {
	ID string `json:"id"`
}
type VPCNetwork struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type SubnetRoute struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}
type SubnetDetail struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Description    string      `json:"description"`
	Status         string      `json:"status"`
	CreatedAt      string      `json:"createdAt"`
	UpdatedAt      string      `json:"updatedAt"`
	Network        VPCNetwork  `json:"network"`
	Router         SubnetRoute `json:"router"`
	Cidr           string      `json:"cidr"`
	IsDefault      int         `json:"isDefault"`
	TenantID       string      `json:"tenantID"`
	AzoneCode      int         `json:"azoneCode"`
	AvailableIPNum int         `json:"availableIPNum"`
	TotalIPNum     int         `json:"totalIPNum"`
}
type GetSubNetDetailResponse struct {
	xhttp.Rsp
	Data SubnetDetail `json:"data"`
}

func GetSubNetDetailInterface(ctx context.Context, apiHost string, req *GetSubNetDetailReq) (subnetDetail *SubnetDetail, err error) {
	result := new(GetSubNetDetailResponse)
	data, _ := query.Values(req)
	response := xhttpclient.Get(ctx, &xhttp.Request{
		Url:     fmt.Sprintf("%s/portal/vpc-service/web/v1/subnet/%s", apiHost, data.Encode()),
		Context: ctx,
		Result:  &result,
	})
	if response.Error != nil {
		err = response.Error
		return
	}
	if result.Code != 0 {
		err = xerrors.NewCustomError(result.Code, result.Msg)
		return
	}
	subnetDetail = &result.Data
	err = xerrors.NewAssignCodeError(xerrors.CommonResourceNotFoundError)
	return
}

type DescribeNetworkInterfaceRequest struct {
	ID             string `form:"id"`
	TenantID       string `form:"tenantID"`
	CloudAccountID string `json:"cloudAccountId"`
	RequestId      string `json:"requestId"`
}

type DescribeNetworkInterfaceResponse struct {
	xhttp.Rsp
	Data ENI `json:"data"`
}

func DescribeNetworkInterface(ctx context.Context, apiHost string, request *DescribeNetworkInterfaceRequest) (eni *ENI, err error) {
	log := llog.Logger

	result := new(DescribeNetworkInterfaceResponse)
	response := xhttpclient.Get(ctx, &xhttp.Request{
		Url:     fmt.Sprintf("%s/portal/vpc-service/web/v1/network-interface/%s", apiHost, request.ID),
		Context: ctx,
		Result:  &result,
	})
	log.Infof("DescribeNetworkInterface req body: %+v , res: %+v", request, response)
	if response.Error != nil {
		log.Errorf("DescribeNetworkInterface, 调用vpc接口异常: %s", response.Error.Error())
		err = response.Error
		return
	}
	if result.Code != 0 {
		log.Errorf("DescribeNetworkInterface, %+v 调用vpc接口响应异常: %s", request, result.Msg)
		err = xerrors.NewCustomError(result.Code, result.Msg)
		return
	}
	eni = &result.Data
	return
}

type FixedIPList struct {
	IPAddress string `json:"ipAddress"`
	SubnetID  string `json:"subnetID"`
}

type HandleENISecondaryIPRequest struct {
	ID        string        `json:"id"`
	FixedIPs  []FixedIPList `json:"fixedIPs"`
	AzoneCode int           `json:"azoneCode"`
}

type SecondaryIPsInfo struct {
	ID                    string                `json:"id"`
	OSID                  string                `json:"osID"`
	Name                  string                `json:"name"`
	Description           string                `json:"description"`
	TenantID              string                `json:"tenantID"`
	RegionCode            int                   `json:"regionCode"`
	RegionName            string                `json:"regionName"`
	AzoneCode             int                   `json:"azoneCode"`
	AzoneName             string                `json:"azoneName"`
	IPAddress             string                `json:"ipAddress"`
	MacAddress            string                `json:"macAddress"`
	NetworkID             string                `json:"networkID"`
	SubnetID              string                `json:"subnetID"`
	Status                string                `json:"status"`
	ServerID              string                `json:"serverID"`
	Type                  int                   `json:"type"`
	TargetID              string                `json:"targetID"`
	TargetType            string                `json:"targetType"`
	SecondaryPrivateIPs   []SecondaryPrivateIPs `json:"secondaryPrivateIPs"`
	CreatedAt             string                `json:"createdAt"`
	UpdatedAt             string                `json:"updatedAt"`
	Subnet                SubnetInfo            `json:"subnet"`
	OwnerType             string                `json:"ownerType"`
	IngressBandwidthLimit int                   `json:"ingressBandwidthLimit"`
	EgressBandwidthLimit  int                   `json:"egressBandwidthLimit"`
}
type SubnetInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Cidr      string `json:"cidr"`
	GatewayIP string `json:"gatewayIP"`
}
type SecondaryPrivateIPs struct {
	IPAddress string `json:"ipAddress"`
	SubnetID  string `json:"subnetID"`
}
type HandleENISecondaryIPResponse struct {
	xhttp.Rsp
	Data SecondaryIPsInfo `json:"data"`
}

func HandleMultiENISecondaryIP(ctx context.Context, apiHost string, request *HandleENISecondaryIPRequest) (ipsInfo *SecondaryIPsInfo, err error) {
	log := llog.Logger

	result := new(HandleENISecondaryIPResponse)
	response := xhttpclient.Post(ctx, &xhttp.Request{
		Url:         fmt.Sprintf("%s/portal/vpc-service/web/v1/network-interface/%s/secondary-private-ip", apiHost, request.ID),
		ContentType: xhttp.JSON,
		Context:     ctx,
		Data:        request,
		Result:      &result,
		Timeout:     time.Duration(600) * time.Second,
	})
	log.Infof("HandleENISecondaryIPRequest req body: %+v , res: %+v ,ctx: %+v", request, response, ctx)
	if response.Error != nil {
		log.Errorf("HandleENISecondaryIP, 调用vpc接口异常: %s", response.Error.Error())
		err = response.Error
		return
	}
	if result.Code != 0 {
		log.Errorf("HandleENISecondaryIP, %+v 调用vpc接口响应异常: %s", request, result.Msg)
		err = xerrors.NewCustomError(result.Code, result.Msg)
		return
	}
	ipsInfo = &result.Data
	return
}
