package link

import (
	"context"
	"crypto/rand"
	"net"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
)

type Veth struct {
	HostIfName string
	PeerName   string
	HwAddr     net.HardwareAddr
	MTU        int
}

func InstallVeth(ctx context.Context, cfg *Veth, netNS ns.NetNS, podMac, hostMac net.HardwareAddr) error {
	peer, err := netlink.LinkByName(cfg.PeerName)
	if err == nil {
		err = LinkDel(peer)
		if err != nil {
			return err
		}
	}
	if _, ok := err.(netlink.LinkNotFoundError); !ok {
		return err
	}
	v := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{
			Name:         cfg.HostIfName,
			Namespace:    netlink.NsFd(int(netNS.Fd())),
			HardwareAddr: hostMac,
		},
		PeerName:         cfg.PeerName,
		PeerHardwareAddr: podMac,
	}
	err = LinkAdd(v)
	if err != nil {
		return err
	}

	return nil
}
func GenerateMac() (net.HardwareAddr, error) {
	mac := make([]byte, 6)
	_, err := rand.Read(mac)
	if err != nil {
		return nil, err
	}
	//表示单播和本地唯一
	mac[0] = (mac[0] | 2) & 0xfe
	return net.HardwareAddr(mac), nil
}
