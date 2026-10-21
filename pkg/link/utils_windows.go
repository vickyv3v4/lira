package link

import (
	"context"
	"net"

	"gitlab.msxf.com/mscloud/lira/windows/containernetworking/plugins/pkg/ns"
)

func GenericCleanup(ctx context.Context, netNs ns.NetNS) error {
	return nil
}

func GetLinkAddrByName(ifName string) (*net.IP, error) {

	return nil, nil
}

func VethNameForPod(name, namespace, ifName, prefix string) (string, error) {

	return "", nil
}
func IPVlanTmpNameForPod(name, namespace, prefix string) (string, error) {
	return "", nil
}
func DeleteVethRoute(hostVethName string, ip *net.IP) error {
	return nil
}
func DeleteVeth(hostVethName string) error {
	return nil
}
func EnsureHostNsConfig() error {
	return nil
}
func DeleteVethNeigh(hostVethName string, ip *net.IP) error {
	return nil
}
