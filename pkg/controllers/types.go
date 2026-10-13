package controllers

import (
	"encoding/json"
)

const (
	MSCloudCM        = "msconfig"
	SysNamespace     = "kube-system"
	InternalAddrType = "InternalIP"

	ErrorInsufficientENINUM = "error Node has binded enough ENIs"
)

type MSConfig struct {
	TenantID        string   `json:"tenantId"`
	CloudAccountID  string   `json:"cloudAccountId"`
	RequestId       string   `json:"requestId"`
	RegionCode      string   `json:"regionCode"`
	AzoneCode       string   `json:"azoneCode"`
	VPCID           string   `json:"vpcId"`
	SubnetID        string   `json:"subnetId"`
	SecurityGroups  []string `json:"securityGroups"`
	ResourceGroupID string   `json:"resourceGroup"`
	OwnerType       string   `json:"ownerType"`
	ServiceCidr     string   `json:"serviceCidr"`
	// NodeENINum弃用 ，从node的lables中获取
	NodeENINum   string `json:"nodeENINum"`
	MasterENINum string `json:"masterENINum"`
	ClusterID    string `json:"clusterId"`
}

func (m *MSConfig) GetConfig(data []byte) (*MSConfig, error) {
	var conf MSConfig
	if err := json.Unmarshal(data, &conf); err != nil {
		return nil, err
	}
	return &conf, nil
}
