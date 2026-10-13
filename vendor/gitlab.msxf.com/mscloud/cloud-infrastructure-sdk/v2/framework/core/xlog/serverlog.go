package xlog

import (
	"fmt"
	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/framework/core/xenv"
)

type LogOption struct {
	LogPath string `json:"log_path" mapstructure:"log_path"`
	LogName string `json:"log_name" mapstructure:"log_name"`
}

func InitServerLog(option *LogOption) {
	if xenv.Local() {
		SetConfigurationHook(&DevDefaultConfigurationHook{})
		return
	}
	infoFile := fmt.Sprintf("app.%s.log", option.LogName)
	errFile := fmt.Sprintf("error.%s.log", option.LogName)
	SetConfigurationHook(NewDefaultConfigurationHook(option.LogPath, infoFile, errFile))

}
