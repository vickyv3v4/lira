package utils

var GConfig *MsxfConfig

type LiraIPMode string

const (
	MultiENIIP   LiraIPMode = "multiEniIP"
	ExclusiveENI LiraIPMode = "exclusiveEni"
)

type MsxfConfig struct {
	EnableLeaderElection bool
	ProbeAddr            string
	OpenAPIUrl           string
	MetricsAddr          string
	PodENICurrent        int
	RunMode              LiraIPMode
}

func NewGConfig() {
	GConfig = &MsxfConfig{}
}
