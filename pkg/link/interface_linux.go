package link

import (
	"fmt"

	"github.com/pkg/errors"
	"github.com/vishvananda/netlink"
)

// GetDeviceNumber get interface device number by mac address
func GetDeviceNumber(mac string) (int, error) {
	linkList, err := netlink.LinkList()
	if err != nil {
		return 0, fmt.Errorf("error get link list from netlink, %w", err)
	}

	for _, link := range linkList {
		// ignore virtual nic type. eg. ipvlan veth bridge
		if _, ok := link.(*netlink.Device); !ok {
			continue
		}
		if link.Attrs().HardwareAddr.String() == mac {
			return link.Attrs().Index, nil
		}
	}
	return 0, errors.Wrapf(ErrNotFound, "can't found dev by mac %s", mac)
}
