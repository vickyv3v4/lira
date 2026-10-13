package link

import (
	"context"

	"github.com/vishvananda/netlink"
)

type NicConf struct {
	IfName string
	MTU    int

	Addrs  []*netlink.Addr
	Routes []*netlink.Route
	Neighs []*netlink.Neigh
}

func ApplyConf(ctx context.Context, link netlink.Link, conf *NicConf) error {
	var err error
	if conf.IfName != "" {
		changed, err := EnsureLinkName(link, conf.IfName)
		if err != nil {
			return err
		}
		if changed {
			link, err = netlink.LinkByIndex(link.Attrs().Index)
			if err != nil {
				return err
			}
		}
	}
	for _, addr := range conf.Addrs {
		_, err = EnsureAddr(link, addr)
		if err != nil {
			return err
		}
	}
	_, err = EnsureLinkUp(link)
	if err != nil {
		return err
	}

	for _, neigh := range conf.Neighs {
		_, err = EnsureNeigh(neigh)
		if err != nil {
			return err
		}
	}

	for _, route := range conf.Routes {
		_, err = EnsureRoute(route)
		if err != nil {
			return err
		}
	}
	return nil
}
