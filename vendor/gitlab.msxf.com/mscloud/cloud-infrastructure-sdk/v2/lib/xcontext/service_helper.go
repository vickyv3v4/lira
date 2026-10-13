package xcontext

import (
	"context"
	"errors"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/framework/core/xhttp"
	"sync"
)

const listServiceApiPath = "/noauth/cloud-gateway-server/api/v1/listService"

type ServiceHelper struct {
	Config  *CloudServiceConfig // 你的网关配置
	Service Service             // 你的服务
}

type ListServiceResp struct {
	RequestID string          `json:"requestId"`
	Code      int             `json:"code"`
	Msg       string          `json:"msg"`
	Data      ListServiceData `json:"data"`
	ErrorCode string          `json:"errorCode"`
	RawCode   int             `json:"rawCode"`
	RawMsg    string          `json:"rawMsg"`
}

type ServiceItem struct {
	ServiceName      string `json:"serviceName"`
	ServiceHost      string `json:"serviceHost"`
	LastRegistryTime string `json:"lastRegistryTime"`
	Token            string `json:"token"`
	ExpiredDate      string `json:"expiredDate"`
}

type ListServiceData struct {
	PageNo   int            `json:"pageNo"`
	PageSize int            `json:"pageSize"`
	Total    int            `json:"total"`
	List     []*ServiceItem `json:"list"`
}

var tokenCache sync.Map

// Validate 校验服务和token是否匹配
func (s *ServiceHelper) Validate(ctx context.Context, service Service, token string) (bool, error) {
	if len(token) == 0 {
		return false, errors.New("token is empty")
	}
	cachedToken, ok := tokenCache.Load(service)
	if !ok || cachedToken.(string) != token {
		srvList, err := s.GetService(ctx, &service)
		if err != nil {
			return false, err
		}
		if srvList == nil || len(srvList) == 0 {
			return false, errors.New("service info not exists")
		}
		tokenCache.Store(service, srvList[0].Token)
		return srvList[0].Token == token, nil
	}

	return cachedToken.(string) == token, nil
}

// GetService 按服务获取服务信息，当传入nil时，返回全部服务。
func (s *ServiceHelper) GetService(ctx context.Context, service *Service) ([]*ServiceItem, error) {
	header := make(map[string]string)
	header["service"] = s.Service.String()
	header["token"] = s.Config.Token

	var apiResp ListServiceResp

	srvName := ""
	if service != nil {
		srvName = service.String()
	}

	httpResp := xhttp.Post(&xhttp.Request{
		Method: xhttp.POST,
		Url:    s.Config.getURI(listServiceApiPath),
		Data: map[string]interface{}{
			"serviceName": srvName,
			"pageNo":      1,
			"pageSize":    1000,
		},
		ContentType: xhttp.JSON,
		HttpHeaders: header,
		Result:      &apiResp,
		Context:     ctx,
	})

	if httpResp.Status != 200 {
		return nil, errors.New("GetService-http response status_code err")
	}
	if apiResp.Code != 0 {
		return nil, errors.New("GetService-api response code err")
	}
	return apiResp.Data.List, nil
}
