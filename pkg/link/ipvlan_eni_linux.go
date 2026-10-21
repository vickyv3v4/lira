package link

import (
	"context"
	"fmt"
	"net"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"
)

type MultiIPvlanENI struct{}

func NewMultiIPvlanENIDriver() *MultiIPvlanENI {
	return &MultiIPvlanENI{}
}
func (r *MultiIPvlanENI) Install(ctx context.Context, cfg *LinkConfig, netNS ns.NetNS) error {
	log := llog.NewLoggerForCNI()

	log.Info("lira: MultiIPvlanENI Install begin")
	parentLink, err := netlink.LinkByIndex(cfg.ENIIndex)
	if err != nil {
		return fmt.Errorf("error get eni by index %d, %w", cfg.ENIIndex, err)
	}

	hostNetNS, err := ns.GetCurrentNS()
	if err != nil {
		return fmt.Errorf("err get host net ns, %w", err)
	}

	ipvlan := &netlink.IPVlan{
		LinkAttrs: netlink.LinkAttrs{
			Name:        cfg.TmpPodIfName,
			Namespace:   netlink.NsFd(int(netNS.Fd())),
			ParentIndex: parentLink.Attrs().Index,
		},
		Mode: netlink.IPVLAN_MODE_L2,
	}
	err = LinkAdd(ipvlan)
	if err != nil {
		log.Errorf("add ipvlan failed. %s", cfg.PodIPNet.IP)
		return err
	}
	defer hostNetNS.Close()
	defer func() {
		if err != nil {
			err = netNS.Do(func(netNS ns.NetNS) error {
				nicLink, err := netlink.LinkByName(cfg.TmpPodIfName)

				if _, ok := err.(netlink.LinkNotFoundError); ok {
					err = nil
				}
				if err == nil {
					LinkSetDown(nicLink)
					return LinkDel(nicLink)
				}
				return err
			})
		}
	}()

	var podMac net.HardwareAddr
	err = netNS.Do(func(netNS ns.NetNS) error {
		contLink, err := netlink.LinkByName(cfg.TmpPodIfName)
		if err != nil {
			return fmt.Errorf("error find link %s, %w", ipvlan.Attrs().Name, err)
		}

		err = LinkSetDown(contLink)
		if err != nil {
			return err
		}

		_, err = EnsureLinkName(contLink, cfg.PodIfName)
		if err != nil {
			return fmt.Errorf("ensure link name %s, err %s", cfg.TmpPodIfName, err)
		}

		err = LinkSetUp(contLink)
		if err != nil {
			return err
		}
		contCfg := generateContCfgForIPVlanENIMultiIP(cfg, contLink)
		err = ApplyConf(ctx, contLink, contCfg)
		if err != nil {
			return err
		}
		// generate mac by self , do not wait veth mac ready
		hostMac, err := GenerateMac()
		if err != nil {
			return fmt.Errorf("error generate mac addr, %w", err)
		}
		podMac, err = GenerateMac()
		if err != nil {
			return fmt.Errorf("error generate mac addr, %w", err)
		}
		err = InstallVeth(ctx, &Veth{
			HostIfName: cfg.HostVethName,
			PeerName:   DefaultVethForENI,
		}, hostNetNS, podMac, hostMac)
		if err != nil {
			return fmt.Errorf("error set up veth link, %w", err)
		}
		log.Infof("lira: InstallVeth, HostIfName %s, PeerName %s", cfg.HostVethName, DefaultVethForENI)
		err = hostNetNS.Do(func(netNS ns.NetNS) error {
			var innerErr error
			hostPeer, innerErr := netlink.LinkByName(cfg.HostVethName)
			if innerErr != nil {
				return innerErr
			}
			innerErr = LinkSetUp(hostPeer)
			if innerErr != nil {
				return innerErr
			}
			return nil
		})
		if err != nil {
			return err
		}
		veth1, err := netlink.LinkByName(DefaultVethForENI)
		if err != nil {
			return err
		}
		log.Infof("lira: generateVeth1Cfg, HostIfName %s, mac %s, PeerName %s, mac %s", cfg.HostVethName, hostMac.String(), DefaultVethForENI, veth1.Attrs().HardwareAddr)
		veth1Cfg := generateIPVlanVeth1Cfg(cfg, veth1, hostMac)
		return ApplyConf(ctx, veth1, veth1Cfg)
	})
	if err != nil {
		return fmt.Errorf("error set container link/address/route, %w", err)
	}
	hostPeer, err := netlink.LinkByName(cfg.HostVethName)
	if err != nil {
		return fmt.Errorf("error get host veth %s, %w", cfg.HostVethName, err)
	}
	hostPeerCfg := generateIPVlanHostSlaveCfg(cfg, hostPeer, podMac)
	err = ApplyConf(ctx, hostPeer, hostPeerCfg)
	if err != nil {
		return fmt.Errorf("error set hostpeer, %w", err)
	}
	return nil
}

func generateIPVlanHostSlaveCfg(cfg *LinkConfig, link netlink.Link, podVethMac net.HardwareAddr) *NicConf {
	var addrs []*netlink.Addr
	var routes []*netlink.Route
	var neighs []*netlink.Neigh

	addrs = append(addrs, &netlink.Addr{
		IPNet: LocalIPNet,
	})

	// add route to container
	routes = append(routes, &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Scope:     netlink.SCOPE_LINK,
		Dst: &net.IPNet{
			IP:   cfg.PodIPNet.IP,
			Mask: net.CIDRMask(32, 32),
		},
	})

	neighs = append(neighs, &netlink.Neigh{
		LinkIndex:    link.Attrs().Index,
		IP:           cfg.PodIPNet.IP,
		HardwareAddr: podVethMac,
		State:        netlink.NUD_PERMANENT,
	})
	ncfg := &NicConf{
		IfName: cfg.HostVethName,
		Addrs:  addrs,
		Routes: routes,
		Neighs: neighs,
	}
	return ncfg
}

func generateContCfgForIPVlanENIMultiIP(cfg *LinkConfig, link netlink.Link) *NicConf {
	var addrs []*netlink.Addr
	var routes []*netlink.Route
	addrs = append(addrs, &netlink.Addr{
		IPNet: cfg.PodIPNet,
	})

	// add default route
	routes = append(routes, &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Scope:     netlink.SCOPE_UNIVERSE,
		Dst:       defaultRoute,
		Gw:        net.ParseIP(cfg.GatewayIP),
		Flags:     int(netlink.FLAG_ONLINK),
	})
	routes = append(routes, &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Scope:     netlink.SCOPE_UNIVERSE,
		Flags:     int(netlink.FLAG_ONLINK),
		Dst:       &cfg.Route.Dst,
	})

	ncfg := &NicConf{
		IfName: cfg.TmpPodIfName,
		Addrs:  addrs,
		Routes: routes,
	}
	return ncfg
}

func generateIPVlanVeth1Cfg(cfg *LinkConfig, link netlink.Link, hostVethMac net.HardwareAddr) *NicConf {
	//	var addrs []*netlink.Addr
	var routes []*netlink.Route
	var neighs []*netlink.Neigh
	/*
		addrs = append(addrs, &netlink.Addr{
			IPNet: cfg.PodIPNet,
		})
	*/
	// xxxx/xxx via 169.254.1.1 dev veth1 onlink
	routes = append(routes, &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Scope:     netlink.SCOPE_LINK,
		Dst:       LocalIPNet,
	})

	if cfg.ServiceRoute != nil {
		routes = append(routes, &netlink.Route{
			LinkIndex: link.Attrs().Index,
			Dst:       &cfg.ServiceRoute.Dst,
			Gw:        LocalIP,
			Flags:     int(netlink.FLAG_ONLINK),
		})
	}
	neighs = append(neighs, &netlink.Neigh{
		LinkIndex:    link.Attrs().Index,
		IP:           LocalIPNet.IP,
		HardwareAddr: hostVethMac,
		State:        netlink.NUD_PERMANENT,
	})
	ncfg := &NicConf{
		IfName: DefaultVethForENI,
		Routes: routes,
		Neighs: neighs,
	}
	return ncfg
}
