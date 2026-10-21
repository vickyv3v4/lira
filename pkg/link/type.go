package link

import (
	"errors"
	"net"

	cniTypes "github.com/containernetworking/cni/pkg/types"
)

var (
	ErrNotFound        = errors.New("not found")
	_, defaultRoute, _ = net.ParseCIDR("0.0.0.0/0")
	LocalIP            = net.IPv4(169, 254, 1, 1)
	LocalIPNet         = &net.IPNet{
		IP:   LocalIP,
		Mask: net.CIDRMask(32, 32),
	}
)

const (
	DefaultContainerIfName = "eth0"
	DefaultVethForENI      = "veth1"
	RetryTimes             = 10
)

type LinkConfig struct {
	HostVethName string
	PodIfName    string
	PodIPNet     *net.IPNet
	ENIIndex     int
	Route        *cniTypes.Route
	ServiceRoute *cniTypes.Route
	GatewayIP    string
	Mac          string
	HostIP       string
	TmpPodIfName string
}
