# 共享 ENI eBPF 网络策略 — 实现与部署

> 配套设计文档：[共享ENI-EBPF网络策略设计方案.md](../共享ENI-EBPF网络策略设计方案.md)
> 本文档说明落地实现、工作原理、部署步骤与验证方法。

## 1. 方案概述

lira 是共享 ENI（ipvlan L2 子接口）CNI：同 ENI 的多个 pod 口共享主口 MAC，
流量绕过宿主机协议栈，iptables 无法观测，标准 NetworkPolicy 引擎（Calico/Cilium
的 kube-proxy 模式等）在此架构下失效。本实现按设计方案将 NetworkPolicy 语义
下沉到 eBPF tc 层：

- **5 个 tc 程序** 覆盖 4 类挂载点（ENI 主口双向 / 宿主机 veth 出向 / pod ipvlan 口双向），
  单 ELF（`bpf/lira.bpf.c`）产出；
- **3 张策略 map + 2 张身份 map**，控制面全量重算、数据面 O(1) 查表判定；
- 控制面（`pkg/policyagent`）watch 集群 Pod / NetworkPolicy / Namespace，去抖后全量重算，
  预计算身份对交集，数据面单次 hash 查表即可给出放行/丢弃；
- 有状态 ct 表：首包过策略检查后建条目，后续报文直接放行（TCP 24h / UDP 5min 老化）。

## 2. 代码结构

| 位置 | 内容 |
| --- | --- |
| `bpf/lira.bpf.c` | 数据面：5 个 tc 程序 + 8 张 map，单 ELF |
| `pkg/ebpf/` | Go 侧加载器：pin 目录管理、map 写入口、tc attach、ringbuf 事件消费 |
| `pkg/policyagent/` | 控制面：身份计算（identity.go）、NetworkPolicy 语义展开（compute.go）、全量同步 reconciler（reconciler.go） |
| `pkg/service/service.go` | CNI 建链 RPC：AllocIP 顺手写 pod_ip→身份，ReleaseIP 尽力清理 |
| `cmd/lira/main.go` | CNI ADD/DEL：挂载/清理 pod 口 ③④ 程序与 slave_src 条目 |
| `daemon/daemon_ebpf_linux.go` | daemon 装配：加载器初始化、controller-runtime Manager + 策略 reconciler 注册、ENI 挂载 reconcile、事件消费 |
| `yaml/networkpolicy-examples.yaml` | NetworkPolicy 例子 |
| `yaml/lira.yaml` | 部署清单（daemon 已提权 + bpf fs 挂载） |

## 3. 原理说明

### 3.1 挂载点与流量路径

```
                    ┌─────────────────────────────────────────┐
  pod A (netns)     │                host netns                │       远端
  ┌──────────┐      │  ┌────────┐   ┌──────────────┐          │    ┌────────┐
  │ eth0 ④③ │◄─────┼─►│veth③◄──┼──►│ ENI 主口 ①② │◄────────┼───►│  外部   │
  └──────────┘      │  └────────┘   └──────────────┘          │    └────────┐
                    └─────────────────────────────────────────┘
```

| 程序 | 挂载点 | 职责 |
| --- | --- | --- |
| ① lira_main_egress | ENI 主口 tc egress | 节点出网唯一出口：pod 出向统一策略判定 + 防伪造（src∈pod_ip） |
| ② lira_main_ingress | ENI 主口 tc ingress | 目的过滤（dst∈pod_ip），跨节点入向策略 + 驳回非本节点 IP |
| ③ lira_veth_egress | 宿主机 veth tc egress | host→pod 注入路径策略（kubelet 探针旁路） |
| ④ lira_pod_{egress,ingress} | pod 内 ipvlan 口 | 同 ENI 直投路径（绕过 ENI 主口 tc）本地过滤 + 顑伪造内层 |

同 ENI pod↔pod 流量在 ipvlan L2 模式下由主口直接分发，不经过宿主机栈，
必须靠 ④ 补位；跨节点/跨 ENI 流量由 ①② 把关；host→pod 由 ③ 把关。

### 3.2 身份模型

identity = fnv1a32("ns/k1=v1,k2=v2"（键排序规范化）) & 0x7FFFFFFF，
0=unknown、1=host 保留。无状态计算、所有节点一致，无需中心分配。
PodENI 场景 pod 标签稳定（工作负载生成），碰撞概率在共享 ENI 场景可忽略
（生日界约 2^16 个身份才 50% 碰撞），三期引入 registry 消除。

### 3.3 map 设计（与 C 结构体逐字段对齐，layout_linux_test.go 断言）

| map | 类型 | 键→值 | 用途 |
| --- | --- | --- | --- |
| lira_slave_src4 | hash | ifindex → 允许源 IP | 防伪造第一层（pod 口） |
| lira_pod_ip4 | hash | pod IP → identity | IP→身份，全集群 pod 视图 |
| lira_policy_id4 | hash | (src_id, dst_id, proto, port) → u8 | 身份对白名单（预计算交集） |
| lira_policy_cidr4 | LPM | (identity, dir, proto, prefix) → ports | ipBlock/外部对端 |
| lira_isolated4 | hash | (identity, dir) → u8 | 该身份该方向是否被 NP 选中 |
| lira_ct4 | LRU | 五元组 → last_seen | 有状态放行 |
| lira_node_cfg4 | hash | 0 → node IP | 节点旁路判定 |
| lira_events | ringbuf | 24B 事件 | 丢弃事件日志（三期 metrics） |

CIDR LPM 键编码：prefixlen 首字段 + identity/dir/proto 编入前缀数据，
`prefixlen = 96 + ipPrefixLen`，仅 IP 段做最长前缀匹配。

### 3.4 判定流程（数据面）

```
包到达 → parse（IPv4 首片）→ 防伪造层（pod 口：slave_src 白名单；
ENI 主口：src 必须是本节点已分配 IP）→ 节点/host 旁路 → ct 命中放行 →
isolated 表查（未隔离全放行）→ pod 对端：pair_allow 单次查表；
CIDR 对端：LPM + 端口数组比对 → 放行则建 ct 条目
```

### 3.5 控制面流程（agent 全量重算，设计 §5.2）

1. controller-runtime Manager watch Pod / NetworkPolicy / Namespace（与仓库现有 controller 统一模式，缓存 client 首次 NewManager 后由 builder 建 informer，首次全量 List 后走 watch）→ 全部事件映射到固定 sync key，Reconcile 内去抖 500ms（窗口内不安静则 RequeueAfter 延后）；
2. 从 manager 缓存列出 pod 快照（跳过 hostNetwork/无 IPv4）与全部 NetworkPolicy；
3. compute()：按 K8s 语义展开规则 →
   - isolated 表 = 被 NP 选中身份 × 方向；
   - 身份对表 = 双向 allow 集合的交集（(A→B,p,port) 写入当且仅当
     A 未 egress 隔离或 (A,B,p,port)∈A.egress，且 B 侧对称成立）;
   - CIDR 表 = ipBlock（except 展开为不相交子网）+ 端口并集（>8 降级任意端口）；
4. ReplaceIsolated/ReplacePair/ReplaceCIDR 全量替换三张 map；
5. pod_ip 表 diff 同步：全量 upsert，删除仅针对 agent 上次成功写入过的条目
   （不误删 daemon AllocIP 刚写入、informer 未同步的条目）。

## 4. 部署方案

### 4.1 前置条件

| 项 | 要求 |
| --- | --- |
| 内核 | ≥ 5.8（ringbuf / CAP_BPF），5.10+ 推荐 |
| 工具链 | clang ≥ 10（含 BPF 后端）仅构建机需要 |
| 配置 | 集群已有 lira CNI 运行（yaml/lira.yaml） |
| RBAC | 已含 networkpolicies/pods/namespaces list+watch（lira.yaml 已具备） |

### 4.2 构建

```bash
# 1. Go 控制面二进制（lira CNI 插件 + daemon）
make lira lira-daemon

# 2. eBPF 数据面
make build-bpf        # 需 clang；无 clang 用 make build-bpf-docker

# 3. 镜像
make images           # daemon/init-cni/controller/resource
```

### 4.3 安装

1. `bin/lira.bpf.o` 打进 init-cni 镜像（Dockerfile-install 已加 COPY），
   init 容器安装到宿主机 `/opt/cni/lira/lira.bpf.o`，同时安装 CNI 二进制与 conflist；
2. daemon 以 root 运行，capabilities：NET_ADMIN（tc 挂载）+ SYS_ADMIN + BPF（bpf syscall），
   挂载 `/sys/fs/bpf`（hostPath DirectoryOrCreate）；
3. yaml/lira.yaml 已更新（caps/volumes/resources），直接 `kubectl apply -f yaml/lira.yaml`；
4. 启用即生效：pin 目录 /sys/fs/bpf/lira 出现后，CNI ADD 自动挂 ③④ 程序，
   daemon 加载 ①②；pin 目录不存在时功能自动禁用（CNI 侧跳过 attach）。

### 4.4 验证

```bash
# 数据面对象与挂载状态
bpftool prog show | grep lira
bpftool map show | grep lira
tc filter show dev eth0 ingress / egress    # ENI 主口 ①②
tc filter show dev <veth> egress            # ③
# pin 目录（pod 口程序）
ls /sys/fs/bpf/lira/progs/

# 策略 map 内容
bpftool map dump pinned /sys/fs/bpf/lira/lira_isolated4
bpftool map dump pinned /sys/fs/bpf/lira/lira_policy_id4
bpftool map dump pinned /sys/fs/bpf/lira/lira_policy_cidr4

# 功能禁用/启用：删除 pin 目录（mount --make-rprivate /sys/fs/bpf; rm -rf /sys/fs/bpf/lira）后重启 daemon
# 丢弃事件：kubectl logs -n kube-system lira-daemon-xxxxx | grep "policy drop"
```

### 4.5 回退

删除全部 NetworkPolicy → isolated 表清空 → 数据面全放行（等效关闭）。
彻底关闭：删除 /sys/fs/bpf/lira pin 目录并重启 daemon（不重挂程序）。

## 5. 已知边界（与设计 §8/§10 对应）

- 身份 hash 碰撞：fnv1a32 理论可碰撞，三期 registry 解决；
- 非 IPv4 首片以外分片丢弃（防绕过），ICMP 仅 any-proto 规则放行；
- 同 CIDR 混合协议端口规则 >8 端口时降级为任意端口（放行方向，安全）；
- pod 口 ④ 程序不随策略变化 reconcile，pod 重建（CNI ADD 重跑）时自动恢复；
- ICMP/UDP 双向性由 ct 补齐；UDP 回包 5min 内免策略判定。
