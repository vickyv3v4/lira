package types

type PodInfo struct {
	//K8sPod *v1.Pod
	Name           string
	Namespace      string
	PodNetworkType string
	PodIP          string // used for eip and mip
	SandboxExited  bool
	PodENI         bool
	PodUID         string
}
