package ebpf

import (
	"context"
	"net"
)

// 与 bpf/lira.bpf.c 逐字段对应的数据结构与常量。
// 注意：结构体字段顺序与 C 侧保持一致，C 侧均无隐式填充（pad 字段显式列出），
// Go 侧依赖 cilium/ebpf 的 native 结构体编解码，两边 sizeof 必须一致
// （layout_linux_test.go 有编译期之外的运行时断言）。

// 固定身份（设计 §3）
const (
	IdentityUnknown uint32 = 0 // 保留：身份表缺项
	IdentityHost    uint32 = 1 // 节点自身 / hostNetwork pod
)

// 策略方向
const (
	DirEgress  uint32 = 1
	DirIngress uint32 = 2
)

// 协议号（0 = 任意协议）
const (
	ProtoAny  uint16 = 0
	ProtoTCP  uint16 = 6
	ProtoUDP  uint16 = 17
	ProtoICMP uint16 = 1
	ProtoSCTP uint16 = 132
)

// CIDR 条目可携带的端口数上限，超出时控制面降级为“任意端口”
const MaxPortsPerCIDR = 8

// PinRoot 所有 map/program 的 pin 目录（设计 §5.5：daemon 重启不影响已挂载程序）
const PinRoot = "/sys/fs/bpf/lira"

// BPFObjectPath 数据面 ELF 编译产物路径（init-cni 容器安装到宿主机）
const BPFObjectPath = "/opt/cni/lira/lira.bpf.o"

// map / program 名称（与 bpf/lira.bpf.c 中 SEC 声明一致）
const (
	ProgMainEgress  = "lira_main_egress"
	ProgMainIngress = "lira_main_ingress"
	ProgVethEgress  = "lira_veth_egress"
	ProgPodEgress   = "lira_pod_egress"
	ProgPodIngress  = "lira_pod_ingress"

	MapSlaveSrc   = "lira_slave_src4"
	MapPodIP      = "lira_pod_ip4"
	MapPolicyID   = "lira_policy_id4"
	MapPolicyCIDR = "lira_policy_cidr4"
	MapIsolated   = "lira_isolated4"
	MapCT         = "lira_ct4"
	MapNodeCfg    = "lira_node_cfg4"
	MapEvents     = "lira_events"
)

// ---- map 键值结构（与 C 侧 struct 对应）----

// PodIPValue 对应 struct lira_ip_value
type PodIPValue struct {
	Identity uint32
}

// PairKey 对应 struct lira_pair_key，条目存在即允许（双向交集，设计 §5.2）
type PairKey struct {
	SrcIdentity uint32
	DstIdentity uint32
	Proto       uint16
	Port        uint16 // 0 = 任意端口
}

// CIDRKey 对应 struct lira_cidr_key。
// bpf_lpm_trie_key 要求 prefixlen 在首字段，identity/dir/proto 编入前缀数据：
// prefixlen = 96 + ipPrefixLen，前 96bit 精确相等，仅 ip 段做最长前缀匹配。
type CIDRKey struct {
	PrefixLen uint32
	Identity  uint32
	DirProto  uint32 // Dir<<16 | Proto
	IP        [4]byte
}

// CIDRValue 对应 struct lira_cidr_value；NPorts=0 表示任意端口
type CIDRValue struct {
	NPorts uint16
	Ports  [MaxPortsPerCIDR]uint16
}

// ISOKey 对应 struct lira_iso_key；条目存在 = 该身份该方向被 NetworkPolicy 选中
type ISOKey struct {
	Identity uint32
	Dir      uint32
}

// SlaveSrcValue pod 内 ipvlan 口 ifindex → 允许的源 IP（防伪造第一层）
type SlaveSrcValue struct {
	IP uint32 // 网络序 4 字节
}

// NodeValue 对应 struct lira_node_value
type NodeValue struct {
	NodeIP uint32 // 网络序 4 字节
}

// beIP 把 IPv4 转成网络序 4 字节整数（写入 C 侧 __be32 字段）
func beIP(ip net.IP) uint32 {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0
	}
	return uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3])
}

// uint32ToIP 还原 beIP 写入的地址
func uint32ToIP(v uint32) net.IP {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).To4()
}

// makeCIDRKey 构造 CIDR LPM key（Go 侧控制面写入用）
func makeCIDRKey(identity, dir uint32, proto uint16, prefix *net.IPNet) CIDRKey {
	ones, _ := prefix.Mask.Size()
	ip := prefix.IP.To4()
	k := CIDRKey{
		PrefixLen: 96 + uint32(ones),
		Identity:  identity,
		DirProto:  dir<<16 | uint32(proto),
	}
	copy(k.IP[:], ip)
	return k
}

// Backend 是策略后端（pkg/ebpf 的 Manager 实现）。非 linux 平台为 no-op 桩。
type Backend interface {
	// UpsertPodIP 写 PodIP → 身份映射（daemon 建链 / agent 全量同步）
	UpsertPodIP(ip net.IP, identity uint32) error
	// DeletePodIP 删除 PodIP 映射（pod 回收）
	DeletePodIP(ip net.IP) error
	// WriteNodeConfig 写节点自身 IP（数据面用于旁路节点流量）
	WriteNodeConfig(nodeIP net.IP) error
	// EnsureENIAttached 确保 ENI 主口已挂 ①② 程序（按 ENI MAC 找设备，幂等）
	EnsureENIAttached(eniMACs []string) error
	// DrainEvents 消费 ringbuf 丢弃事件（内部自带退出逻辑，随 ctx 结束）
	DrainEvents(ctx context.Context)
	// ReplaceIsolated 全量替换隔离表（agent 重算结果）
	ReplaceIsolated(entries []ISOKey) error
	// ReplacePair 全量替换身份对策略表
	ReplacePair(keys []PairKey) error
	// ReplaceCIDR 全量替换 CIDR 策略表
	ReplaceCIDR(entries map[CIDRKey]CIDRValue) error
}
