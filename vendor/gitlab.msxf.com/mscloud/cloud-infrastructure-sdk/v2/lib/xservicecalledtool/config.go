package xservicecalledtool

import "net/http"

type ServiceCalledConf struct {
	PreventReplay bool     `json:"prevent_replay" mapstructure:"prevent_replay"`
	AllowDelaySec int64    `json:"allow_delay_sec" mapstructure:"allow_delay_sec"`
	WhiteList     []string `json:"white_list" mapstructure:"white_list"`
}

type FallbackFuc interface {
	Pass(req *http.Request) bool
}

func NewServiceCalled(conf *ServiceCalledConf, fuc FallbackFuc) {
	Manager.CalledConf = conf
	Manager.FallbackF = fuc
}

type defaultFallbackFuc struct {
}

func (d *defaultFallbackFuc) Pass(req *http.Request) bool {
	return false
}

var DefaultFallbackFuc = new(defaultFallbackFuc)