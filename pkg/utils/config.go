package utils

var GConfig *MsxfConfig

type MsxfConfig struct {
	EnableLeaderElection bool
	ProbeAddr            string
	OpenAPIUrl           string
	MetricsAddr          string
	PodENICurrent        int
}

func NewGConfig() {
	GConfig = &MsxfConfig{}
}
