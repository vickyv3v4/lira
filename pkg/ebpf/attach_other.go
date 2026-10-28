//go:build !linux
// +build !linux

package ebpf

import "net"

func AttachPod(podNSPath, podIfName, hostVethName string, podIP net.IP) error {
	return ErrUnsupported
}

func DetachPod(podNSPath, podIfName, hostVethName string) {}
