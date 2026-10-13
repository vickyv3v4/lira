package xservicecalledtool

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/tjfoc/gmsm/sm3"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/framework/core/xerrors"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/framework/core/xlog"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/framework/core/xpromethus"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/lib/xutils"
	"strings"
	"time"
)

const (
	Semicolon           = ";"
	CloudGatewayServer  = "gcloudgwserver"
	CloudGatewayServerB = "gc-bgw-server"
	ServiceSignature    = "x-cloud-app-call-signature"
	MetricCallAuthReceiverErr = "cloudmonitor_%scalled_auth_conf_receive_err"
)

type ServiceAuth struct {
	AppCalledAuth bool              `json:"appCalledAuth"`
	StrictModel   bool              `json:"strictModel"`
	AllowApps     map[string]string `json:"allowApps"`
}

type KeepaliveResponse struct {
	RequestId string            `json:"requestId"`
	Code      int               `json:"code"`
	Msg       string            `json:"msg"`
	Data      KeepaliveRespData `json:"data"`
}

type KeepaliveRespData struct {
	RegistrySerial    string        `json:"registrySerial"`
	StrictModel       bool          `json:"strictModel"`
	AppCalledAuth     bool          `json:"appCalledAuth"`
	AppCalledAuthList []appAuthList `json:"appCalledAuthList"`
	DefaultAuthList   []appAuthList `json:"defaultAuthList"`
}

type appAuthList struct {
	SourceApp      string `json:"sourceApp"`
	SourceAppToken string `json:"sourceAppToken"`
}

// 更新授权配置
func (k KeepaliveRespData) GenCalledAuthConf() (ServiceAuth, error) {
	conf := ServiceAuth{AllowApps: make(map[string]string)}
	conf.AppCalledAuth = k.AppCalledAuth
	conf.StrictModel = k.StrictModel
	for _, authConf := range k.AppCalledAuthList {
		appToken, err := xutils.Decrypt([]byte(Manager.ServiceToken[0:16]), authConf.SourceAppToken)
		// sm4解密失败
		if err != nil {
			return conf, err
		}
		conf.AllowApps[authConf.SourceApp] = string(appToken)
	}

	for _, authConf := range k.DefaultAuthList {
		appToken, err := xutils.Decrypt([]byte(Manager.ServiceToken[0:16]), authConf.SourceAppToken)
		// sm4解密失败
		if err != nil {
			return conf, err
		}
		conf.AllowApps[authConf.SourceApp] = string(appToken)
	}
	return conf, nil
}

// 更新授权配置http接口
func UpdateCalledAuthConf(c *gin.Context) (interface{}, error) {
	xlog.CtxInfof(c.Request.Context(), "接收到服务调用鉴权策略更新, sign:%s", c.GetHeader(ServiceSignature))
	err := doUpdateCalledAuthConf(c)
	if err != nil {
		xpromethus.MetricManager.RegisterCounter(fmt.Sprintf(MetricCallAuthReceiverErr, Manager.ServiceName)).Increment()
	}
	return nil, err
}

func doUpdateCalledAuthConf(c *gin.Context) error {
	var req KeepaliveRespData
	err := c.ShouldBindJSON(&req)
	if err != nil {
		return err
	}
	signature := c.GetHeader(ServiceSignature)
	decodeString, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return err
	}
	services := strings.Split(string(decodeString), Semicolon)
	if len(services) == 0 {
		return xerrors.NewCustomError(xerrors.CommonIllegalArgError, "headers signature不能为空")
	}
	if CloudGatewayServer != services[0] && CloudGatewayServerB != services[0] {
		return xerrors.NewCustomError(xerrors.CommonIllegalArgError, "仅限网关服务可更新服务调用配置")
	}
	// 构建新配置
	authConf, err := req.GenCalledAuthConf()
	if err != nil {
		return err
	}
	Manager.ServiceValidator.UpdateConf(authConf)
	return nil
}

// 生成应用授权签名 http header
func GenReqSignature() string {
	milli := time.Now().UnixMilli()
	hashData := sm3.New().Sum([]byte(fmt.Sprintf("%s:%s:%d", Manager.ServiceName, Manager.ServiceToken, milli)))
	toSign := fmt.Sprintf("%s;%s;%d", Manager.ServiceName, hex.EncodeToString(hashData), milli)
	return base64.StdEncoding.EncodeToString([]byte(toSign))
}

func Initialized() bool {
	if Manager.ServiceName == "" || Manager.ServiceToken == "" {
		return false
	}
	return true
}
