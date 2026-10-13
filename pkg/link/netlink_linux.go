package link

import (
	"fmt"
	"net"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"
)

func LinkSetName(link netlink.Link, name string) error {
	cmd := fmt.Sprintf("ip link set %s name %s", link.Attrs().Name, name)
	llog.Logger.Info(cmd)
	err := netlink.LinkSetName(link, name)
	if err != nil {
		return fmt.Errorf("error %s, %w", cmd, err)
	}
	return nil
}

func LinkAdd(link netlink.Link) error {
	cmd := fmt.Sprintf("ip link add %s type %s", link.Attrs().Name, link.Type())
	llog.Logger.Info(cmd)
	err := netlink.LinkAdd(link)
	if err != nil {
		return fmt.Errorf("error %s, %w", cmd, err)
	}
	return nil
}

func LinkSetUp(link netlink.Link) error {
	cmd := fmt.Sprintf("ip link set %s up", link.Attrs().Name)
	llog.Logger.Info(cmd)
	err := netlink.LinkSetUp(link)
	if err != nil {
		return fmt.Errorf("error %s, %w", cmd, err)
	}
	return nil
}

func LinkSetDown(link netlink.Link) error {
	cmd := fmt.Sprintf("ip link set %s down", link.Attrs().Name)
	llog.Logger.Info(cmd)
	err := netlink.LinkSetDown(link)
	if err != nil {
		return fmt.Errorf("error %s, %w", cmd, err)
	}
	return nil
}

func LinkDel(link netlink.Link) error {
	cmd := fmt.Sprintf("ip link del %s", link.Attrs().Name)
	llog.Logger.Info(cmd)
	err := netlink.LinkDel(link)
	if err != nil {
		if _, ok := err.(netlink.LinkNotFoundError); ok {
			return nil
		}
		return fmt.Errorf("error %s, %w", cmd, err)
	}
	return nil
}

func LinkSetNsFd(link netlink.Link, netNS ns.NetNS) error {
	cmd := fmt.Sprintf("ip link set %s netns %s", link.Attrs().Name, netNS.Path())
	llog.Logger.Info(cmd)
	err := netlink.LinkSetNsFd(link, int(netNS.Fd()))
	if err != nil {
		return fmt.Errorf("error %s, %w", cmd, err)
	}
	return nil
}

func RouteReplace(route *netlink.Route) error {
	cmd := fmt.Sprintf("ip route replace %s", route.String())
	llog.Logger.Info(cmd)
	err := netlink.RouteReplace(route)
	if err != nil {
		return fmt.Errorf("error %s, %w", cmd, err)
	}
	return nil
}

func AddrDel(link netlink.Link, addr *netlink.Addr) error {
	cmd := fmt.Sprintf("ip addr del %s dev %s", addr.IPNet.String(), link.Attrs().Name)
	llog.Logger.Info(cmd)
	err := netlink.AddrDel(link, addr)
	if err != nil {
		return fmt.Errorf("error %s, %w", cmd, err)
	}
	return nil
}

func AddrReplace(link netlink.Link, addr *netlink.Addr) error {
	cmd := fmt.Sprintf("ip addr replace %s dev %s", addr.IPNet.String(), link.Attrs().Name)
	llog.Logger.Info(cmd)
	err := netlink.AddrReplace(link, addr)
	if err != nil {
		return fmt.Errorf("error %s, %w", cmd, err)
	}
	return nil
}

func RouteDel(route *netlink.Route) error {
	cmd := fmt.Sprintf("ip route del %s", route.String())
	llog.Logger.Info(cmd)
	err := netlink.RouteDel(route)
	if err != nil {
		return fmt.Errorf("error %s, %w", cmd, err)
	}
	return nil
}
func NetlinkFamily(ip net.IP) int {
	if ip.To4() == nil {
		return netlink.FAMILY_V6
	}
	return netlink.FAMILY_V4
}

// FoundRoutes look up routes
func FoundRoutes(expected *netlink.Route) ([]netlink.Route, error) {
	family := NetlinkFamily(expected.Dst.IP)
	routeFilter := netlink.RT_FILTER_DST
	if expected.Dst == nil {
		return nil, fmt.Errorf("dst in route expect not nil")
	}
	find := *expected

	if find.Dst.String() == "::/0" || find.Dst.String() == "0.0.0.0/0" {
		find.Dst = nil
	}
	if find.LinkIndex > 0 {
		routeFilter = routeFilter | netlink.RT_FILTER_OIF
	}
	if find.Scope > 0 {
		routeFilter = routeFilter | netlink.RT_FILTER_SCOPE
	}
	if find.Gw != nil {
		routeFilter = routeFilter | netlink.RT_FILTER_GW
	}
	if find.Table > 0 {
		routeFilter = routeFilter | netlink.RT_FILTER_TABLE
	}
	return netlink.RouteListFiltered(family, &find, routeFilter)
}

func NeighSet(neigh *netlink.Neigh) error {
	cmd := fmt.Sprintf("ip neigh replace %s", neigh.String())
	llog.Logger.Info(cmd)
	err := netlink.NeighSet(neigh)
	if err != nil {
		return fmt.Errorf("error %s, %w", cmd, err)
	}
	return nil
}

func NeighDel(neigh *netlink.Neigh) error {
	cmd := fmt.Sprintf("ip neigh del %s", neigh.String())
	llog.Logger.Info(cmd)
	err := netlink.NeighDel(neigh)
	if err != nil {
		return fmt.Errorf("error %s, %w", cmd, err)
	}
	return nil
}
