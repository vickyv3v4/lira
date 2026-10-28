// +build ignore

/*
Lira 共享 ENI eBPF 网络策略数据面（IPv4 / tc）。

挂载点（与《共享ENI-EBPF网络策略设计方案》§4 对应）：
  lira_main_egress   ① ENI 主口 tc-egress  —— 所有 pod 出向（slave 出向包内核切到主口发出）
  lira_main_ingress  ② ENI 主口 tc-ingress —— 全部入向（ipvlan rx_handler 分发之前）
  lira_veth_egress   ③ 宿主机 veth tc-egress —— host→pod、SVC DNAT 后投递
  lira_pod_egress    ④ pod 内 ipvlan 口 tc-egress —— 同 ENI 直投唯一落点 + 源校验
  lira_pod_ingress   ④ pod 内 ipvlan 口 tc-ingress —— 目的校验 + ingress 策略

编译：clang -O2 -g -target bpf -D__TARGET_ARCH_x86 -c bpf/lira.bpf.c -o lira.bpf.o
内核要求：clsact 4.5 / LRU 4.10 / lpm trie 4.11 / ringbuf 5.8（全功能建议 5.8+）。
*/

#include <linux/bpf.h>
#include <linux/if_ether.h>
#include <linux/ip.h>
#include <linux/tcp.h>
#include <linux/udp.h>
#include <linux/in.h>

#include <bpf/bpf_helpers.h>
#include <bpf/bpf_endian.h>

char __license[] SEC("license") = "Dual BSD/GPL";

/* ------------------------------------------------------------------ */
/* 常量                                                                */
/* ------------------------------------------------------------------ */

#define TC_ACT_OK       0
#define TC_ACT_SHOT     2

/* 固定身份：0 保留为“未知”，1 为节点自身 / hostNetwork pod（设计 §3） */
#define LIRA_ID_UNKNOWN 0
#define LIRA_ID_HOST    1

/* 策略方向 */
#define LIRA_DIR_EGRESS  1
#define LIRA_DIR_INGRESS 2

/* 协议号；0 表示任意协议 */
#define LIRA_PROTO_ANY 0

/* ct 超时（纳秒） */
#define LIRA_CT_TIMEOUT_TCP 86400000000000ULL /* 24h */
#define LIRA_CT_TIMEOUT_UDP 300000000000ULL   /* 5min  */

#define LIRA_MAX_PORTS 8

/* 挂载点编号，用于事件上报 */
#define LIRA_HOOK_MAIN_EGRESS   1
#define LIRA_HOOK_MAIN_INGRESS  2
#define LIRA_HOOK_VETH_EGRESS   3
#define LIRA_HOOK_POD_EGRESS    4
#define LIRA_HOOK_POD_INGRESS   5

/* 丢弃原因 */
#define LIRA_DROP_SPOOF        1 /* 源/目的校验失败（防伪造）      */
#define LIRA_DROP_FRAG         2 /* 非首分片，无法判定             */
#define LIRA_DROP_POLICY       3 /* 未命中 allow 规则（默认拒绝）  */
#define LIRA_DROP_NO_IDENTITY  4 /* 身份表缺项                     */

/* ------------------------------------------------------------------ */
/* 数据结构（与 pkg/ebpf/types.go 逐字段对应，禁止调整字段顺序）          */
/* ------------------------------------------------------------------ */

/* pod_ip map：PodIP → 身份ID。控制面按 cluster 全量写（跨节点互访需要对端身份）。 */
struct lira_ip_value {
	__u32 identity;
};

/* 策略条目（身份对）：(源身份, 目的身份, 协议, 端口) 存在即允许。
 * 语义为“双向交集”：仅当源侧 egress 与目的侧 ingress 同时允许时，
 * 控制面才写入该条目（设计 §5.2）。port=0 表示任意端口，proto=0 表示任意协议。 */
struct lira_pair_key {
	__u32 src_identity;
	__u32 dst_identity;
	__u16 proto;
	__u16 port;
};

/* 策略条目（CIDR 对端，LPM trie）。
 * bpf_lpm_trie_key 要求 prefixlen 位于首个字段，因此把 identity/direction/proto
 * 编进前缀数据：prefixlen = 96 + ip 前缀长度，即前 96bit 必须完全相等，
 * 只有 ip 部分做最长前缀匹配。 */
struct lira_cidr_key {
	__u32 prefixlen; /* 96 + ip_prefix_len */
	__u32 identity;
	__u32 dir_proto; /* dir<<16 | proto */
	__u8  ip[4];
};

/* CIDR 条目的端口集合：nports=0 表示任意端口 */
struct lira_cidr_value {
	__u16 nports;
	__u16 ports[LIRA_MAX_PORTS];
};

/* 隔离表：条目存在表示该身份在该方向被至少一条 NetworkPolicy 选中（需要强制策略）。
 * 未隔离 = 不存在条目 = 全放行（NetworkPolicy 白名单语义）。 */
struct lira_iso_key {
	__u32 identity;
	__u32 dir;
};

/* 连接表：首个通过策略检查的包建条目，回程/后续包命中直接放行（设计 §5.4）。 */
struct lira_ct_key {
	__be32 src;
	__be32 dst;
	__u16  sport;
	__u16  dport;
	__u8   proto;
	__u8   pad;
};

struct lira_ct_value {
	__u64 last_seen_ns;
};

/* 节点配置（当前仅节点自身 IP，key 恒为 0） */
struct lira_node_value {
	__be32 node_ip;
};

/* 丢弃事件（ringbuf 上报，daemon 消费） */
struct lira_event {
	__be32 src;
	__be32 dst;
	__u32  src_identity;
	__u32  dst_identity;
	__u16  sport;
	__u16  dport;
	__u8   proto;
	__u8   hook;
	__u8   verdict;
	__u8   reason;
};

/* pod 内 ipvlan 口 ifindex → 该 pod 允许的源 IP（防伪造第一层，设计 §7） */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 4096);
	__type(key, __u32);
	__type(value, __be32);
} lira_slave_src4 SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 65536);
	__type(key, __be32);
	__type(value, struct lira_ip_value);
} lira_pod_ip4 SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 131072);
	__type(key, struct lira_pair_key);
	__type(value, __u8);
} lira_policy_id4 SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_LPM_TRIE);
	__uint(max_entries, 65536);
	__uint(map_flags, BPF_F_NO_PREALLOC);
	__type(key, struct lira_cidr_key);
	__type(value, struct lira_cidr_value);
} lira_policy_cidr4 SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 16384);
	__type(key, struct lira_iso_key);
	__type(value, __u8);
} lira_isolated4 SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__uint(max_entries, 262144);
	__type(key, struct lira_ct_key);
	__type(value, struct lira_ct_value);
} lira_ct4 SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 4);
	__type(key, __u32);
	__type(value, struct lira_node_value);
} lira_node_cfg4 SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_RINGBUF);
	__uint(max_entries, 1 << 22);
} lira_events SEC(".maps");

/* ------------------------------------------------------------------ */
/* 解析                                                                */
/* ------------------------------------------------------------------ */

struct lira_tuple {
	__be32 src;
	__be32 dst;
	__u16  sport;
	__u16  dport;
	__u8   proto;
};

/* 返回：0 成功；-1 非 IPv4/不支持（调用方放行）；-2 非首分片（调用方丢弃） */
static __always_inline int lira_parse(struct __sk_buff *skb, struct lira_tuple *t)
{
	void *data = (void *)(long)skb->data;
	void *data_end = (void *)(long)skb->data_end;

	struct ethhdr *eth = data;
	if ((void *)(eth + 1) > data_end)
		return -1;
	if (eth->h_proto != bpf_htons(ETH_P_IP))
		return -1; /* ARP / IPv6 等不在本策略范围内 */

	struct iphdr *ip = (void *)(eth + 1);
	if ((void *)(ip + 1) > data_end)
		return -1;
	if (ip->version != 4 || ip->ihl < 5)
		return -1;

	__u16 frag_off = bpf_ntohs(ip->frag_off);
	if (frag_off & 0x1fff)
		return -2; /* 非首分片：拿不到端口，统一拒绝 */

	__u32 ihl = (__u32)ip->ihl * 4;
	if ((void *)ip + ihl > data_end)
		return -1;

	t->src = ip->saddr;
	t->dst = ip->daddr;
	t->proto = ip->protocol;
	t->sport = 0;
	t->dport = 0;

	switch (ip->protocol) {
	case IPPROTO_TCP: {
		struct tcphdr *tcp = (void *)ip + ihl;
		if ((void *)(tcp + 1) > data_end)
			return -1;
		t->sport = tcp->source;
		t->dport = tcp->dest;
		break;
	}
	case IPPROTO_UDP: {
		struct udphdr *udp = (void *)ip + ihl;
		if ((void *)(udp + 1) > data_end)
			return -1;
		t->sport = udp->source;
		t->dport = udp->dest;
		break;
	}
	case IPPROTO_ICMP:
		/* 端口记 0：只能命中 (proto=ICMP, port=0) 或 any 规则 */
		break;
	default:
		break;
	}
	return 0;
}

/* ------------------------------------------------------------------ */
/* 查表辅助                                                            */
/* ------------------------------------------------------------------ */

static __always_inline void *lira_pod_ip(__be32 ip)
{
	return bpf_map_lookup_elem(&lira_pod_ip4, &ip);
}

static __always_inline int lira_is_isolated(__u32 identity, __u32 dir)
{
	struct lira_iso_key k = {
		.identity = identity,
		.dir = dir,
	};
	return bpf_map_lookup_elem(&lira_isolated4, &k) != NULL;
}

/* 身份对策略：依次尝试 (proto,port) / (proto,any) / (any,any)。
 * (any,port) 无对应 NetworkPolicy 表达能力，不查。 */
static __always_inline int lira_pair_allow(__u32 src, __u32 dst, __u16 proto, __u16 port)
{
	struct lira_pair_key k = {
		.src_identity = src,
		.dst_identity = dst,
		.proto = proto,
		.port = port,
	};
	if (bpf_map_lookup_elem(&lira_policy_id4, &k))
		return 1;
	k.port = 0;
	if (bpf_map_lookup_elem(&lira_policy_id4, &k))
		return 1;
	k.proto = LIRA_PROTO_ANY;
	if (bpf_map_lookup_elem(&lira_policy_id4, &k))
		return 1;
	return 0;
}

/* CIDR 策略：LPM 最长前缀匹配一次，再在端口集合内匹配 */
static __always_inline int lira_cidr_allow(__u32 identity, __u32 dir, __be32 ip,
					   __u16 proto, __u16 port)
{
	struct lira_cidr_key k = {
		.prefixlen = 128,
		.identity = identity,
		.dir_proto = (dir << 16) | proto,
	};
	__builtin_memcpy(k.ip, &ip, sizeof(k.ip));

	struct lira_cidr_value *v = bpf_map_lookup_elem(&lira_policy_cidr4, &k);
	if (!v)
		return 0;
	if (v->nports == 0)
		return 1;

#pragma unroll
	for (int i = 0; i < LIRA_MAX_PORTS; i++) {
		if (i >= v->nports)
			break;
		if (v->ports[i] == 0 || v->ports[i] == port)
			return 1;
	}
	return 0;
}

static __always_inline void lira_ct_key_of(struct lira_ct_key *k, const struct lira_tuple *t)
{
	__builtin_memset(k, 0, sizeof(*k));
	k->src = t->src;
	k->dst = t->dst;
	k->sport = t->sport;
	k->dport = t->dport;
	k->proto = t->proto;
}

/* 连接表查询：正向与反向各查一次，命中则刷新 */
static __always_inline int lira_ct_lookup(const struct lira_tuple *t)
{
	struct lira_ct_key k, rk;
	lira_ct_key_of(&k, t);
	struct lira_ct_value *v = bpf_map_lookup_elem(&lira_ct4, &k);
	if (!v) {
		lira_ct_key_of(&rk, t);
		__be32 tmp_ip = rk.src;
		rk.src = rk.dst;
		rk.dst = tmp_ip;
		__u16 tmp_port = rk.sport;
		rk.sport = rk.dport;
		rk.dport = tmp_port;
		v = bpf_map_lookup_elem(&lira_ct4, &rk);
		if (!v)
			return 0;
		k = rk;
	}

	__u64 now = bpf_ktime_get_ns();
	__u64 timeout = t->proto == IPPROTO_TCP ? LIRA_CT_TIMEOUT_TCP : LIRA_CT_TIMEOUT_UDP;
	if (now - v->last_seen_ns > timeout) {
		bpf_map_delete_elem(&lira_ct4, &k);
		return 0;
	}
	v->last_seen_ns = now;
	return 1;
}

static __always_inline void lira_ct_create(const struct lira_tuple *t)
{
	struct lira_ct_key k;
	lira_ct_key_of(&k, t);
	struct lira_ct_value v = {
		.last_seen_ns = bpf_ktime_get_ns(),
	};
	bpf_map_update_elem(&lira_ct4, &k, &v, BPF_NOEXIST);
}

static __always_inline void lira_emit_drop(__u8 hook, __u8 reason,
					   const struct lira_tuple *t,
					   __u32 src_id, __u32 dst_id)
{
	struct lira_event *e = bpf_ringbuf_reserve(&lira_events, sizeof(*e), 0);
	if (!e)
		return;
	e->src = t->src;
	e->dst = t->dst;
	e->src_identity = src_id;
	e->dst_identity = dst_id;
	e->sport = t->sport;
	e->dport = t->dport;
	e->proto = t->proto;
	e->hook = hook;
	e->verdict = TC_ACT_SHOT;
	e->reason = reason;
	bpf_ringbuf_submit(e, 0);
}

/* 节点自身 IP（未配置时返回 0） */
static __always_inline __be32 lira_node_ip(void)
{
	__u32 zero = 0;
	struct lira_node_value *v = bpf_map_lookup_elem(&lira_node_cfg4, &zero);
	if (!v)
		return 0;
	return v->node_ip;
}

/* ------------------------------------------------------------------ */
/* ① ENI 主口 tc-egress                                                */
/* ------------------------------------------------------------------ */

SEC("tc/egress")
int lira_main_egress(struct __sk_buff *skb)
{
	struct lira_tuple t;
	int r = lira_parse(skb, &t);
	if (r == -1)
		return TC_ACT_OK;

	/* 节点自身/host 出向流量不归策略管 */
	if (t.src == lira_node_ip())
		return TC_ACT_OK;

	struct lira_ip_value *sv = lira_pod_ip(t.src);
	if (!sv) {
		/* 防伪造外层：本节点未分配的 IP 不允许从 ENI 出网（设计 §7） */
		lira_emit_drop(LIRA_HOOK_MAIN_EGRESS, LIRA_DROP_SPOOF, &t, 0, 0);
		return TC_ACT_SHOT;
	}
	__u32 src_id = sv->identity;
	if (src_id == LIRA_ID_HOST)
		return TC_ACT_OK; /* hostNetwork pod 视作节点身份 */

	__u32 dst_id = LIRA_ID_UNKNOWN;
	struct lira_ip_value *dv = lira_pod_ip(t.dst);
	if (dv)
		dst_id = dv->identity;

	if (lira_ct_lookup(&t))
		return TC_ACT_OK;

	/* 未隔离 = 没有 NetworkPolicy 选中 = 全放行 */
	if (!lira_is_isolated(src_id, LIRA_DIR_EGRESS))
		return TC_ACT_OK;

	int allowed;
	if (dv && dst_id != LIRA_ID_HOST) {
		/* 对端是集群内 pod（含跨节点/同节点异 ENI）：查身份对 */
		allowed = lira_pair_allow(src_id, dst_id, t.proto, t.dport);
	} else {
		/* 集群外或节点/hostNetwork：按 CIDR 判定 */
		allowed = lira_cidr_allow(src_id, LIRA_DIR_EGRESS, t.dst, t.proto, t.dport);
	}
	if (allowed) {
		lira_ct_create(&t);
		return TC_ACT_OK;
	}

	lira_emit_drop(LIRA_HOOK_MAIN_EGRESS, LIRA_DROP_POLICY, &t, src_id, dst_id);
	return TC_ACT_SHOT;
}

/* ------------------------------------------------------------------ */
/* ② ENI 主口 tc-ingress（ipvlan 分发之前）                             */
/* ------------------------------------------------------------------ */

SEC("tc/ingress")
int lira_main_ingress(struct __sk_buff *skb)
{
	struct lira_tuple t;
	int r = lira_parse(skb, &t);
	if (r == -1)
		return TC_ACT_OK;
	if (r == -2)
		return TC_ACT_SHOT;

	/* 目的不是本节点 pod（节点自身服务/hostNetwork）→ 放行 */
	struct lira_ip_value *dv = lira_pod_ip(t.dst);
	if (!dv)
		return TC_ACT_OK;
	__u32 dst_id = dv->identity;
	if (dst_id == LIRA_ID_HOST)
		return TC_ACT_OK;

	/* 节点自身出向流量的回包/直连流量旁路（设计 §8） */
	if (t.src == lira_node_ip())
		return TC_ACT_OK;

	if (lira_ct_lookup(&t))
		return TC_ACT_OK;

	if (!lira_is_isolated(dst_id, LIRA_DIR_INGRESS))
		return TC_ACT_OK;

	__u32 src_id = LIRA_ID_UNKNOWN;
	struct lira_ip_value *sv = lira_pod_ip(t.src);
	if (sv)
		src_id = sv->identity;

	int allowed;
	if (sv && src_id != LIRA_ID_HOST) {
		/* 对端是集群内 pod：查身份对（跨节点到达时身份来自全量同步） */
		allowed = lira_pair_allow(src_id, dst_id, t.proto, t.dport);
	} else {
		/* 集群外来源：按 CIDR 判定 */
		allowed = lira_cidr_allow(dst_id, LIRA_DIR_INGRESS, t.src, t.proto, t.dport);
	}
	if (allowed) {
		lira_ct_create(&t);
		return TC_ACT_OK;
	}

	lira_emit_drop(LIRA_HOOK_MAIN_INGRESS, LIRA_DROP_POLICY, &t, src_id, dst_id);
	return TC_ACT_SHOT; /* 丢弃后不进入 ipvlan 分发 */
}

/* ------------------------------------------------------------------ */
/* ③ 宿主机 veth tc-egress（host→pod、SVC DNAT 后投递）                */
/* ------------------------------------------------------------------ */

SEC("tc/egress")
int lira_veth_egress(struct __sk_buff *skb)
{
	struct lira_tuple t;
	int r = lira_parse(skb, &t);
	if (r == -1)
		return TC_ACT_OK;
	if (r == -2)
		return TC_ACT_SHOT;

	struct lira_ip_value *dv = lira_pod_ip(t.dst);
	if (!dv || dv->identity == LIRA_ID_HOST)
		return TC_ACT_OK;
	__u32 dst_id = dv->identity;

	/* 节点发起（kubelet 探针、host 进程）永远放行：NetworkPolicy 语义不匹配节点流量 */
	if (t.src == lira_node_ip())
		return TC_ACT_OK;

	if (lira_ct_lookup(&t))
		return TC_ACT_OK;

	struct lira_ip_value *sv = lira_pod_ip(t.src);
	if (!sv || sv->identity == LIRA_ID_HOST)
		return TC_ACT_OK;
	__u32 src_id = sv->identity;

	/* 双向均未隔离 → 全放行；否则查交集条目
	 * （SVC 场景 DNAT 后源仍是真实 pod IP，一次同时覆盖源 egress 与后端 ingress，设计 §5.3-链路三） */
	if (!lira_is_isolated(src_id, LIRA_DIR_EGRESS) &&
	    !lira_is_isolated(dst_id, LIRA_DIR_INGRESS))
		return TC_ACT_OK;

	if (lira_pair_allow(src_id, dst_id, t.proto, t.dport)) {
		lira_ct_create(&t);
		return TC_ACT_OK;
	}

	lira_emit_drop(LIRA_HOOK_VETH_EGRESS, LIRA_DROP_POLICY, &t, src_id, dst_id);
	return TC_ACT_SHOT;
}

/* ------------------------------------------------------------------ */
/* ④ pod 内 ipvlan 口 tc-egress（同 ENI 直投唯一执行点）                */
/* ------------------------------------------------------------------ */

SEC("tc/egress")
int lira_pod_egress(struct __sk_buff *skb)
{
	struct lira_tuple t;
	int r = lira_parse(skb, &t);
	if (r == -1)
		return TC_ACT_OK;
	if (r == -2)
		return TC_ACT_SHOT;

	/* 防伪造内层：源 IP 必须是本 pod 分配的 IP（设计 §7 双层校验之一） */
	__be32 *allowed_src = bpf_map_lookup_elem(&lira_slave_src4, &skb->ifindex);
	if (!allowed_src || *allowed_src != t.src) {
		lira_emit_drop(LIRA_HOOK_POD_EGRESS, LIRA_DROP_SPOOF, &t, 0, 0);
		return TC_ACT_SHOT;
	}

	/* pod→节点流量（nodePort、metadata 等）不归策略管（设计 §8） */
	if (t.dst == lira_node_ip())
		return TC_ACT_OK;

	struct lira_ip_value *sv = lira_pod_ip(t.src);
	if (!sv) {
		lira_emit_drop(LIRA_HOOK_POD_EGRESS, LIRA_DROP_NO_IDENTITY, &t, 0, 0);
		return TC_ACT_SHOT;
	}
	__u32 src_id = sv->identity;

	if (lira_ct_lookup(&t))
		return TC_ACT_OK;

	if (!lira_is_isolated(src_id, LIRA_DIR_EGRESS))
		return TC_ACT_OK;

	__u32 dst_id = LIRA_ID_UNKNOWN;
	struct lira_ip_value *dv = lira_pod_ip(t.dst);
	if (dv)
		dst_id = dv->identity;

	int allowed;
	if (dv && dst_id != LIRA_ID_HOST)
		allowed = lira_pair_allow(src_id, dst_id, t.proto, t.dport);
	else
		allowed = lira_cidr_allow(src_id, LIRA_DIR_EGRESS, t.dst, t.proto, t.dport);
	if (allowed) {
		lira_ct_create(&t);
		return TC_ACT_OK;
	}

	lira_emit_drop(LIRA_HOOK_POD_EGRESS, LIRA_DROP_POLICY, &t, src_id, dst_id);
	return TC_ACT_SHOT;
}

/* ------------------------------------------------------------------ */
/* ④ pod 内 ipvlan 口 tc-ingress                                       */
/* ------------------------------------------------------------------ */

SEC("tc/ingress")
int lira_pod_ingress(struct __sk_buff *skb)
{
	struct lira_tuple t;
	int r = lira_parse(skb, &t);
	if (r == -1)
		return TC_ACT_OK;
	if (r == -2)
		return TC_ACT_SHOT;

	/* 目的校验：投进本 pod 的包目的必须是本 pod IP */
	__be32 *allowed_dst = bpf_map_lookup_elem(&lira_slave_src4, &skb->ifindex);
	if (!allowed_dst || *allowed_dst != t.dst) {
		lira_emit_drop(LIRA_HOOK_POD_INGRESS, LIRA_DROP_SPOOF, &t, 0, 0);
		return TC_ACT_SHOT;
	}

	/* 节点发起的流量（kubelet 探活等）永远放行（设计 §8） */
	if (t.src == lira_node_ip())
		return TC_ACT_OK;

	struct lira_ip_value *dv = lira_pod_ip(t.dst);
	if (!dv) {
		lira_emit_drop(LIRA_HOOK_POD_INGRESS, LIRA_DROP_NO_IDENTITY, &t, 0, 0);
		return TC_ACT_SHOT;
	}
	__u32 dst_id = dv->identity;

	if (lira_ct_lookup(&t))
		return TC_ACT_OK;

	if (!lira_is_isolated(dst_id, LIRA_DIR_INGRESS))
		return TC_ACT_OK;

	__u32 src_id = LIRA_ID_UNKNOWN;
	struct lira_ip_value *sv = lira_pod_ip(t.src);
	if (sv)
		src_id = sv->identity;

	int allowed;
	if (sv && src_id != LIRA_ID_HOST)
		allowed = lira_pair_allow(src_id, dst_id, t.proto, t.dport);
	else
		allowed = lira_cidr_allow(dst_id, LIRA_DIR_INGRESS, t.src, t.proto, t.dport);
	if (allowed) {
		lira_ct_create(&t);
		return TC_ACT_OK;
	}

	lira_emit_drop(LIRA_HOOK_POD_INGRESS, LIRA_DROP_POLICY, &t, src_id, dst_id);
	return TC_ACT_SHOT;
}
