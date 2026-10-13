package xhttp

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/lib/xservicecalledtool"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/framework/core/xerrors"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/framework/core/xlog"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/framework/core/xpromethus"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/framework/core/xserializer"
)

// PropagationKey 传播平台上下文的Key
// 注：为了避免循环引用，这个常量在xcontext模块有重新定义，修改此值需要同步修改xhttp的同名常量
const PropagationKey = "platform-ctx"

// PropagationType 传播平台上下文的类型的Key
// 注：为了避免循环引用，这个常量在xcontext模块有重新定义，修改此值需要同步修改xhttp的同名常量
const PropagationType = "ctx-type"

// 应用间调用签名
const ServiceSignature = "x-cloud-app-call-signature"

type Method int

const (
	GET Method = 1 + iota
	POST
	PUT
	DELETE
	HEAD
)

var methods = [...]string{
	"GET",
	"POST",
	"PUT",
	"DELETE",
	"HEAD",
}

func (method Method) String() string {
	return methods[method-1]
}

type ContentType int

const (
	// JSON
	JSON ContentType = 1 + iota
	// FORM DATA
	FORM_DATA
	// X-WWW-FORM-URLENCODED
	FORM_URLENCODED
	NULL_CONTENT
	// 自定义req body 直接传不convert
	CUSTOMIZE
)

type HttpHeaders map[string]string

type Response struct {
	// HTTP Status Code
	Status int
	// 消息体
	Content string
	// 响应时间
	Elapsed time.Duration
	// 错误消息
	Error error
}

type Request struct {
	// Http Request Method
	Method Method
	// 完整URL
	Url string
	// 请求体类型
	ContentType ContentType
	// 请求体数据
	Data interface{}
	// 请求头
	HttpHeaders HttpHeaders
	// 接受数据类型
	Result interface{}
	// 消息编码类型 (optional : default JSON)
	EncodingType xserializer.EncoderType
	// 超时时间
	Timeout time.Duration
	// 重试次数
	RetryCount int
	// 重试间隔
	RetryInterval time.Duration
	// ctx
	context.Context
}

func (r *Request) Validate() error {
	if r.Url == "" {
		return xerrors.NewAssignCodeError(xerrors.CommonIllegalArgError)
	}
	return nil
}

type HttpClientConfig struct {
	sync.RWMutex
	// Http Basic 认证用户名
	HttpUser string
	// Http Basic 认证密码
	HttpPass string
	// Access Token
	AccessToken string
	// 请求超时时间
	RequestTimeout int
	// 是否启用 TLS
	TLSInsecureSkipVerify bool
}

type httpClient struct {
	config *HttpClientConfig
	http   *http.Client
}

type HttpClient interface {
	Get(request *Request) *Response

	Put(request *Request) *Response

	Delete(request *Request) *Response

	Post(request *Request) *Response
}

var (
	// 单例 Http Client
	client = DefaultHttpClient()
)

// 实例化默认 HTTP Client
func DefaultHttpClient() HttpClient {
	return NewHttpClient(NewDefaultConfig())
}

// 默认 HTTP CLIENT 配置
func NewDefaultConfig() *HttpClientConfig {
	return &HttpClientConfig{RequestTimeout: 30, TLSInsecureSkipVerify: false}
}

func NewHttpClient(config *HttpClientConfig) HttpClient {
	hc := &httpClient{
		config: config,
		http: &http.Client{
			Timeout: time.Duration(config.RequestTimeout) * time.Second,
		},
	}

	if config.TLSInsecureSkipVerify {
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
		hc.http.Transport = tr
	}

	// wrap original http client to pinpoint-client
	hc.http = pphttp.WrapClient(hc.http)
	return hc
}

func NewResponse(status int, elapsed time.Duration, content string, err error) *Response {
	return &Response{Status: status, Elapsed: elapsed, Content: content, Error: err}
}

func Get(request *Request) *Response {
	return client.Get(request)
}

func Post(request *Request) *Response {
	return client.Post(request)
}

func Put(request *Request) *Response {
	return client.Put(request)
}

func Delete(request *Request) *Response {
	return client.Delete(request)
}

func (h *httpClient) Get(request *Request) *Response {
	request.Method = GET
	return h.invoke(request)
}

func (h *httpClient) Post(request *Request) *Response {
	request.Method = POST
	return h.invoke(request)
}

func (h *httpClient) Put(request *Request) *Response {
	request.Method = PUT
	return h.invoke(request)
}

func (h *httpClient) Delete(request *Request) *Response {
	request.Method = DELETE
	return h.invoke(request)
}

func (h *httpClient) invoke(r *Request) *Response {
	if r.Context == nil {
		r.Context = context.Background()
	}

	var err error
	err = r.Validate()
	if err != nil {
		return &Response{Error: err}
	}

	u, err := url.Parse(r.Url)
	if err != nil {
		return &Response{Error: err}
	}

	var (
		_contextHost = xpromethus.MetricManager.RegisterMetric("out_generic_host_" + u.Host).Start()
		_contextPath = xpromethus.MetricManager.RegisterMetric("out_generic_path_" + u.Path).Start()
	)

	defer func() {
		if err != nil {
			_contextHost.Failure(false)
			_contextPath.Failure(false)
			return
		}
		_contextHost.Success()
		_contextPath.Success()
	}()

	xlog.CtxDebugf(r.Context, "%s - %s, Body:\n%v", r.Method.String(), r.Url, r.Data)

	var body io.Reader

	data := r.Data

	if r.HttpHeaders == nil {
		r.HttpHeaders = make(HttpHeaders)
	}

	if data != nil {
		switch r.ContentType {
		case JSON:
			body = strings.NewReader(h.convertJsonData(data))
			r.HttpHeaders["Content-Type"] = "application/json;charset=UTF-8"
		case FORM_URLENCODED:
			if v, ok := data.(url.Values); ok {
				body = strings.NewReader(v.Encode())
			}
			r.HttpHeaders["Content-Type"] = "application/x-www-form-urlencoded"
		case FORM_DATA:
			reader, err := h.convertFormData(data, r.HttpHeaders)
			if err != nil {
				return &Response{Error: err}
			}
			body = reader
		case NULL_CONTENT:
			body = nil
		case CUSTOMIZE:
			body = data.(io.Reader)
		default:
			return &Response{
				Error: xerrors.NewCustomError(xerrors.CommonIllegalArgError, "contentType only support JSON | FORM_DATA | FORM_URLENCODED |NULL_CONTENT"),
			}
		}
	}

	// 添加网关开始传递的上下文信息
	if propagationType, ok := r.Context.Value(PropagationType).(string); ok {
		r.HttpHeaders[PropagationType] = propagationType
	}
	if propagationKey, ok := r.Context.Value(PropagationKey).(string); ok {
		r.HttpHeaders[PropagationKey] = propagationKey
	}
	// 设置应用鉴权header
	if xservicecalledtool.Initialized() {
		r.HttpHeaders[ServiceSignature] = xservicecalledtool.GenReqSignature()
	}

	request, err := http.NewRequestWithContext(r.Context, r.Method.String(), r.Url, body)

	if err != nil {
		return &Response{Error: err}
	}

	addHeaders(request, r)
	addAuthentication(h.config, request)

	if r.Timeout > 0 {
		h.http.Timeout = r.Timeout
	}

	reqStart := time.Now()

	var response *http.Response
	for {
		response, err = h.http.Do(request)
		if err != nil {
			if r.RetryCount > 0 {
				r.RetryCount--
				time.Sleep(r.RetryInterval)
			} else {
				break
			}
		} else {
			break
		}
	}
	reqEnd := time.Now()
	reqElapsed := reqEnd.Sub(reqStart)

	if err != nil {
		return NewResponse(0, reqElapsed, "", err)
	}

	defer response.Body.Close()
	status := response.StatusCode
	var content string
	if response.ContentLength != 0 {
		rc, err := ioutil.ReadAll(response.Body)
		if err != nil {
			return NewResponse(status, reqElapsed, "", err)
		}
		content = string(rc)
	}

	if status >= http.StatusOK && status < http.StatusMultipleChoices {

		if r.Result != nil {
			h.convert(r, content)
		}
		return NewResponse(status, reqElapsed, content, nil)
	}

	_contextHost.Failure(false)
	_contextPath.Failure(false)

	switch status {
	case 500:
		return NewResponse(status, reqElapsed, content, xerrors.NewAssignCodeError(xerrors.InternalError))
	case 404:
		return NewResponse(status, reqElapsed, content, xerrors.NewAssignCodeError(xerrors.CommonIllegalArgError))
	case 403:
		return NewResponse(status, reqElapsed, content, xerrors.NewAssignCodeError(xerrors.CommonIllegalArgError))
	case 401:
		return NewResponse(status, reqElapsed, content, xerrors.NewAssignCodeError(xerrors.CommonIllegalArgError))
	case 400:
		return NewResponse(status, reqElapsed, content, xerrors.NewAssignCodeError(xerrors.CommonIllegalArgError))
	}

	return NewResponse(status, reqElapsed, content, xerrors.NewAssignCodeError(-1))
}

func (h *httpClient) convertFormData(data interface{}, httpHeaders HttpHeaders) (*bytes.Buffer, error) {
	var bufReader bytes.Buffer

	if data == nil {
		return &bufReader, nil
	}

	writer := multipart.NewWriter(&bufReader)

	if val, ok := data.(map[string]string); ok {
		for k, v := range val {
			writer.WriteField(k, v)
		}
	} else {
		return &bufReader, xerrors.NewCustomError(xerrors.InternalError, "convert error")
	}

	httpHeaders["Content-Type"] = writer.FormDataContentType()

	writer.Close()

	return &bufReader, nil
}

func (h *httpClient) convertJsonData(data interface{}) string {
	if data == nil {
		return ""
	}
	encoder, _ := xserializer.NewEncoder(xserializer.JSON)
	body, _ := encoder.Marshal(data)
	return body
}

func (h *httpClient) convert(r *Request, content string) error {
	um, _ := xserializer.NewEncoder(xserializer.JSON)
	if r.EncodingType != 0 {
		um, _ = xserializer.NewEncoder(r.EncodingType)
	}
	um.UnMarshalStr(content, r.Result)
	return nil

}

func addHeaders(req *http.Request, r *Request) {
	headers := r.HttpHeaders
	if headers != nil {
		for k, v := range headers {
			req.Header.Add(k, v)
		}
	}

	req.Header.Set("Accept", "application/json;charset=UTF-8")
}

func addAuthentication(c *HttpClientConfig, req *http.Request) {
	if c.HttpUser != "" {
		req.SetBasicAuth(c.HttpUser, c.HttpPass)
	}

	if c.AccessToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.AccessToken))
	}
}
