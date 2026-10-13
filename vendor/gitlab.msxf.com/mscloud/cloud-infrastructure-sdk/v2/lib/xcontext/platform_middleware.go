package xcontext

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/duke-git/lancet/cryptor"
	"github.com/gin-gonic/gin"
)

type (
	UrlPrefix   string
	platformKey struct{}
)

// PropagationKey 传播平台上下文的Key
// 注：为了避免循环引用，这个常量在xhttp模块有重新定义，修改此值需要同步修改xhttp的同名常量
const PropagationKey = "platform-ctx"

// PropagationType 传播平台上下文的类型的Key
// 注：为了避免循环引用，这个常量在xhttp模块有重新定义，修改此值需要同步修改xhttp的同名常量
const PropagationType = "ctx-type"

// 应用间调用签名
const ServiceSignature = "x-cloud-app-call-signature"

const ReqId = "reqId"

const (
	ParamDeployTenantIdKey = "deployTenantId"
)

// CloudPlatformContext 提供给gin用于构造平台统一上下文的中间件
func CloudPlatformContext(c *gin.Context) {
	if c.GetHeader(PropagationType) == "" {
		c.Next()
		return
	}
	// load and set platform context into origin context
	platform, t, o, reqId := load(c)
	ctx := context.WithValue(c.Request.Context(), platformKey{}, platform)
	ctx = context.WithValue(ctx, PropagationKey, o)
	ctx = context.WithValue(ctx, PropagationType, t)
	ctx = context.WithValue(ctx, ReqId, reqId)

	// clone with context
	c.Request = c.Request.WithContext(ctx)
	// 后续Log是直接用ctx.Value()取的ReqId
	c.Set(ReqId, reqId)
	c.Next()
}

func Clone(cloudContext context.Context, newContext context.Context) context.Context {
	if newContext == nil {
		newContext = context.Background()
	}
	if cloudContext == nil {
		return newContext
	}

	ctx := context.WithValue(newContext, platformKey{}, cloudContext.Value(platformKey{}))
	ctx = context.WithValue(ctx, PropagationKey, cloudContext.Value(PropagationKey))
	ctx = context.WithValue(ctx, PropagationType, cloudContext.Value(PropagationType))
	ctx = context.WithValue(ctx, ReqId, cloudContext.Value(ReqId))
	return ctx
}

func load(c *gin.Context) (*CloudPlatform, string, string, string) {
	t := c.GetHeader(PropagationType)
	if t == "" {
		return nil, "", "", ""
	}
	p := new(CloudPlatform)
	p.Type = t
	origin := c.GetHeader(PropagationKey)
	if origin == "" {
		return nil, "", "", ""
	}

	// base64 decode
	data := cryptor.Base64StdDecode(origin)

	switch t {
	case AdminPlatform:
		a := new(Admin)
		_ = json.Unmarshal([]byte(data), &a)
		p.Admin = a
	case PortalPlatform:
		a := new(Portal)
		_ = json.Unmarshal([]byte(data), &a)
		p.Portal = a
	case NoauthPlatform:
		a := new(NoAuth)
		_ = json.Unmarshal([]byte(data), &a)
		p.NoAuth = a
	}

	m := make(map[string]interface{})
	_ = json.Unmarshal([]byte(data), &m)
	i := m["requestId"]
	s, ok := i.(string)
	if !ok {
		s = ""
	}
	return p, t, origin, s
}

// RamAccountId get ramAccountId from context
func RamAccountId(ctx context.Context) string {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return ""
	}
	if p == nil {
		return ""
	}
	if p.Type != PortalPlatform {
		return ""
	}
	if p.Portal == nil {
		return ""
	}
	return p.Portal.RamAccountId
}

// CloudAccountID get cloudAccountID from context
func CloudAccountID(ctx context.Context) string {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return ""
	}
	if p == nil {
		return ""
	}
	if p.Type != PortalPlatform {
		return ""
	}
	if p.Portal == nil {
		return ""
	}
	return p.Portal.CloudAccountID
}

// TenantID get tenantID from context
func TenantID(ctx context.Context) string {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return ""
	}
	if p == nil {
		return ""
	}
	if p.Type != PortalPlatform {
		return ""
	}
	if p.Portal == nil {
		return ""
	}
	return p.Portal.TenantID
}

// ParamData get ParamData from context
func ParamData(ctx context.Context) map[string]string {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return nil
	}
	if p == nil {
		return nil
	}
	if p.Type != PortalPlatform {
		return nil
	}
	if p.Portal == nil {
		return nil
	}
	return p.Portal.ParamMap
}

// GetParamData get val from ctx param data
func GetParamData(ctx context.Context, key string) string {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return ""
	}
	if p == nil {
		return ""
	}
	if p.Type != PortalPlatform {
		return ""
	}
	if p.Portal == nil {
		return ""
	}
	if p.Portal.ParamMap == nil {
		return ""
	}
	return p.Portal.ParamMap[key]
}

// SetParamData only for PortalPlatform ctx set param data
func SetParamData(ctx context.Context, key, val string) error {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return errors.New("nil header")
	}
	if p == nil {
		return errors.New("nil header")
	}
	if p.Type != PortalPlatform {
		return errors.New("incorrect typ ctx")
	}
	if p.Portal == nil {
		return errors.New("nil ctx")
	}
	if p.Portal.ParamMap == nil {
		p.Portal.ParamMap = map[string]string{}
	}
	p.Portal.ParamMap[key] = val
	return nil
}

// AdminUserID get adminUserID from context
func AdminUserID(ctx context.Context) string {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return ""
	}
	if p == nil {
		return ""
	}
	if p.Type != AdminPlatform {
		return ""
	}
	if p.Admin == nil {
		return ""
	}
	return p.Admin.AdminUserID
}

// SuperAdminUserId get superAdminUserId from context
func SuperAdminUserId(ctx context.Context) string {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return ""
	}
	if p == nil {
		return ""
	}
	if p.Type != AdminPlatform {
		return ""
	}
	if p.Admin == nil {
		return ""
	}
	return p.Admin.SuperAdminUserId
}

// Username 获取 userName
// 注：目前只能获取到admin的 userName，portal的 userName 返回空字符串
func Username(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok || p == nil {
		return ""
	}

	switch p.Type {
	case AdminPlatform:
		return p.Admin.UserName
	default:
		return ""
	}
}

func Token(ctx context.Context) string {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return ""
	}
	if p == nil {
		return ""
	}
	if p.Type != PortalPlatform {
		return ""
	}
	if p.Portal == nil {
		return ""
	}
	return p.Portal.Token
}

func ShadowRamAccountId(ctx context.Context) string {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return ""
	}
	if p == nil {
		return ""
	}
	if p.Type != PortalPlatform {
		return ""
	}
	if p.Portal == nil {
		return ""
	}
	return p.Portal.ShadowRamAccountId
}

func IsUpgrade(ctx context.Context) bool {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return false
	}
	if p == nil {
		return false
	}
	if p.Type != PortalPlatform {
		return false
	}
	if p.Portal == nil {
		return false
	}
	return p.Portal.IsUpgrade
}

func UpgradeLogs(ctx context.Context) []int64 {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return nil
	}
	if p == nil {
		return nil
	}
	if p.Type != PortalPlatform {
		return nil
	}
	if p.Portal == nil {
		return nil
	}
	return p.Portal.UpgradeLogs
}

func IsAdmin(ctx context.Context) bool {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return false
	}
	if p == nil {
		return false
	}
	return p.Type == AdminPlatform
}

func IsPortal(ctx context.Context) bool {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return false
	}
	if p == nil {
		return false
	}
	return p.Type == PortalPlatform
}

func IsNoauth(ctx context.Context) bool {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return false
	}
	if p == nil {
		return false
	}
	return p.Type == NoauthPlatform
}

func Upgrade(ctx context.Context) {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return
	}

	if p.Type != PortalPlatform {
		return
	}

	if p.Portal == nil {
		return
	}

	p.Portal.IsUpgrade = true
	p.Portal.ShadowRamAccountId = p.Portal.RamAccountId
	p.Portal.RamAccountId = ""
	p.Portal.UpgradeLogs = append(p.Portal.UpgradeLogs, time.Now().UnixMilli())
}

func RealRamAccountId(ctx context.Context) string {
	p, ok := ctx.Value(platformKey{}).(*CloudPlatform)
	if !ok {
		return ""
	}

	if p.Type != PortalPlatform {
		return ""
	}

	if p.Portal == nil {
		return ""
	}

	if p.Portal.IsUpgrade {
		return p.Portal.ShadowRamAccountId
	}

	return p.Portal.RamAccountId
}

func MockPortalHeader(cloudAccountID, ramAccountId, tenantID, requestID string) string {
	marshal, _ := json.Marshal(Portal{
		CloudAccountID: cloudAccountID,
		RamAccountId:   ramAccountId,
		TenantID:       tenantID,
		RequestID:      requestID,
	})
	return cryptor.Base64StdEncode(string(marshal))
}

func MockAdminHeader(adminUserID string, requestID string) string {
	marshal, _ := json.Marshal(Admin{AdminUserID: adminUserID, RequestID: requestID})
	return cryptor.Base64StdEncode(string(marshal))
}

func MockContext(ctx context.Context, data *CloudPlatform) context.Context {
	return context.WithValue(ctx, platformKey{}, data)
}

// RequestId 从ctx中获取requestId
// 注：如果 gin.Engine.ContextWithFallback=false，请使用 c.Request.Context() (c 为 *gin.Context)
func RequestId(ctx context.Context) string {
	id, _ := ctx.Value(ReqId).(string)
	return id
}
