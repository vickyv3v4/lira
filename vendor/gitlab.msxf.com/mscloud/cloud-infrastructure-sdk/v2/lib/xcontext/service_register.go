/**
* @Author: wei.peng03
* @Date: 2022/7/20
 */

package xcontext

import (
	"encoding/hex"
	"fmt"
	"github.com/tjfoc/gmsm/sm3"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/framework/core/xgateway"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/lib/xservicecalledtool"
	"net"
	"time"

	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/framework/core/xhttp"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/framework/core/xlog"
)

const DefaultKeepaliveSecond = 180
const defaultPort = 8080

type CloudServiceConfig struct {
	Token   string `json:"token" mapstructure:"token"`
	Host    string `json:"host" mapstructure:"host"`
	Gateway string `json:"gateway" mapstructure:"gateway"`
}

type keepaliver struct {
	config  *xgateway.CloudServiceConfig
	service Service
	port    int
	serial  string
}

func (c *CloudServiceConfig) getURI(path string) string {
	return fmt.Sprintf("http://%s%s", c.Gateway, path)
}

const RegistryUri = "/noauth/cloud-gateway-server/api/v1/registryService"
const RegistryV2Uri = "/noauth/cloud-gateway-server/api/v2/registryService"
const KeepAliveUri = "/noauth/cloud-gateway-server/api/v1/keepAliveService"

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
		Endpoint    string `json:"endpoint"`
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
		Method:      xhttp.POST,
		Url:         config.getURI(RegistryUri),
		ContentType: xhttp.JSON,
		Data:        data,
		HttpHeaders: header,
		Result:      respData,
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

func RegistryServiceV2(config *xgateway.CloudServiceConfig, service Service, port int) {
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
	if port <= 0 {
		port = defaultPort
	}

	type registerData struct {
		ServiceName string `json:"serviceName"`
		ServiceHost string `json:"serviceHost"`
		Endpoint    string `json:"endpoint"`
		Signature   string `json:"signature"`
	}

	hash := sm3.New()
	data := registerData{
		ServiceName: string(service),
		ServiceHost: config.Host,
		Endpoint:    fmt.Sprintf("%s:%d", endpoint, port),
		Signature:   hex.EncodeToString(hash.Sum([]byte(string(service) + ":" + config.Token))),
	}

	header := make(map[string]string)
	header["service"] = string(service)
	header["token"] = config.Token

	respData := &xservicecalledtool.KeepaliveResponse{}

	req := &xhttp.Request{
		Method:      xhttp.POST,
		Url:         config.GetURI(RegistryV2Uri),
		ContentType: xhttp.JSON,
		Data:        data,
		HttpHeaders: header,
		Result:      respData,
	}

	resp := xhttp.Post(req)
	if resp.Status != 200 {
		panic(fmt.Errorf("service register failed"))
	}

	if respData.Code != 0 {
		panic(fmt.Errorf("service register failed :%s", respData.Msg))
	}

	// cache token, 启动心跳，存储授权列表
	xservicecalledtool.Manager.SetServiceAndToken(string(service), config.Token)
	authConf, err := respData.Data.GenCalledAuthConf()
	if err != nil {
		panic(fmt.Errorf("service register failed, updateCalledAuthConf err: %v", err))
	}
	xservicecalledtool.Manager.ServiceValidator.UpdateConf(authConf)
	xlog.Info("服务注册成功")
	k := keepaliver{
		config:  config,
		service: service,
		port: port,
		serial:  respData.Data.RegistrySerial,
	}
	xservicecalledtool.Manager.Registrar = &k
	go k.KeepAlive()
}

func getEndpoint() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String(), nil
		}
	}

	return "", fmt.Errorf("service endpoint not found")
}

func (k keepaliver) KeepAlive() {
	ticker := time.NewTicker(time.Second * DefaultKeepaliveSecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			k.doKeepAlive()
		}
	}
}

func (k keepaliver) doKeepAlive() {
	endpoint, err := getEndpoint()
	if err != nil {
		xlog.Errorf("get endpoint failed :%v", err)
		return
	}
	type keepAliveData struct {
		ServiceName string `json:"serviceName"`
		ServiceHost string `json:"serviceHost"`
		Endpoint    string `json:"endpoint"`
		Serial      string `json:"serial"`
	}

	data := keepAliveData{
		ServiceName: string(k.service),
		ServiceHost: k.config.Host,
		Endpoint:    fmt.Sprintf("%s:%d", endpoint, k.port),
		Serial:      k.serial,
	}

	respData := &xservicecalledtool.KeepaliveResponse{}

	req := &xhttp.Request{
		Method:      xhttp.POST,
		Url:         k.config.GetURI(KeepAliveUri),
		ContentType: xhttp.JSON,
		Data:        data,
		Result:      respData,
	}

	resp := xhttp.Post(req)
	if resp.Status != 200 {
		xlog.Errorf("service keepalive failed")
		return
	}

	if respData.Code != 0 {
		xlog.Errorf("service keepalive failed :%s", respData.Msg)
		return
	}

	// 更新服务授权列表
	authConf, err := respData.Data.GenCalledAuthConf()
	if err != nil {
		xlog.Errorf("service keepalive updateCalledAuthConf failed")
		return
	}
	xservicecalledtool.Manager.ServiceValidator.UpdateConf(authConf)
}

func (k keepaliver) Refresh() {
	k.doKeepAlive()
}
