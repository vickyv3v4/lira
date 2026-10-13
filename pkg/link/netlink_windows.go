package link

import (
	"net"

	"github.com/vishvananda/netlink"

	"gitlab.msxf.com/mscloud/lira/windows/containernetworking/plugins/pkg/ns"
)

func LinkSetName(link netlink.Link, name string) error {

	return nil
}

func LinkAdd(link netlink.Link) error {

	return nil
}
func LinkSetUp(link netlink.Link) error {

	return nil
}

func LinkSetDown(link netlink.Link) error {

	return nil
}

func LinkDel(link netlink.Link) error {

	return nil
}

func LinkSetNsFd(link netlink.Link, netNS ns.NetNS) error {

	return nil
}

func RouteReplace(route *netlink.Route) error {

	return nil
}

func AddrDel(link netlink.Link, addr *netlink.Addr) error {

	return nil
}

func AddrReplace(link netlink.Link, addr *netlink.Addr) error {

	return nil
}

func RouteDel(route *netlink.Route) error {

	return nil
}
func NetlinkFamily(ip net.IP) int {
	return 0
}

// FoundRoutes look up routes
func FoundRoutes(expected *netlink.Route) ([]netlink.Route, error) {
	return nil, nil
}

func NeighSet(neigh *netlink.Neigh) error {

	return nil
}
