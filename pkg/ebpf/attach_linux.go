//go:build linux
// +build linux

package ebpf

import (
	"fmt"
	"net"
	"strings"

	"github.com/cilium/ebpf"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"
)

// tc filter 统一优先级（同优先级同 parent 为替换语义，保证幂等）
const liraFilterPriority = 1

// findENIIndexByMAC 按 MAC 在宿主机上查找物理网卡（ENI 主口）ifindex
func findENIIndexByMAC(mac string) (int, error) {
	want := strings.ToLower(mac)
	links, err := netlink.LinkList()
	if err != nil {
		return 0, fmt.Errorf("error list host links, %w", err)
	}
	for _, link := range links {
		// 只匹配物理设备（与 pkg/link.GetDeviceNumber 行为一致）
		if _, ok := link.(*netlink.Device); !ok {
			continue
		}
		if strings.ToLower(link.Attrs().HardwareAddr.String()) == want {
			return link.Attrs().Index, nil
		}
	}
	return 0, fmt.Errorf("eni device with mac %s not found", mac)
}

// ensureClsact 保证网卡上存在 clsact qdisc（不排队，仅提供 tc ingress/egress 执行点）
func ensureClsact(link netlink.Link) error {
	qdiscs, err := netlink.QdiscList(link)
	if err != nil {
		return fmt.Errorf("error list qdisc on %s, %w", link.Attrs().Name, err)
	}
	for _, q := range qdiscs {
		if _, ok := q.(*netlink.Clsact); ok {
			return nil
		}
	}
	attrs := netlink.QdiscAttrs{
		LinkIndex: link.Attrs().Index,
		Handle:    netlink.MakeHandle(0xffff, 0),
		Parent:    netlink.HANDLE_CLSACT,
	}
	return netlink.QdiscAdd(&netlink.Clsact{QdiscAttrs: attrs})
}

// attachTC 在设备指定方向挂 tc bpf filter（Replace 语义，幂等；程序已 pin，attach 不换 fd 对象）
func attachTC(prog *ebpf.Program, ifindex int, ingress bool, name string) error {
	if prog == nil {
		return fmt.Errorf("program %s not loaded", name)
	}
	link, err := netlink.LinkByIndex(ifindex)
	if err != nil {
		return fmt.Errorf("error get link by index %d, %w", ifindex, err)
	}
	if err := ensureClsact(link); err != nil {
		return err
	}
	parent := uint32(netlink.HANDLE_MIN_EGRESS)
	if ingress {
		parent = uint32(netlink.HANDLE_MIN_INGRESS)
	}
	filter := &netlink.BpfFilter{
		FilterAttrs: netlink.FilterAttrs{
			LinkIndex: ifindex,
			Parent:    parent,
			Protocol:  0x0003, // ETH_P_ALL
			Priority:  liraFilterPriority,
		},
		Fd:           prog.FD(),
		Name:         name,
		DirectAction: true,
	}
	return netlink.FilterReplace(filter)
}

// detachTC 摘除设备指定方向上本插件的 filter
func detachTC(ifindex int, ingress bool) error {
	parent := uint32(netlink.HANDLE_MIN_EGRESS)
	if ingress {
		parent = uint32(netlink.HANDLE_MIN_INGRESS)
	}
	filter := &netlink.BpfFilter{
		FilterAttrs: netlink.FilterAttrs{
			LinkIndex: ifindex,
			Parent:    parent,
			Protocol:  0x0003,
			Priority:  liraFilterPriority,
		},
	}
	err := netlink.FilterDel(filter)
	if err != nil && !strings.Contains(err.Error(), "no such file") {
		return err
	}
	return nil
}

// AttachPod attaches ④ 程序到 pod 内 ipvlan 口（pod netns）、③ 程序到宿主机 veth 口，
// 并写 slave_src 表（防伪造第一层白名单）。由 CNI ADD 在 Install 成功后调用（设计 §5.1）。
// pin 目录不存在时静默跳过 —— 网络策略功能未启用。
func AttachPod(podNSPath, podIfName, hostVethName string, podIP net.IP) error {
	progPodEgress, err := ebpf.LoadPinnedProgram(filepathProgs(ProgPodEgress), nil)
	if err != nil {
		llog.Logger.WithError(err).Info("ebpf: policy pins not found, skip attach (feature disabled)")
		return nil
	}
	progPodIngress, err := ebpf.LoadPinnedProgram(filepathProgs(ProgPodIngress), nil)
	if err != nil {
		return fmt.Errorf("error load pinned pod ingress prog, %w", err)
	}
	progVethEgress, err := ebpf.LoadPinnedProgram(filepathProgs(ProgVethEgress), nil)
	if err != nil {
		return fmt.Errorf("error load pinned veth egress prog, %w", err)
	}
	slaveSrc, err := ebpf.LoadPinnedMap(filepathMaps(MapSlaveSrc), nil)
	if err != nil {
		return fmt.Errorf("error load pinned slave_src map, %w", err)
	}

	podNS, err := ns.GetNS(podNSPath)
	if err != nil {
		return fmt.Errorf("error open pod netns %s, %w", podNSPath, err)
	}
	defer podNS.Close()

	// ④ pod 内 ipvlan 口双向
	err = podNS.Do(func(_ ns.NetNS) error {
		link, err := netlink.LinkByName(podIfName)
		if err != nil {
			return fmt.Errorf("error get pod link %s, %w", podIfName, err)
		}
		if err := attachTC(progPodIngress, link.Attrs().Index, true, ProgPodIngress); err != nil {
			return err
		}
		if err := attachTC(progPodEgress, link.Attrs().Index, false, ProgPodEgress); err != nil {
			return err
		}
		// ifindex 为 pod netns 内的索引，与数据面 skb->ifindex 同一命名空间
		return slaveSrc.Put(uint32(link.Attrs().Index), beIP(podIP))
	})
	if err != nil {
		return fmt.Errorf("error attach pod-ns tc programs, %w", err)
	}

	// ③ 宿主机 veth 出向
	hostVeth, err := netlink.LinkByName(hostVethName)
	if err != nil {
		return fmt.Errorf("error get host veth %s, %w", hostVethName, err)
	}
	if err := attachTC(progVethEgress, hostVeth.Attrs().Index, false, ProgVethEgress); err != nil {
		return fmt.Errorf("error attach veth egress, %w", err)
	}
	return nil
}

// DetachPod 在 CNI DEL 时清理：摘除 ④ 双向 filter、删除 slave_src 条目、摘除 ③。
// 尽力而为：任一步失败仅记录日志（filter 随 netns 销毁，条目由 ifindex 键控）。
func DetachPod(podNSPath, podIfName, hostVethName string) {
	progPodEgress, err := ebpf.LoadPinnedProgram(filepathProgs(ProgPodEgress), nil)
	if err != nil {
		return // 功能未启用
	}
	progPodIngress, err := ebpf.LoadPinnedProgram(filepathProgs(ProgPodIngress), nil)
	if err != nil {
		return
	}
	progVethEgress, err := ebpf.LoadPinnedProgram(filepathProgs(ProgVethEgress), nil)
	if err != nil {
		return
	}
	slaveSrc, err := ebpf.LoadPinnedMap(filepathMaps(MapSlaveSrc), nil)
	if err != nil {
		return
	}

	podNS, err := ns.GetNS(podNSPath)
	if err != nil {
		return // netns 已销毁，无需清理
	}
	defer podNS.Close()

	var podIfindex int
	_ = podNS.Do(func(_ ns.NetNS) error {
		link, err := netlink.LinkByName(podIfName)
		if err != nil {
			return nil // 容器接口已销毁，无需清理
		}
		podIfindex = link.Attrs().Index
		_ = detachTC(podIfindex, true)
		_ = detachTC(podIfindex, false)
		return nil
	})
	if podIfindex != 0 {
		if err := slaveSrc.Delete(uint32(podIfindex)); err != nil {
			llog.Logger.WithError(err).Warn("ebpf: delete slave_src entry failed")
		}
	}
	if hostVeth, err := netlink.LinkByName(hostVethName); err == nil {
		_ = detachTC(hostVeth.Attrs().Index, false)
	}
	_ = progPodEgress.Close()
	_ = progPodIngress.Close()
	_ = progVethEgress.Close()
	_ = slaveSrc.Close()
}

func filepathProgs(name string) string {
	return PinRoot + "/progs/" + name
}

func filepathMaps(name string) string {
	return PinRoot + "/maps/" + name
}
