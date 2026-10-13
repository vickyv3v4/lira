/**
* @Author: wei.peng03
* @Date: 2022/7/20
 */

package xcontext

import (
	"fmt"
	"gitlab.msxf.com/icloud/cloud-common-go/core/xhttp"
	"gitlab.msxf.com/icloud/cloud-common-go/core/xlog"
	"net"
)

type CloudServiceConfig struct {
	Token string `json:"token" mapstructure:"token"`
	Host string `json:"host" mapstructure:"host"`
	Gateway string `json:"gateway" mapstructure:"gateway"`
}

const RegistryUri = "/noauth/cloud-gateway-admin/api/v1/registryService"

func RegistryService(config *CloudServiceConfig, service Service) {
	if config.Token == "" {
		panic(fmt.Errorf("token not config"))
	}

	if config.Host == "" {
		panic(fmt.Errorf("host not config"))
	}

	if config.Gateway == "" {
		panic(fmt.Errorf("gateway not config"))
	}

	endpoint, err := getEndpoint()
	if err != nil {
		panic(fmt.Errorf("get endpoint failed :%v", err))
	}

	type registerData struct {
		ServiceName string `json:"serviceName"`
		ServiceHost string `json:"serviceHost"`
		Endpoint string `json:"endpoint"`
	}

	data := registerData{
		ServiceName: string(service),
		ServiceHost: config.Host,
		Endpoint:    endpoint,
	}

	header := make(map[string]string)
	header["service"] = string(service)
	header["token"] = config.Token

	respData := &CloudResponse{}

	req := &xhttp.Request{
		Method:        xhttp.POST,
		Url:           fmt.Sprintf("http://%s%s", config.Gateway, RegistryUri),
		ContentType:   xhttp.JSON,
		Data:          data,
		HttpHeaders:   header,
		Result:        respData,
	}

	resp := xhttp.Post(req)
	if resp.Status != 200 {
		panic(fmt.Errorf("service register failed"))
	}

	if respData.Code != 0 {
		panic(fmt.Errorf("service register failed :%s", respData.Msg))
	}

	xlog.Info("服务注册成功")
}

func getEndpoint() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil{
			return ipnet.IP.String(), nil
		}
	}

	return "", fmt.Errorf("service endpoint not found")
}