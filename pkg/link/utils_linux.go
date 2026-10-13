package link

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net"
	"os"

	"github.com/containernetworking/plugins/pkg/ip"
	"github.com/containernetworking/plugins/pkg/ns"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"

	"github.com/vishvananda/netlink"

	k8sErr "k8s.io/apimachinery/pkg/util/errors"
)

var ipv4NetConfig = [][]string{
	{"/proc/sys/net/ipv4/conf/%s/forwarding", "1"},
	{"/proc/sys/net/ipv4/conf/%s/rp_filter", "0"},
}

func EnsureLinkName(link netlink.Link, name string) (bool, error) {
	if link.Attrs().Name == name {
		return false, nil
	}
	return true, LinkSetName(link, name)
}

// EnsureAddr ensure only one IP for each family is present on link
func EnsureAddr(link netlink.Link, expect *netlink.Addr) (bool, error) {
	var changed bool

	addrList, err := netlink.AddrList(link, NetlinkFamily(expect.IP))
	if err != nil {
		return false, fmt.Errorf("error list address from if %s, %w", link.Attrs().Name, err)
	}

	found := false
	for _, addr := range addrList {
		if !addr.IP.IsGlobalUnicast() {
			continue
		}

		if (addr.IPNet.String() == expect.IPNet.String()) && (addr.Scope == expect.Scope) {
			found = true
			continue
		}

		err := AddrDel(link, &addr)
		if err != nil {
			return false, err
		}
		changed = true
	}
	if found {
		return changed, nil
	}
	return true, AddrReplace(link, expect)
}

// EnsureLinkUp set link up,return changed and err
func EnsureLinkUp(link netlink.Link) (bool, error) {
	if link.Attrs().Flags&net.FlagUp != 0 {
		return false, nil
	}
	return true, LinkSetUp(link)
}

// EnsureRoute will call ip route replace if route is not found
func EnsureRoute(expected *netlink.Route) (bool, error) {
	routes, err := FoundRoutes(expected)
	if err != nil {
		return false, fmt.Errorf("error list expected: %v", err)
	}
	if len(routes) > 0 {
		return false, nil
	}

	return true, RouteReplace(expected)
}

func EnsureNeigh(neigh *netlink.Neigh) (bool, error) {
	var neighs []netlink.Neigh
	var err error
	if IsIPv6(neigh.IP) {
		neighs, err = netlink.NeighList(neigh.LinkIndex, netlink.FAMILY_V6)
	} else {
		neighs, err = netlink.NeighList(neigh.LinkIndex, netlink.FAMILY_V4)
	}
	if err != nil {
		return false, err
	}
	found := false
	for _, n := range neighs {
		if n.IP.Equal(neigh.IP) && n.HardwareAddr.String() == neigh.HardwareAddr.String() {
			found = true
			break
		}
	}
	if !found {
		return true, NeighSet(neigh)
	}
	return false, err
}

func DeleteNeigh(neigh *netlink.Neigh) error {
	var neighs []netlink.Neigh

	log := llog.Logger
	var err error
	if IsIPv6(neigh.IP) {
		neighs, err = netlink.NeighList(neigh.LinkIndex, netlink.FAMILY_V6)
	} else {
		neighs, err = netlink.NeighList(neigh.LinkIndex, netlink.FAMILY_V4)
	}
	if err != nil {
		return err
	}
	for _, n := range neighs {
		err := NeighDel(&n)
		if err != nil {
			log.Error(err, "delete veth neigh.")
		}
	}
	return nil
}

func IsIPv6(ip net.IP) bool {
	return ip.To4() == nil
}

func GetLinkAddrByName(ifName string) (*net.IP, error) {
	link, err := netlink.LinkByName(ifName)
	if err != nil {
		return nil, err
	}
	addrs, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return nil, err
	}
	return &addrs[0].IP, nil
}

func GenericCleanup(ctx context.Context, netNs ns.NetNS) error {
	var errList []error

	hostNetNS, err := ns.GetCurrentNS()
	if err != nil {
		return fmt.Errorf("err get host net ns, %w", err)
	}
	err = netNs.Do(func(netNs ns.NetNS) error {
		linkList, err := netlink.LinkList()
		if err != nil {
			return fmt.Errorf("error get link list from netlink, %w", err)
		}
		for _, l := range linkList {
			if l.Attrs().Name == "lo" {
				continue
			}
			_ = LinkSetDown(l)
			switch l.(type) {
			case *netlink.Veth:
				errList = append(errList, LinkDel(l))
			case *netlink.Device:
				name, err := ip.RandomVethName()
				if err != nil {
					errList = append(errList, err)
					continue
				}
				errList = append(errList, LinkSetName(l, name))
				errList = append(errList, LinkSetNsFd(l, hostNetNS))
			default:
				continue
			}
		}
		return nil
	})
	if err != nil {
		if _, ok := err.(ns.NSPathNotExistErr); !ok {
			errList = append(errList, err)
		}
	}
	return k8sErr.NewAggregate(errList)
}

// RandomVethName returns string "veth" with random prefix (hashed from entropy)
func RandomVethName() (string, error) {
	entropy := make([]byte, 4)
	_, err := rand.Read(entropy)
	if err != nil {
		return "", fmt.Errorf("failed to generate random veth name: %v", err)
	}

	// NetworkManager (recent versions) will ignore veth devices that start with "veth"
	return fmt.Sprintf("veth%x", entropy), nil
}

// VethNameForPod return host-side veth name for pod
// max veth length is 15
func VethNameForPod(name, namespace, ifName, prefix string) (string, error) {
	// A SHA1 is always 20 bytes long, and so is sufficient for generating the
	// veth name and mac addr.
	h := sha1.New()

	_, err := h.Write([]byte(namespace + "." + name + ifName))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%s", prefix, hex.EncodeToString(h.Sum(nil))[:11]), nil
}
func DeleteVeth(hostVethName string) error {
	log := llog.Logger

	link, err := netlink.LinkByName(hostVethName)
	if err != nil {
		log.Error(err, "get veth host link.")
		return err
	}
	err = LinkDel(link)
	if err != nil {
		return err
	}
	return nil
}
func DeleteVethNeigh(hostVethName string, ip *net.IP) error {
	log := llog.Logger

	var neighs []*netlink.Neigh
	hostVethLink, err := netlink.LinkByName(hostVethName)
	if err != nil {
		log.Error(err, "get veth host link.")
		return err
	}

	neighs = append(neighs, &netlink.Neigh{
		LinkIndex:    hostVethLink.Attrs().Index,
		IP:           *ip,
		HardwareAddr: hostVethLink.Attrs().HardwareAddr,
		State:        netlink.NUD_PERMANENT,
	})
	for _, n := range neighs {
		err := DeleteNeigh(n)
		if err != nil {
			log.Error(err, "DeleteVethNeigh failed.")
		}
	}
	return nil
}
func DeleteVethRoute(hostVethName string, ip *net.IP) error {
	log := llog.Logger

	hostVethLink, err := netlink.LinkByName(hostVethName)
	if err != nil {
		log.Error(err, "get veth host link.")
		return err
	}
	route := netlink.Route{
		LinkIndex: hostVethLink.Attrs().Index,
		Scope:     netlink.SCOPE_LINK,
		Dst: &net.IPNet{
			IP:   *ip,
			Mask: net.CIDRMask(32, 32),
		},
	}
	err = RouteDel(&route)
	if err != nil {
		log.Error(err, "delete veth route.")
		return err
	}
	return nil
}

func EnsureHostNsConfig() error {
	for _, key := range []string{"default", "all"} {
		for _, cfg := range ipv4NetConfig {
			err := EnsureConf(fmt.Sprintf(cfg[0], key), cfg[1])
			if err != nil {
				return err
			}
		}
	}

	return EnsureNetConfSet()
}

// EnsureNetConfSet will set net config to all link
func EnsureNetConfSet() error {
	links, err := netlink.LinkList()
	if err != nil {
		return err
	}

	for _, link := range links {
		for _, cfg := range ipv4NetConfig {
			innerErr := EnsureConf(fmt.Sprintf(cfg[0], link.Attrs().Name), cfg[1])
			if innerErr != nil {
				if !os.IsNotExist(innerErr) {
					err = fmt.Errorf("%v, %w", err, innerErr)
				}
			}
		}
	}
	return err
}

func EnsureConf(fPath string, cfg string) error {
	if content, err := os.ReadFile(fPath); err == nil {
		if bytes.Equal(bytes.TrimSpace(content), []byte(cfg)) {
			return nil
		}
	}
	return os.WriteFile(fPath, []byte(cfg), 0644)
}
