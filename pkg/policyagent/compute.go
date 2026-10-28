//go:build linux
// +build linux

package policyagent

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"gitlab.msxf.com/mscloud/lira/pkg/ebpf"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// 全量计算（设计 §5.2）：任一 watch 事件触发后，遍历全部 NetworkPolicy 与 pod，
// 产出三张策略 map 的目标状态，再由 agent 全量替换写入。
// 复杂度 O(策略数 × 规则数 × 选中 pod 数)，共享 ENI 场景规模很小（设计 §3）。

// computeResult 一次计算的产物，直接映射到隔离表 / 身份对表 / CIDR 表
type computeResult struct {
	isolated []ebpf.ISOKey
	pairs    []ebpf.PairKey
	cidrs    map[ebpf.CIDRKey]ebpf.CIDRValue
}

// directionPeers 某身份在某方向允许的对端集合（尚未与对端方向求交集）
type directionPeers struct {
	// pod 对端：对端身份 → (proto, port) 集合
	podPeers map[uint32]map[portKey]struct{}
	// CIDR 对端：去重后的网段 → (proto, port) 集合
	cidrPeers map[string]*cidrPeer
}

type portKey struct {
	proto uint16
	port  uint16 // 0 = 任意端口
}

type cidrPeer struct {
	net   *net.IPNet
	ports map[portKey]struct{}
}

func newDirectionPeers() *directionPeers {
	return &directionPeers{
		podPeers:  make(map[uint32]map[portKey]struct{}),
		cidrPeers: make(map[string]*cidrPeer),
	}
}

func compute(pods []*podState, policies []*networkingv1.NetworkPolicy) (*computeResult, error) {
	egress := make(map[uint32]*directionPeers)
	ingress := make(map[uint32]*directionPeers)
	isoEgress := make(map[uint32]bool)
	isoIngress := make(map[uint32]bool)

	for _, np := range policies {
		selected := selectPods(pods, np)
		if len(selected) == 0 {
			continue
		}
		needIngress, needEgress := policyDirections(np)
		for _, pod := range selected {
			if needIngress {
				isoIngress[pod.identity] = true
				collectDirection(ingress, pod.identity, np.Spec.Ingress, np.Namespace, pods)
			}
			if needEgress {
				isoEgress[pod.identity] = true
				collectDirection(egress, pod.identity, egressAsIngress(np.Spec.Egress), np.Namespace, pods)
			}
		}
	}

	res := &computeResult{cidrs: make(map[ebpf.CIDRKey]ebpf.CIDRValue)}
	for id := range isoEgress {
		res.isolated = append(res.isolated, ebpf.ISOKey{Identity: id, Dir: ebpf.DirEgress})
	}
	for id := range isoIngress {
		res.isolated = append(res.isolated, ebpf.ISOKey{Identity: id, Dir: ebpf.DirIngress})
	}

	// 身份对交集（数据面单次查表的前提，设计 §5.2）：
	// (A→B, proto, port) 写入当且仅当
	//   (A 未 egress 隔离 或 (A,B,pp) ∈ A 的 egress allow 集) 且
	//   (B 未 ingress 隔离 或 (A,B,pp) ∈ B 的 ingress allow 集)
	pairSeen := make(map[ebpf.PairKey]bool)
	addPair := func(a, b uint32, pt portKey) {
		k := ebpf.PairKey{SrcIdentity: a, DstIdentity: b, Proto: pt.proto, Port: pt.port}
		if !pairSeen[k] {
			pairSeen[k] = true
			res.pairs = append(res.pairs, k)
		}
	}

	// egress 侧发起：A 一定隔离
	for a, peers := range egress {
		for b, ports := range peers.podPeers {
			if isoIngress[b] {
				bPorts := ingress[b].podPeers[a]
				for pt := range ports {
					if _, ok := bPorts[pt]; ok {
						addPair(a, b, pt)
					}
				}
			} else {
				for pt := range ports {
					addPair(a, b, pt)
				}
			}
		}
		for _, cp := range peers.cidrPeers {
			writeCIDR(res.cidrs, a, ebpf.DirEgress, cp)
		}
	}
	// ingress 侧补齐：源 A 未 egress 隔离时，由 B 的 ingress 规则单边决定
	for b, peers := range ingress {
		for a, ports := range peers.podPeers {
			if isoEgress[a] {
				continue // 已由 egress 侧循环写入
			}
			for pt := range ports {
				addPair(a, b, pt)
			}
		}
		for _, cp := range peers.cidrPeers {
			writeCIDR(res.cidrs, b, ebpf.DirIngress, cp)
		}
	}

	return res, nil
}

// policyDirections 依据 policyTypes 判定策略生效方向（K8s 语义）
func policyDirections(np *networkingv1.NetworkPolicy) (ingress, egress bool) {
	if len(np.Spec.PolicyTypes) > 0 {
		for _, t := range np.Spec.PolicyTypes {
			switch t {
			case networkingv1.PolicyTypeIngress:
				ingress = true
			case networkingv1.PolicyTypeEgress:
				egress = true
			}
		}
		return
	}
	return len(np.Spec.Ingress) > 0, len(np.Spec.Egress) > 0
}

// selectPods 计算 NP 选中（隔离）的 pod：同 ns 内匹配 podSelector
func selectPods(pods []*podState, np *networkingv1.NetworkPolicy) []*podState {
	sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
	if err != nil {
		return nil
	}
	var out []*podState
	for _, p := range pods {
		if p.hostNetwork || p.ip == "" {
			continue // hostNetwork pod 归节点身份，不参与（设计 §3/§8）
		}
		if p.namespace != np.Namespace || !sel.Matches(labels.Set(p.labels)) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// collectDirection 处理一个方向的全部规则；peers 为空的规则按 K8s 语义 = 允许所有对端，
// 用 0.0.0.0/0 全端口条目表达（LPM prefixlen=96 命中任意目的）。
// NetworkPolicyEgressRule 与 IngressRule 字段同构，统一按 IngressRule 处理。
func collectDirection(dst map[uint32]*directionPeers, selfID uint32,
	rules []networkingv1.NetworkPolicyIngressRule, policyNS string, pods []*podState) {
	if dst[selfID] == nil {
		dst[selfID] = newDirectionPeers()
	}
	d := dst[selfID]

	for i := range rules {
		rule := &rules[i]
		if len(rule.From) == 0 {
			addCIDRPeer(d, "0.0.0.0/0", &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}, nil)
			continue
		}
		for _, peer := range rule.From {
			switch {
			case peer.PodSelector != nil || peer.NamespaceSelector != nil:
				for _, p := range matchingPeerPods(pods, peer, policyNS) {
					addPodPeerPorts(d.podPeers, p.identity, rule.Ports)
				}
			case peer.IPBlock != nil:
				subnets, err := subtractCIDRs(peer.IPBlock.CIDR, peer.IPBlock.Except)
				if err != nil {
					// 单条 ipBlock 非法不应中断整体同步：跳过并依赖下次事件重算
					continue
				}
				for _, s := range subnets {
					addCIDRPeer(d, s.String(), s, rule.Ports)
				}
			}
		}
	}
}

// egressAsIngress 借用 IngressRule 结构处理 Egress 规则（Ports 字段同构，对端字段 To 对应 From）
func egressAsIngress(rules []networkingv1.NetworkPolicyEgressRule) []networkingv1.NetworkPolicyIngressRule {
	out := make([]networkingv1.NetworkPolicyIngressRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, networkingv1.NetworkPolicyIngressRule{Ports: r.Ports, From: r.To})
	}
	return out
}

func addPodPeerPorts(dst map[uint32]map[portKey]struct{}, peerID uint32, ports []networkingv1.NetworkPolicyPort) {
	if dst[peerID] == nil {
		dst[peerID] = make(map[portKey]struct{})
	}
	expandPorts(ports, dst[peerID])
}

func addCIDRPeer(d *directionPeers, key string, subnet *net.IPNet, ports []networkingv1.NetworkPolicyPort) {
	if _, ok := d.cidrPeers[key]; !ok {
		d.cidrPeers[key] = &cidrPeer{net: subnet, ports: make(map[portKey]struct{})}
	}
	expandPorts(ports, d.cidrPeers[key].ports)
}

// expandPorts 展开端口规则：未指定端口 = 任意端口；endPort 范围逐端口展开
func expandPorts(ports []networkingv1.NetworkPolicyPort, dst map[portKey]struct{}) {
	if len(ports) == 0 {
		dst[portKey{proto: ebpf.ProtoAny, port: 0}] = struct{}{}
		return
	}
	for i := range ports {
		p := &ports[i]
		proto := ebpf.ProtoTCP // NetworkPolicyPort.Protocol 缺省为 TCP
		if p.Protocol != nil {
			switch strings.ToUpper(string(*p.Protocol)) {
			case "UDP":
				proto = ebpf.ProtoUDP
			case "SCTP":
				proto = ebpf.ProtoSCTP
			}
		}
		if p.Port == nil {
			dst[portKey{proto: proto, port: 0}] = struct{}{}
			continue
		}
		start, end, ok := parsePortRange(p.Port.String(), p.EndPort)
		if !ok {
			continue
		}
		for pt := start; pt <= end; pt++ {
			dst[portKey{proto: proto, port: pt}] = struct{}{}
		}
	}
}

func parsePortRange(port string, endPort *int32) (uint16, uint16, bool) {
	start64, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return 0, 0, false
	}
	end := start64
	if endPort != nil {
		end = uint64(*endPort)
	}
	if end < start64 || end-start64 > 1024 {
		// 异常范围防呆：单条规则展开上限 1024 端口
		return 0, 0, false
	}
	return uint16(start64), uint16(end), true
}

// writeCIDR 把一个 CIDR 对端的端口集合写入 lpm 表目标状态。
// 同 key（identity/dir/proto/prefix）多条规则端口求并；任一规则为任意端口
// 或展开数超上限时，该条目降级为任意端口（方向为放行，与 NetworkPolicy 并集语义一致）。
func writeCIDR(dst map[ebpf.CIDRKey]ebpf.CIDRValue, identity, dir uint32, cp *cidrPeer) {
	ones, _ := cp.net.Mask.Size()
	ip4 := cp.net.IP.To4()
	if ip4 == nil {
		return
	}
	base := ebpf.CIDRKey{
		PrefixLen: 96 + uint32(ones),
		Identity:  identity,
		DirProto:  dir << 16,
	}
	copy(base.IP[:], ip4)

	// 按协议分组
	byProto := make(map[uint16][]uint16)
	anyPort := map[uint16]bool{}
	for pt := range cp.ports {
		if pt.proto == ebpf.ProtoAny || pt.port == 0 {
			anyPort[pt.proto] = true
			continue
		}
		byProto[pt.proto] = append(byProto[pt.proto], pt.port)
	}
	if anyPort[ebpf.ProtoAny] {
		// 任意协议任意端口：一条 proto=any 全端口条目即可
		k := base
		k.DirProto |= uint32(ebpf.ProtoAny)
		dst[k] = ebpf.CIDRValue{NPorts: 0}
		return
	}
	for proto, list := range byProto {
		k := base
		k.DirProto |= uint32(proto)
		if anyPort[proto] || len(list) > ebpf.MaxPortsPerCIDR {
			dst[k] = ebpf.CIDRValue{NPorts: 0}
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
		v := ebpf.CIDRValue{NPorts: uint16(len(list))}
		copy(v.Ports[:], list)
		if old, ok := dst[k]; ok {
			// 同 key 已有条目：合并端口集合，超限降级
			seen := make(map[uint16]bool)
			for _, p := range old.Ports[:old.NPorts] {
				seen[p] = true
			}
			for _, p := range list {
				if !seen[p] {
					v.Ports[v.NPorts] = p
					v.NPorts++
					seen[p] = true
				}
			}
			if int(v.NPorts) > ebpf.MaxPortsPerCIDR {
				v = ebpf.CIDRValue{NPorts: 0}
			}
		}
		dst[k] = v
	}
}

// matchingPeerPods 计算一个 podSelector/namespaceSelector 对端选中的 pod 集合（K8s 语义）：
//   - 仅 podSelector：限定在策略所在命名空间；
//   - namespaceSelector + podSelector：跨命名空间叠加过滤；
//   - 仅 namespaceSelector：匹配命名空间内全部 pod。
func matchingPeerPods(pods []*podState, peer networkingv1.NetworkPolicyPeer, policyNS string) []*podState {
	var nsSelector labels.Selector
	if peer.NamespaceSelector != nil {
		s, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
		if err != nil {
			return nil
		}
		nsSelector = s
	}
	var podSelector labels.Selector
	if peer.PodSelector != nil {
		s, err := metav1.LabelSelectorAsSelector(peer.PodSelector)
		if err != nil {
			return nil
		}
		podSelector = s
	}

	var out []*podState
	for _, p := range pods {
		if p.hostNetwork || p.ip == "" {
			continue
		}
		if nsSelector == nil && podSelector != nil {
			// 仅 podSelector：限定策略命名空间
			if p.namespace != policyNS {
				continue
			}
		} else if nsSelector != nil && !nsSelector.Matches(labels.Set(p.nsLabels)) {
			continue
		}
		if podSelector != nil && !podSelector.Matches(labels.Set(p.labels)) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// subtractCIDRs 计算 base 减去 excepts 后的不相交子网集合（ipBlock.except 展开）
func subtractCIDRs(base string, excepts []string) ([]*net.IPNet, error) {
	_, baseNet, err := net.ParseCIDR(strings.TrimSpace(base))
	if err != nil {
		return nil, fmt.Errorf("invalid cidr %q, %w", base, err)
	}
	result := []*net.IPNet{baseNet}
	for _, e := range excepts {
		_, exNet, err := net.ParseCIDR(strings.TrimSpace(e))
		if err != nil {
			return nil, fmt.Errorf("invalid except cidr %q, %w", e, err)
		}
		var next []*net.IPNet
		for _, r := range result {
			next = append(next, splitSubnet(r, exNet)...)
		}
		result = next
	}
	return result, nil
}

// splitSubnet 把 net 中被 hole 覆盖的部分挖掉，返回剩余不相交子网
func splitSubnet(net_, hole *net.IPNet) []*net.IPNet {
	// 无交集或 hole 完全覆盖
	if !net_.Contains(hole.IP) {
		return []*net.IPNet{net_}
	}
	ones, bits := net_.Mask.Size()
	holeOnes, _ := hole.Mask.Size()
	if holeOnes <= ones {
		return nil // hole 覆盖整个 net
	}
	// 在 net 与 hole 的公共前缀处二分：生成 net 下比 hole 更短一层的两个子网，
	// 递归处理被 hole 命中的一支
	ones++
	left := &net.IPNet{IP: dupIP(net_.IP), Mask: net.CIDRMask(ones, bits)}
	right := &net.IPNet{IP: dupIP(net_.IP), Mask: net.CIDRMask(ones, bits)}
	// 置位新的一位
	bitIdx := ones - 1
	byteIdx := bitIdx / 8
	bitMask := byte(1) << (7 - bitIdx%8)
	right.IP[byteIdx] |= bitMask

	var out []*net.IPNet
	if holeContains(hole, left) {
		out = append(out, right)
	} else {
		out = append(out, splitSubnet(left, hole)...)
	}
	if holeContains(hole, right) {
		out = append(out, left)
	} else {
		out = append(out, splitSubnet(right, hole)...)
	}
	return out
}

func holeContains(hole, sub *net.IPNet) bool {
	ones, _ := hole.Mask.Size()
	subOnes, _ := sub.Mask.Size()
	return subOnes >= ones && hole.Contains(sub.IP)
}

func dupIP(ip net.IP) net.IP {
	d := make(net.IP, len(ip))
	copy(d, ip)
	return d
}
