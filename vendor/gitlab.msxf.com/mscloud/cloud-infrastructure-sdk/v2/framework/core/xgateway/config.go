package xgateway

import "fmt"

type CloudServiceConfig struct {
	Token   string `json:"token" mapstructure:"token"`
	Host    string `json:"host" mapstructure:"host"`
	Gateway string `json:"gateway" mapstructure:"gateway"`
}

func (c *CloudServiceConfig) GetURI(path string) string {
	return fmt.Sprintf("http://%s%s", c.Gateway, path)
}