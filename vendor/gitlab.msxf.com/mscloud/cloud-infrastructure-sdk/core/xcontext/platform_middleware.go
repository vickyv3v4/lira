package xcontext

import (
	"context"
	"encoding/json"
	"time"

	"github.com/duke-git/lancet/cryptor"
	"github.com/gin-gonic/gin"
)

type (
	UrlPrefix   string
	platformKey struct{}
)

// PropagationKey 传播平台上下文的Key
const PropagationKey = "platform-ctx"

// propagationType 传播平台上下文的类型的Key
const PropagationType = "ctx-type"

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
	ctx = context.WithValue(ctx, "reqId", reqId)
	// clone with context
	c.Request = c.Request.WithContext(ctx)
	c.Next()
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
