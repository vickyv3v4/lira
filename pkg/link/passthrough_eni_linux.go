package link

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"time"

	"github.com/containernetworking/plugins/pkg/ip"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"
)

type ExclusiveENI struct{}

func NewExclusiveENIDriver() *ExclusiveENI {
	return &ExclusiveENI{}
}

func (r *ExclusiveENI) Install(ctx context.Context, cfg *LinkConfig, netNS ns.NetNS) error {
	log := llog.Logger

	log.Info("lira: ExclusiveENI Install begin")
	nicLink, err := netlink.LinkByIndex(cfg.ENIIndex)
	if err != nil {
		return fmt.Errorf("error get eni by index %d, %w", cfg.ENIIndex, err)
	}
	hostNetNS, err := ns.GetCurrentNS()
	if err != nil {
		return fmt.Errorf("err get host net ns, %w", err)
	}
	defer hostNetNS.Close()
	err = LinkSetNsFd(nicLink, netNS)
	if err != nil {
		return fmt.Errorf("error set nic %s to container, %w", nicLink.Attrs().Name, err)
	}

	defer func() {
		if err != nil {
			err = netNS.Do(func(netNS ns.NetNS) error {
				nicLink, err = netlink.LinkByName(cfg.PodIfName)
				if err == nil {
					nicName, err1 := ip.RandomVethName()
					if err1 != nil {
						return err1
					}
					err = LinkSetName(nicLink, nicName)
					if err != nil {
						return err
					}
				}

				if _, ok := err.(netlink.LinkNotFoundError); ok {
					err = nil
					nicLink, err = netlink.LinkByName(nicLink.Attrs().Name)
				}
				if err == nil {
					err = LinkSetDown(nicLink)
					return LinkSetNsFd(nicLink, hostNetNS)
				}
				return err
			})
		}
	}()
	var podMac net.HardwareAddr
	err = netNS.Do(func(netNS ns.NetNS) error {
		contLink, err := netlink.LinkByName(nicLink.Attrs().Name)
		if err != nil {
			return fmt.Errorf("error find link %s, %w", nicLink.Attrs().Name, err)
		}
		contCfg := generateContCfgForExclusiveENI(cfg, contLink)
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
		veth1Cfg := generateVeth1Cfg(cfg, veth1, hostMac)
		return ApplyConf(ctx, veth1, veth1Cfg)
	})
	if err != nil {
		return fmt.Errorf("error set container link/address/route, %w", err)
	}
	hostPeer, err := netlink.LinkByName(cfg.HostVethName)
	if err != nil {
		return fmt.Errorf("error get host veth %s, %w", cfg.HostVethName, err)
	}
	hostPeerCfg := generateHostSlaveCfg(cfg, hostPeer, podMac)
	err = ApplyConf(ctx, hostPeer, hostPeerCfg)
	if err != nil {
		return fmt.Errorf("error set hostpeer, %w", err)
	}
	return nil
}
func (r *ExclusiveENI) WaitForStableMac(ifName string, timeout time.Duration, count int) (macAddr net.HardwareAddr, err error) {
	log := llog.Logger
	linkCh := make(chan netlink.LinkUpdate)
	linkDone := make(chan struct{})
	defer close(linkDone)

	err = netlink.LinkSubscribe(linkCh, linkDone)
	if err != nil {
		return nil, err
	}
	var lastMac []byte
	stableCount := 0
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case update := <-linkCh:
			log.Infof("current interface %s, mac %s", update.Attrs().Name, update.Attrs().HardwareAddr.String())
			if update.Attrs().Name != ifName {
				continue
			}
			currentMac := update.Attrs().HardwareAddr
			log.Infof("current %s mac is %s", ifName, currentMac.String())

			if !bytes.Equal(currentMac, lastMac) {
				lastMac = currentMac
				stableCount = 0
			} else {
				stableCount++
			}

			if stableCount >= count {
				return currentMac, nil
			}
		case <-timer.C:
			log.Infof("timeout waiting for stable MAC on %s", ifName)
			return nil, nil
		}
	}
}
func generateHostSlaveCfg(cfg *LinkConfig, link netlink.Link, podVethMac net.HardwareAddr) *NicConf {
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
func generateContCfgForExclusiveENI(cfg *LinkConfig, link netlink.Link) *NicConf {
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
		IfName: cfg.PodIfName,
		Addrs:  addrs,
		Routes: routes,
	}
	return ncfg
}

func generateVeth1Cfg(cfg *LinkConfig, link netlink.Link, hostVethMac net.HardwareAddr) *NicConf {
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
	if cfg.HostIP != "" {
		hostIPNet := &net.IPNet{
			IP:   net.ParseIP(cfg.HostIP),
			Mask: net.CIDRMask(32, 32),
		}
		routes = append(routes, &netlink.Route{
			LinkIndex: link.Attrs().Index,
			Dst:       hostIPNet,
			Gw:        LocalIP,
		})
	}
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
		//Addrs:  addrs,
	}
	return ncfg
}
