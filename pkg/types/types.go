package types

import (
	"fmt"
	"net"
	"strings"

	"gitlab.msxf.com/mscloud/lira/pkg/rpc"

	cniTypes "github.com/containernetworking/cni/pkg/types"
)

const (
	DefaultSocketPath      = "/var/run/lira/lira.socket"
	AnnotationPrefix       = "mck.msxf.com/"
	AnnotationsPodUID      = AnnotationPrefix + "pod-uid"
	AnnotationsENINodeName = AnnotationPrefix + "nodename"
	AnnotationsENIECSID    = AnnotationPrefix + "ecsId"
	AnnotationsENINUMS     = AnnotationPrefix + "eniNums"
	FinalizerPodENI        = "mck.msxf.com/pod-eni"

	LabelRoleControl = "node-role.kubernetes.io/control-plane"
	LabelRoleMaster  = "node-role.kubernetes.io/master"
	LabelRoleWorker  = "node-role.kubernetes.io/worker"

	ENIResourceKey = "msxf.com/eni"
	ENI_COUNTS     = "eni_counts"
)

type IPNetSet struct {
	IPv4 *net.IPNet
	IPv6 *net.IPNet
}

func (i *IPNetSet) ToRPC() *rpc.IPSet {
	var ipv4, ipv6 string
	if i.IPv4 != nil {
		ipv4 = i.IPv4.String()
	}
	if i.IPv6 != nil {
		ipv6 = i.IPv6.String()
	}
	return &rpc.IPSet{
		IPv4: ipv4,
		IPv6: ipv6,
	}
}

func (i *IPNetSet) String() string {
	if i == nil {
		return ""
	}
	var result []string
	if i.IPv4 != nil {
		result = append(result, i.IPv4.String())
	}
	if i.IPv6 != nil {
		result = append(result, i.IPv6.String())
	}
	return strings.Join(result, "-")
}

func IPv6(ip net.IP) bool {
	return ip.To4() == nil
}
func (i *IPNetSet) SetIPNet(str string) *IPNetSet {
	ip, ipNet, err := net.ParseCIDR(str)
	if err != nil {
		return i
	}
	if ip == nil {
		return i
	}
	if IPv6(ip) {
		i.IPv6 = ipNet
		return i
	}
	i.IPv4 = ipNet
	return i
}

func ToIPNetSet(ip *rpc.IPSet) (*IPNetSet, error) {
	if ip == nil {
		return nil, fmt.Errorf("ip is nil")
	}
	ipNetSet := &IPNetSet{}
	var err error
	if ip.IPv4 != "" {
		_, ipNetSet.IPv4, err = net.ParseCIDR(ip.IPv4)
		if err != nil {
			return nil, err
		}
	}
	if ip.IPv6 != "" {
		_, ipNetSet.IPv6, err = net.ParseCIDR(ip.IPv6)
		if err != nil {
			return nil, err
		}
	}
	return ipNetSet, nil
}

type K8SArgs struct {
	cniTypes.CommonArgs
	K8S_POD_NAME               cniTypes.UnmarshallableString // nolint
	K8S_POD_NAMESPACE          cniTypes.UnmarshallableString // nolint
	K8S_POD_INFRA_CONTAINER_ID cniTypes.UnmarshallableString // nolint
}

// CNIConf is the cni network config
type CNIConf struct {
	cniTypes.NetConf
}
