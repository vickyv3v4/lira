package xcontext

// Platform.Type枚举值
const (
	AdminPlatform  = "admin"
	PortalPlatform = "portal"
	NoauthPlatform = "noauth"
)

type (
	// Platform 平台统一上下文
	CloudPlatform struct {
		Type   string
		Admin  *Admin
		Portal *Portal
		NoAuth *NoAuth
	}

	// Admin 平台后管侧的统一上下文
	Admin struct {
		RequestID string `json:"requestId"`

		AdminUserID string `json:"adminUserId"`

		SuperAdminUserId string `json:"superAdminUserId"`
		UserName         string `json:"userName"`

		ParamMap map[string]string `json:"paramMap"`
	}

	// Portal 平台用户侧的统一上下文
	Portal struct {
		RequestID string `json:"requestId"`

		CloudAccountID string `json:"cloudAccountId"`

		RamAccountId string `json:"ramAccountId"`
		// TenantID projectName和tenantId合并了,tenantId == projectName
		TenantID string `json:"tenantId"`

		ShadowRamAccountId string `json:"shadowRamAccountId"`

		IsUpgrade bool `json:"isUpgrade"`

		UpgradeLogs []int64 `json:"upgradeLogs"`

		Token string `json:"token"`

		ParamMap map[string]string `json:"paramMap"`
	}

	// NoAuth 平台用户侧的统一上下文
	NoAuth struct {
		RequestID string `json:"requestId"`
	}
)
