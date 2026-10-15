# 共享ENI网络策略设计方案（EBPF引入）

> 关联文档：《Lira共享ENI方案及实现》
> 关联代码：https://codelab.msxf.com/gc-bs/lira/-/commits/feature-multi-eniip
> 接口命名沿用主文档：eth1/eth2 = ENI，liraxxxx = 宿主机 veth，容器内 eth0 = ipvlan 口、veth1 = 宿主机通信口

## 1. 方案定位

共享 ENI 模式下，按 pod 做网络隔离有两个死结：

1. 所有 pod 共用一张 ENI 的安全组，云层面无法按 pod 隔离；
2. 同 ENI pod 互访走 ipvlan 二层直投不进内核协议栈，iptables 看不到。

本方案：保持 NetworkPolicy API 不变，执行层换成节点上的 eBPF。VPC 侧零改动，零新增 VPC 接口调用。

## 2. eBPF 原理

### 2.1 eBPF 是什么

eBPF 是内核提供的一种机制：允许把一段受限的 C 程序编译后加载进内核，挂到内核的固定事件点上执行（网卡收发包、系统调用等）。

"受限"是关键——程序不是普通内核模块，加载时内核的 **verifier** 会静态模拟执行程序的所有分支，逐条检查：不能越界读内存、不能解空指针、不能有无界循环、不能直接调用内核函数（只能调用内核白名单里的 helper 函数）。全部通过后 JIT 成本机指令运行。这保证了最坏情况是程序返回一个错误值，不会把内核搞挂。

本方案用到三个组成部分：

| 组成 | 是什么 | 在本方案中的角色 |
|---|---|---|
| 程序 | 挂在内核事件点上的处理函数，输入是一个数据包（skb），返回放行或丢弃 | 挂在各网卡口上做策略判定 |
| map | 内核里的键值表，内核态程序和用户态进程都能读写 | 存放身份表、策略表、连接表 |
| verifier/JIT | 加载时的安全校验和编译 | 保证程序安全，无需信任程序作者 |

### 2.2 程序挂在哪里：tc 挂载点

每块网卡可以挂一个 clsact（它不排队，纯粹提供两个执行点），对应 tc ingress 和 tc egress：

```
            出向                                            入向
pod ──► slave口 ──► [tc egress] ──► qdisc ──► 驱动 ══════► 驱动 ──► [tc ingress] ──► 协议栈/ipvlan分发
                    (包出网卡前)                    网线        (驱动收包后最先执行)
```

两个关键顺序（内核源码已核对）：

- **tc ingress 在 ipvlan 分发之前**：外部来的包先到 ENI 主口，tc ingress 执行完（放行/丢弃），才轮到 ipvlan 的 rx_handler 把包分发进某个 pod 的 slave 口。所以在主口 ingress 挂程序，能在分发前拦住所有入向包。
- **tc egress 在 qdisc 之前**：slave 的出向包在内核里被切到主口发出（`skb->dev = 主口; dev_queue_xmit`），经过主口的 tc egress，再进主口 qdisc 排队。所以在主口 egress 挂程序，能看到所有 pod 的出向包。

程序对包的处理：直接解析以太网头、IP 头、TCP/UDP 头（拿五元组），查 map 做判定，返回 TC_ACT_OK（放行）或 TC_ACT_SHOT（丢弃）。

### 2.3 map：策略数据的存放地

map 是内核维护的键值表，两个特性决定了它适合做策略：

- **热更新**：用户态进程调用一次更新接口，下一只包就查到新值，程序不需要重启。这就是"策略下发"的全部机制——程序固定不变，变的只是 map 内容。
- **可 pin**：map 挂在 /sys/fs/bpf 目录下，生命周期跟创建它的进程无关，daemon 和 agent 两个进程、以及内核里的程序，通过 pin 路径共享同一份数据。

本方案用四种 map 类型：hash（精确查 IP→身份）、LRU hash（连接表，满了自动淘汰最旧条目，不需要自己清理）、lpm trie（CIDR 最长前缀匹配，用于集群外网段规则）、ringbuf（程序向用户态上报丢弃事件）。

### 2.4 为什么不用 iptables

| | iptables | eBPF |
|---|---|---|
| 能否看到共享 ENI 的流量 | 看不到。ipvlan 的包绕过宿主机协议栈直接从主口进出，不经过任何 netfilter 链 | tc 挂在网卡上，包从网卡进出一律经过 |
| 匹配开销 | 线性遍历规则，O(规则数) | map 哈希查找，O(1) |
| 有状态 | 依赖 conntrack，规则静态 | 连接表自己建在 map 里，可编程 |
| 按 pod 控制 | 链按接口建，共享 ENI 下所有 pod 出同一个口，匹配条件不成立 | 按 IP 查身份，天然按 pod |

## 3. 身份模型

ipvlan 所有子口共用 ENI 的 MAC，身份只能用 IP 表达：

- 两级映射：**PodIP → 身份ID**；身份ID = 命名空间 + 标签集合的哈希
- 相同 labels 的 pod 共享身份 ID，策略条目按"身份对"组织，不随 pod 数膨胀（单节点 pod 上限 = ENI数 × 9，条目量级很小）
- 固定身份：节点自身与 hostNetwork pod 为一类；集群外网段按 CIDR 处理

## 4. 执行点布局

程序由 lira-daemon 统一挂载，一套编译产物 attach 到多个位置：

```
                 VPC云网络
                    │
        ┌───────────┴────────────┐
        │ [H2] ENI主口 tc-ingress │←──── 外部/跨节点入向（ipvlan分发之前）
        │ [H1] ENI主口 tc-egress  │───── pod出向、同节点不同ENI互访
        │        eth1(ENI)        │
        │   ┌──────┴───────┐      │
        │ liraxxxx      lirayyyy │
        │ [H3] tc-egress          │←──── 宿主机→pod、svc DNAT后的流量
        │   │             │       │
        │ veth1          veth1    │
        │ eth0(ipvlan)   eth0     │
        │ [H4] tc-egress/ingress  │←──── 同ENI pod互访直投 + 源IP校验
        │  Pod2          Pod1     │
        └─────────────────────────┘
```

| 挂载点 | netns | 拦到的流量 | 为什么必须在这 |
|---|---|---|---|
| H1 ENI主口 tc-egress | 宿主机 | 全部 pod 出向 | slave 出向都从主口发出（内核已验证） |
| H2 ENI主口 tc-ingress | 宿主机 | 全部入向（分发前） | tc ingress 在 ipvlan rx_handler 之前（内核已验证） |
| H3 liraxxxx tc-egress | 宿主机 | 宿主机路由到 pod 的流量 | host 访问 pod、svc 后端为本节点 pod，走的是 veth 不是 ipvlan |
| H4 pod内ipvlan口 tc | pod | 同 ENI 直投流量 + 本 pod 全部出向 | 同 ENI 互访内核直投，主口两个钩子都看不到，这是唯一落点 |

**六条数据链路 → 执行点对照：**

| 数据链路 | 源方向检查 | 目的方向检查 |
|---|---|---|
| 节点访问同节点pod | 节点=固定身份，按集群配置 | H3 |
| 同节点不同ENI互访 | H1 | H2 |
| 同节点相同ENI互访 | H4（源pod ipvlan egress） | H4（目的pod ipvlan ingress） |
| 跨节点pod访问 | H1 | H2 |
| 访问SVC后端同节点 | H3（DNAT后：src=真实源pod，dst=后端IP，一次同时查源的egress与后端的ingress策略） | 同左 |
| 访问集群外ECS | H1（CIDR 规则） | — |

## 5. 主干流程

### 5.1 建链流程（pod 创建）

```
pod调度 → PodENI创建 → lira-controller分配secondary ip → PodENI进入Bind
   │
   ▼
lira CNI发起gRPC → lira-daemon AllocIP返回（此时拿到PodIP/接口名）
   │
   ├─ 创建容器内ipvlan口 + veth口
   ├─ attach H4程序到ipvlan口（pin目录里的程序，attach到新口）
   ├─ 写pod_ip map：PodIP → 身份ID（身份ID由agent按labels预分配）
   ├─ 写slave_src map：ipvlan口ifindex → 本pod的IP
   └─ 宿主机veth口attach H3程序
```

### 5.2 策略下发流程（控制面）

```
policy agent（lira-controller内）：
  watch NetworkPolicy / Pod / PodENI 全量事件
      │
      ▼
  对每个本地身份计算：允许哪些对端（身份对/CIDR）× 哪些协议端口
      │
      ▼
  生成条目集 → 全量替换写 policy map（规模小，全量替换比增量收敛简单可靠）
      │
      ▼
  程序不重启，下一只包即按新策略判定
```

关键性质：**数据面只读 map，控制面只写 map**。策略变更、pod 增删都只体现为 map 内容变化，转发程序自始至终不变。

### 5.3 转发判定流程（数据面，三条典型链路逐步走包）

**链路一：跨节点 pod1 → pod2**

```
1. pod1 发包，出slave口 → H4: 校验源IP=本pod IP（防伪造），放行
2. 到ENI主口 → H1: 查pod_ip[src]得身份A，查pod_ip[dst]得身份B
   ct命中（已建立连接）→ 放行
   否则查policy(A→B,协议,端口) → 通过则写ct、放行；不通过 TC_ACT_SHOT 丢弃
3. VPC转发到对端节点
4. 对端ENI主口 → H2: 查dst身份，查ct/ingress policy → 放行
5. ipvlan分发进pod2的slave口 → pod2收到
```

**链路二：同ENI pod1 → pod2（内核直投，不过主口）**

```
1. pod1发包 → H4(源slave egress): 源IP校验 + 查policy(A→B) —— 唯一执行点
2. 内核直投到pod2的slave口
3. H4(目的slave ingress): 校验dst=本pod IP + 查ingress policy
4. pod2收到
```

**链路三：pod1 → SVC（后端为本节点pod2）**

```
1. pod1把目的写为ClusterIP，经veth1进宿主机（走veth，不走ipvlan）
2. kube-proxy做DNAT: dst改为pod2的IP（源不变，仍是pod1真实IP）
3. 宿主机路由: 10.0.8.67 dev liraxxxx → 出liraxxxx → H3:
   src=pod1真实IP、dst=pod2 → 查源身份A的egress策略 + 查pod2身份B的ingress策略
4. pod2经veth1收到；回程由ct放行并还原源为ClusterIP
```

### 5.4 有状态处理

NetworkPolicy 语义是有状态的（允许出去自动放回包）：首个包查 policy 通过后在 ct map 写入五元组+方向+超时；回程和后续包 ct 命中直接放行，不再查规则。LRU map 满时自动淘汰最旧连接，无需清理任务。

### 5.5 回收流程（pod 删除）

ReleaseIP → 清 pod_ip / slave_src / policy 中该身份的条目 → detach 并清 H4 → ct 条目随 LRU 自然过期。程序和 map 都 pin 在 /sys/fs/bpf/lira/ 下，daemon 重启不影响已挂载的程序。

## 6. BPF Map 设计

| Map | 类型 | 内容 | 写入方 |
|---|---|---|---|
| pod_ip | hash | PodIP → 身份ID | daemon（建链）/ agent |
| policy | hash + lpm子表 | (源身份，目的身份，协议，端口) → 动作；CIDR对端走lpm | agent |
| ct | LRU hash | 五元组+方向 → 状态/超时 | 数据面运行时 |
| slave_src | hash | ipvlan口ifindex → 本pod允许的源IP | daemon |
| events | ringbuf | 丢弃事件、策略命中计数 | 数据面上报 |

## 7. 安全边界

- **防伪造双层**：H4 校验源 IP = 本 pod 分配的 IP（防同 ENI pod 之间冒用）；H1 校验源属于本节点已分配 IP（防未分配 IP 出网）。共享 ENI 下 MAC 全同、VPC 无法区分，这两层是安全前提。
- **抗篡改分层**：H1–H3 在宿主机 netns，pod 不可触碰；H4 在 pod netns，特权容器理论上可摘除 → daemon 周期性 reconcile（对照 pin 目录重挂）。
- **内核版本要求**：按设施逐项核对——clsact 4.5 / LRU map 4.10 / lpm trie 4.11 / ringbuf 5.8 / BTF 5.2。实施前先在节点上 `uname -r` 确认；3.10 内核无 clsact，方案不成立；4.11–5.7 可行但 ringbuf 需换 perf event array、lpm 需换 hash 分桶；5.8+ 全功能。

## 8. 已知限制

| 问题 | 说明与对策 |
|---|---|
| 跨节点SVC源身份退化 | kube-proxy 对跨节点流量做 SNAT，后端看到的源是节点IP，策略只能按节点身份放行。一期接受；后续可将服务NAT移入eBPF（参照Cilium），身份全程保留 |
| hostNetwork后端 | DNAT后目的为节点IP，流量走宿主机协议栈，无挂载点。按"节点身份"处理 |
| 入向无法整形 | 策略只能丢弃不能排队（所有入向策略的通性） |
| 带宽共享 | 单ENI带宽由挂在其上的pod共享，本方案只管连通性不管资源分配，per-pod限速走slave qdisc或EDT |

## 9. 与独占ENI模式的关系

两种模式流量路径不同，策略执行层是两套：

| | 独占 ENI（安全组） | 共享 ENI（本方案） |
|---|---|---|
| 执行位置 | VPC 云层面 | 节点上 |
| 现状 | 每张ENI创建时挂安全组（podeni_controller） | 需本方案新增 |
| 抗篡改 | 强：包到网卡才过安全组 | 分层：H1–H3不可篡改，H4靠reconcile |
| 语义 | 只有allow白名单，表达不了deny | NetworkPolicy完整语义 |
| 生效速度 | 走VPC API，秒级到十秒级 | map更新，毫秒级 |
| VPC调用压力 | 每次策略变更打VPC接口 | 零 |

若平台要求两个模式提供统一的 NetworkPolicy API，则为一个 API、两个适配器：独占模式翻译成安全组规则（慢、有损、有配额），共享模式翻译成 BPF map 条目（快、全语义、零VPC调用）。

## 10. 分期实施

1. **一期**：pod_ip / slave_src / policy(CIDR粒度) + H1/H2/H4挂载——管住南北向和防伪造
2. **二期**：NetworkPolicy 完整语义（标签选择、协议端口）+ H3，补齐同ENI东西向和svc路径
3. **三期**：ct 有状态补全、策略命中 metrics、跨节点SVC真实源还原
