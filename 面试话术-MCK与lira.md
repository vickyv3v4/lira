# MCK + lira 面试话术（计时口述稿）

> 用法：本文件是**逐字稿**，细节出处一律回 interview-prep-mck.md / interview-prep-cni.md（均带 文件:行号）。
> 口径纪律（一票否决）：MCK / lira = 真实经历；GPU 智算方案 = "设计 + 源码研究，未上线"；不知道就说不知道。
> 待填数字集中在 §6，没测过的不编精确数，说量级。

---

## 0. 开场怎么接

- 被问「讲讲你的经历 / 做过什么平台」→ 先上 **MCK 主菜①**，讲完主动补一句桥接："平台侧讲完了，网络侧我更有体感——lira 是我自己写的 CNI"，自然进主菜②。
- 被问「讲个最有技术深度的东西」→ 直接上 **lira 主菜②**，讲完用 §5 桥接句接 GPU。
- 两个 30 秒电梯版（必须背熟）在两本手册 §0，此处不重复；逐字稿开头 15 秒已内置电梯版压缩句。

---

## 1. 主菜① MCK 创建集群五阶段（目标 3 分钟，逐字稿）

**（0:00–0:15 定位）**
"MCK 是我们公司私有云的托管 Kubernetes 产品，做的事是**把租户订单变成一台台真实可用的 K8s 集群，再管它一辈子的扩容、缩容、升级、停启、删除**。四层架构：portal 做订单履约编排和主数据，res 是无状态资源网关、唯一持有各集群 kubeconfig，ctl 是 kubebuilder Operator，底层内嵌完整 kubespray 真装集群。最核心的设计一句话：**集群的每一次变更都是一条 ClusterOperation CR 加一个跑 ansible-playbook 的 Job**——创建、扩缩容、删除、升级全部被 playbook 白名单收敛到同一条声明式管道。我按创建这条最复杂的链路讲，分五个阶段。"

**（0:15–0:45 阶段A 下单校验 + 预生成）**
"第一阶段是下单校验加预生成。校验网段重叠、规格、镜像白名单；然后加分布式锁过一个幂等闸门——同一订单绝不重复预生成；服务端预生成 clusterId，落任务树：一条主任务加 N 条建 ECS 的子任务，node、volume 数据行全部落库，ECS 预创建，还上报资源中心建立引用关系。**为什么这么设计？这等于把'等支付'拆成了两阶段提交**：下单阶段就把 ID、数据、资源预占全准备好，支付成功后零参数续跑。"

**（0:45–1:05 阶段B 支付成功）**
"第二阶段支付成功，Kafka 事件驱动。portal 落集群主记录，状态 initial；namespace 定成 mck-租户ID——这既是 CR 的 namespace 也是租户隔离边界；登录密码 KMS 加密落库；然后正式创建 ECS，带重试注解。"

**（1:05–1:45 阶段C ECS 就绪 → 生成两个 ConfigMap）**
"第三阶段，每台 ECS 就绪事件回来，portal 给它建 DNAT——manage_ip 就是元集群 SSH 到这台 ECS 的管理口，后面 ansible 全靠它连节点。全部就绪后锁内外双查，集群置 creating，然后用模板生成**两个 ConfigMap**：一个就是 kubespray 的 inventory hosts.yml，一个是 group_vars.yml，接着调 res。**这里是我认为整个架构最漂亮的收口：portal 和 kubespray 之间的接口就是两个 ConfigMap**——portal 负责'把订单翻译成 ansible inventory'，ctl 负责'把 inventory 塞进容器跑 playbook'，两个团队用一个 K8s 原生对象解耦，谁都不用懂对方的内部实现。"

**（1:45–2:10 阶段D res 落 CR）**
"第四阶段 res 做四件事，顺序很重要：建业务 namespace；给 Operator 建 ServiceAccount 并绑进 ClusterRoleBinding——**先给 Operator 铺好执行身份，CR 才有意义**；建那两个 ConfigMap；最后用 dynamic client 写 Cluster 和 ClusterOperation 两个 CR。"

**（2:10–3:00 阶段E ctl 调谐 → kubespray 装机 → 回流）**
"第五阶段 Operator 调谐，三个关键动作。第一，**防双跑**：第一次发现 Job 下有 running Pod，就把 Pod 名写进注解做锚定；之后任何一轮 reconcile 发现另一个 running Pod，就把 Job suspend 掉——**宁可失败，绝不并行双跑**，因为 ansible-playbook 重复执行会重复 kubeadm init，是灾难。第二，把 hosts/vars 复制成带毫秒时间戳的快照——每次操作一份独立基线，扩缩容失败不污染母本。第三，渲染 entrypoint.sh、建 Job 跑 cluster.yml，每 5 秒轮询 Job conditions 回写 status。装完 ctl 回写 Succeeded，Cluster 置 running，回调 portal，portal 部署 categraf 监控插件、推云监控计费。全程还有兜底任务扫中间态做修复。整条链路 4 个仓库、3 个 Kafka topic、2 个事件回环，我是全链路打通的角色。"

> 每阶段的"为什么"已嵌在稿里；再被钻一层 → 回手册 §2 原文（幂等闸门 checkOrderNotInTask / 状态机 / ScaleFailed→running）。

---

## 2. 主菜② lira CNI ADD 八步（目标 2 分钟，逐字稿）

**（0:00–0:15 定位）**
"lira 是我在公司自研云上写的 CNI，ENI 直通模式：**每个 Pod 独占一张弹性网卡，拿 VPC 原生 IP，没有 overlay、没有 NAT，安全组能下到 Pod 粒度**。架构三段式：薄 CNI 插件只做数据面、节点 daemon 常驻持有 K8s 缓存、集中式 controller 管云资源生命周期，中间用 PodENI CRD 做协调载体。我按一条 ADD 讲，八步。"

**（0:15–1:35 八步）**
"一，Pod 调度到节点，controller 的 pod controller watch 到，过滤条件是节点名非空、非 hostNetwork。二，调云 OpenAPI AllocateENI 创建弹性网卡，同时建 PodENI CR，带 finalizer 和 pod-uid 注解。三，podeni controller 把状态从 Initial 推到 Bind——这一步做的是 ENI 热插到 ECS。四，kubelet 起 sandbox，调 CNI ADD。五，插件通过 unix socket gRPC 调 daemon 拿 IP 配置，daemon 读 CR 指数退避等 Bind，返回 IP、MAC、网关。六，**插件按 MAC 反查主机上网卡的 ifindex——因为 attach 是异步的，插件和 controller 之间唯一可靠的关联键就是 MAC**——然后把 ENI 移进 Pod netns、改名 eth0、配 IP 和默认路由。七，建一对 veth：Pod 侧 veth1 承载 Service 流量，指向 169.254.1.1 这个 host 上的假网关，并配**永久 ARP** 避免首包 ARP 等待——两侧 veth 的 MAC 都是插件自己生成的，不等内核分配；host 侧配 169.254.1.1/32 加回 Pod 的 /32 回程路由，也写一条 PodIP 到 pod 侧 MAC 的永久 ARP。八，回写 Pod event 带耗时，输出 CNI Result 给 kubelet。"

**（1:35–2:00 两个"为什么" + DEL 收口）**
"两个为什么必答。**为什么双接口？** ENI 直通后默认路由走 VPC 网关，而 VPC 网关不认识 ClusterIP——Service 流量单独路由到 veth1 借道 host 上的 kube-proxy 做转换，VPC 流量走 ENI 保高性能和原生 IP。**为什么用 CRD？** 云 API 异步且会失败，CR 加 reconcile 状态机天然幂等可重入，controller 重启能从断点续走、不会重复建 ENI，kubectl get podeni 还能直接观测。删除链路就是反向：CNI DEL 清 veth → DetachENI → DeleteENI → 去 finalizer，**finalizer 保证云上资源不泄漏**。"

---

## 3. 高频追问快答（练到脱口而出）

### MCK

| 追问 | 快答 |
|---|---|
| 为什么 Operator + kubespray？ | 生命周期动作被 playbook 白名单收敛（entrypoint.go 是唯一真相源，没有 enum）；ansible 生态直接继承——离线源、国产 OS（enable-repo 支持 Kylin/openEuler）、踩坑产物（mount-xfs-pquota / config-insecure-registry）都是现成 playbook |
| 创建 20 分钟没出来怎么排查？ | 分层：portal task 表看子任务卡在哪（建 ECS / DNAT）→ res 侧 CR 是否落 → ctl 侧 Job 是否起来 → playbook 日志；兜底任务会扫中间态，子任务超时 10 分钟判失败 |
| reconcile 幂等怎么保证？ | 每一步都是"字段为空才做"的声明式守卫 + 确定性命名（`<ops>-job`）让"已存在则采纳"成为可能；防双跑用 actor Pod 锚定——Pod 名是 K8s 分配的唯一标识，写注解天然持久化，比内存锁/lease 轻 |
| 扩缩容失败集群什么状态？ | ScaleFailed → running，**有意为之**：集群本身健康，只是这次变更没成；失败留在 conditions 和 portal 任务里。缩容失败还有反向补偿——探测剩余节点不 Ready 就自动扩回来，宁可缩容失败不让客户集群残废 |
| 停止/重启为什么不走 res/ctl？ | ECS 停机即 K8s 不可用，纯资源开关不值得调谐；恢复后由 ctl 回调做健康确认——节点数核对 + 云监控查 apiserver up |
| 节点池是什么？ | 见 §4 |

### lira

| 追问 | 快答 |
|---|---|
| 插件和 daemon 为什么 unix socket gRPC？ | 插件是短命进程，每次 fork，不能持 informer cache 和长连接；daemon 常驻持有缓存。unix socket 文件权限（0600）天然访问控制，比 TCP 简单安全 |
| CHECK 实现了吗？ | 老实说：早期空实现（netns 静态校验价值有限），计划补链路级校验——IP 在不在、路由在不在、veth 对端活不活。主动说反而加分 |
| Pod 起来没 IP 怎么排查？ | 分层：describe pod 看 event（有耗时打点）→ 没 event 查 socket/daemon → 有 event 看 PodENI Phase：停 Initial = attach 问题查云 API/配额，Bind 但没 IP = 数据面问题（MAC 反查/attach 延迟）→ 云控制台对账 ENI |
| 调度怎么感知 ENI 配额？ | lira-resource 把配额 patch 成 Node 扩展资源 msxf.com/eni，按实际占用扣减 allocatable，scheduler Filter 原生过滤——Pod 不会调度过去才发现网卡不够 |
| 已知的边界问题？ | 退避总时长上限约 310s 大于 CNI 插件 120s 超时，参数没对齐——**主动指出自己系统的边界问题是强信号** |
| 为什么不用 Terway/Calico？ | Flannel/VXLAN overlay 排查难；Calico BGP 我们环境跑不了 underlay；Terway 绑死阿里云 OpenAPI，我们是自研云，协议/认证体系完全不同。核心论点：**CNI 的差异本质在 IPAM 与云 API 集成层，数据面插件是通的** |

---

## 4. 节点池问题专用话术（3 句版，防穿底）

> 背景：MCK 四仓库代码里**没有**节点池实体（已全文检索核实）；"节点池"出自产品讨论文档 §3.1 与智算设计。千万别把它和 poolCode 混为一谈。

1. "MCK 现网代码里没有独立的节点池实体——是**集群-节点两级**，节点直接挂集群，节点类型 ECS/BMS；代码里按 poolCode 分的是**元集群资源池**，那是管控面路由（决定成员集群由哪个元集群管），不是节点分组；节点靠 `mck.msxf.com/ecsId` 标签和 ECS 实例对账（node-label.yml）。"
2. "节点池是产品规划功能（产品文档 §3.1，'节点功能：节点池、标签、污点均适用'）。我的智算设计把它落为**驱动版本/卡型的管理载体**：一台节点同一时刻只能加载一个 NVIDIA 内核驱动，所以'同集群多版本'的载体只能是节点池——驱动版本是池属性，打 `gpu.model`/`gpu.driver` 标签。"
3. "落地完全复用现有管道、不改 MCK 内核：入池 = gpu-driver/gpu-runtime playbook 装驱动 + 打标（playbook 白名单机制不变）；调度 = 按 nodeSelector 选池；升级 = 新池灰度、禁止原地升级；云外节点纳管四步法（预检→注册入池→变更锁→切换自有）。"

---

## 5. lira 进阶追问：eBPF SVC 数据面（评审稿口径）

> 口径纪律：SVC/网络策略两份 eBPF 均为**设计评审稿，未实现**（共享 ENI 在 feature-multi-eniip 分支实现）。被钻到时按"设计+取舍"讲，不冒充已上线；完整依据见 SVC-eBPF实现方案-独占与共享ENI.md（数据流图在 §3.3）。

**3 句收口**：
"落地 = lira-daemon 里加 svc agent watch Service/EndpointSlice 增量写 map，数据面 3 个 TC 程序查 ct/svc4 做 DNAT，Go 不在转发路径上；"
"DaemonSet 复用 lira 现有骨架，增量是 CAP_BPF、bpffs 挂载（/sys/fs/bpf）和 endpointslices 权限，产物 bpf2go embed 进二进制；"
"交互两个方向——Go 经 cilium/ebpf 写 pin 在 /sys/fs/bpf 的 map，BPF 经 ringbuf 上报事件，attach/reconcile 由 daemon 兜底。"

**追问快答表**：

| 追问 | 快答 |
|---|---|
| 针对独占还是共享 ENI？ | 双模式统一设计：容器侧结构同构（业务口 eth0 + 宿主通信口 veth1，ServiceCIDR 都指 169.254.1.1）→ svc 流量必经宿主机 → 三个挂载点两模式通用；分叉只在 NodePort 覆盖面（共享可扩主口）和二期源保持还原落点（共享=主口入向 / 独占=pod 内 ENI） |
| eth0 和容器 ENI 什么关系？ | 不是一张卡：eth0=节点主网卡（SNAT 源），容器 ENI 是额外 Attach 的辅助网卡（独占=移进 pod netns 当 eth0；共享=宿主机 ENI 主口+ipvlan 子口）；一期 SNAT 回包统一回节点主网卡，还原程序不碰容器 ENI，二期才分叉 |
| 为什么 TC 不是 XDP？ | XDP 入向-only，覆盖不了出向和 host 起源流量（SVC §1.4） |
| 为什么不用 IPVS/Cilium？ | IPVS 仍在 netfilter 全路径+nf_conntrack、无法与策略共享 ct；Cilium 要接管 CNI 数据面与 ENI 直通/ipvlan 架构冲突——取其 map/ct 设计不引其栈（SVC §2/§13） |
| Go↔eBPF 怎么交互？ | user→kernel：cilium/ebpf 加载（bpf2go embed ELF）+ Map.Update/Delete 增量写，pin 在 /sys/fs/bpf/lira/ 与进程生命周期解耦；kernel→user：ringbuf 上报丢弃计数告警（<5.8 换 perf event array）【通用机制，仓库未实现】 |
| agent 挂了 svc 会断吗？ | 不会，map 是持久状态，数据面照常转发；仅变更停止生效，重启 resync 修漂移（SVC §13-Q4） |
| 内核版本红线？ | clsact 4.5 / LRU map 4.10 / ringbuf 5.8 / BTF 5.2；3.10 无 clsact 方案不成立；attach 失败该节点不上报就绪、维持 kube-proxy（灰度自然跳过，SVC §8） |
| 连接内后端会漂移吗？ | 不会，ct 记录已选后端，后续包命中 ct 直接复用；只有 ct 丢失才重选，此时 TCP 已断（SVC §13-Q7） |

**数据流图**（同 SVC 方案 §3.3；背图先背三句：①在 veth 因为 svc 流量必经宿主机；②在节点 eth0 因为一期 SNAT 回包必回主网卡；独占/共享差异只有 ENI 本体的 netns 位置）：

图 1：独占 ENI

```
                     VPC 网络 ═══════════════════════════════════════
                       ▲     │                                  ▲
              远端后端  │     │ 回包(dst=节点IP)                  │ 外部客户端
              出站流量  │     ▼ ②                               │ NodePort入向 ▼②
┌──────────────────────┴─────┴──────── 宿主机 netns ──────────────────────────┐
│                                                                            │
│  [节点主网卡 eth0]（节点IP，= SNAT 源）                                       │
│     ├─② tc-ingress 回包还原入口：ct 反查 → 还原 → redirect(lirayyyy) → pod1  │
│     │                 ct 未命中 → NodePort 新连接：svc4 → DNAT+SNAT          │
│     │                   ├─ 本地后端 → redirect   ├─ 远端 → 交路由             │
│     └─③ tc-egress host起源入口：host 发出的 ClusterIP 流量 DNAT（另挂 lo）     │
│                                                                            │
│  [lirayyyy host端]（请求 pod 宿主通信口，169.254.1.1/32 + 回 pod1 /32 路由）   │
│     └─① tc-ingress svc请求入口：ct → svc4 → 选后端 → DNAT+SNAT                │
│            ├─ 本地后端 → bpf_redirect(liraxxxx host端)                       │
│            └─ 远端后端 → TC_ACT_OK → 路由 → eth0 出 VPC                       │
│                                                                            │
│  [liraxxxx host端]（后端 pod 宿主通信口，本地投递落点）                        │
│                                                                            │
│  注：容器 ENI 不在宿主机 netns —— 独占模式 ENI 已移进 pod netns 当 eth0       │
└───────┬───────────────────────────────────┬────────────────────────────────┘
        │ veth（pod 侧 = veth1）             │ veth（pod 侧 = veth1）
┌───────┴──────────┐           ┌────────────┴──────┐
│ pod1 netns       │           │ pod2 netns        │
│ [veth1] 宿主通信 │           │ [veth1] 宿主通信  │
│   ServiceCIDR    │           │   ServiceCIDR     │
│   路由→169.254.1.1│          │   路由→169.254.1.1│
│ [eth0] = ENI 直通│           │ [eth0] = ENI 直通 │
│   VPC 原生 IP    │           │   后端 IP         │
│   默认路由→VPC网关│           │   默认路由→VPC网关│
└──────────────────┘           └───────────────────┘
```

图 2：共享 ENI（只画差异，①②③ 位置相同）

```
                     VPC ═══════════════════════════════════════
                       ▲    │                          ▲
                       │    ▼ ②                        ▼ ②
┌──────────────────────┴────┴──── 宿主机 netns ──────────────────────────────┐
│  [节点主网卡 eth0] ②还原/NodePort入向   ③host起源（另挂 lo） —— 同图 1       │
│                                                                           │
│  [lirayyyy host端] ①（同图 1）   [liraxxxx host端] 本地投递                 │
│                                                                           │
│  [ENI 主口] —— 共享模式 ENI 留在宿主机 netns（独占模式没有这个设备）           │
│     ├─ pod eth0 是它的 ipvlan 子口；出向包内核切到主口发出（内核已核对）       │
│     └─ 二期源保持：主口 tc-ingress 内 ct 反查还原（无新程序）                 │
│                                                                           │
│  注：同 ENI pod 互访走 ipvlan 二层直投、不进协议栈；但 svc 流量走 veth1，      │
│      不经该路径，① 两模式通用                                               │
└───────┬───────────────────────────────────┬────────────────────────────────┘
        │ veth（pod 侧 = veth1）             │ veth（pod 侧 = veth1）
┌───────┴──────────┐           ┌────────────┴──────┐
│ pod1 netns       │           │ pod2 netns        │
│ [veth1] 宿主通信 │           │ [veth1] 宿主通信  │
│   ServiceCIDR    │           │   ServiceCIDR     │
│   路由→169.254.1.1│          │   路由→169.254.1.1│
│ [eth0] = ipvlan  │           │ [eth0] = ipvlan   │
│   子口(secondary)│           │   子口（后端IP）   │
│   默认路由→VPC网关│           │   默认路由→VPC网关│
└──────────────────┘           └───────────────────┘
```

接口对照表 + "三行读图要点"见 SVC 方案 §3.3（rehearsal 时对照 §5.2 四条流程走一遍）。

---

## 6. 衔接与收口

- **MCK → lira**："平台管控侧讲完了，网络数据面我更有体感——lira 是我自己从零写的，2 分钟讲一条 ADD。"
- **lira → GPU（唯一合法桥接句）**："我在 CNI 里做 ENI 扩展资源上报和调度预检，这套机制和 NVIDIA device plugin 上报 nvidia.com/gpu **完全同构**——都是 device plugin/扩展资源 → node allocatable → scheduler Filter。所以我理解 GPU 调度的骨架，区别在共享、拓扑和 gang 语义。"（然后切 GPU 6 分钟版，按 checklist ③）
- **GPU 口径自锁**（被问生产经验时）："GPU 这块我要说清楚：是设计方案加源码研究，没上过生产——HAMi 2.6.1 源码我本地读过 bind 流程，方案里每个结论都标了出处。"
- **反问挑 2**：① 平台当前多集群规模与元集群瓶颈？GPU 池化路线 vGPU/MIG/DRA 的取舍？② 平台当前网络方案的主要痛点？

---

## 7. 待填数字清单（面试前实测/回忆）

| # | 指标 | 量级口径（没实测就说这个） | 你的数 |
|---|---|---|---|
| 1 | lira ADD 端到端耗时 | 秒级，主要耗时在云 API attach | ___ |
| 2 | lira 纯数据面耗时 | 毫秒级 | ___ |
| 3 | ENI attach 云侧延迟 | 秒级 | ___ |
| 4 | lira 自验规模 / 单节点 ENI 上限 | ___ / 机型配额 | ___ |
| 5 | MCK 真实集群/节点/pool 规模 | mck §9.5 | ___ |
| 6 | 故障故事 ×1（背景→现象→定位→根因→修复→预防，带 MTTR） | 真实优先，网络或集群生命周期 | ___ |

---

## 8. 经历型问答库：解决了什么问题 / 优化了什么性能

> 用法：§3/§5 回答"为什么这么设计"（机制型）；本节回答**经历四连**——你做了什么、解决了什么问题、优化了什么、效果如何（经历型问题必问且往往最先问）。统一答法骨架：**背景一句 → 动作 → 效果（量级/验证方法）→ 主动边界**。数字纪律同 §7：没实测说量级。

### 8.1 MCK

**Q1 "MCK 是团队做的，你具体负责什么？"**

"整体是**全链路打通**的角色：portal/res/ctl/plt 四个仓库、3 个 Kafka topic、2 个事件回环这条链上的设计取舍我能讲到代码行——比如 portal 和 kubespray 之间用两个 ConfigMap 做接口、ctl 的 actor 防双跑。网络侧 lira CNI 主体代码是我写的。"

（口径提醒：不虚构"某模块全是我写的"，用"能讲到代码行"证明参与深度；被问具体归属再如实分层。）

**Q2 "MCK 解决了什么问题？没有它之前怎么做？"**

"一句话：**把租户订单自动变成可用的 K8s 集群，并管它一辈子的扩缩容升级删除**。之前这类交付非声明式、失败要人守，国产 OS 的坑（离线源、xfs pquota、insecure registry）每个集群重踩一遍。MCK 之后每次变更都是一条 ClusterOperation CR + 白名单内 playbook：幂等可重入、失败原因自动回传（`mck.msxf.com/error` 注解里直接看到 ansible 原始报错）、状态 kubectl get cluster 可观测；踩坑沉淀成 playbook 资产，第二个集群就是零成本。"

（为什么 Operator 三点：分钟级长任务不能进 HTTP / 断点续做 / 状态可观测——mck §9.1。）

**Q3 "你做过什么优化？"（性能/效率题，两层答法）**

"两层。**第一层，履约时延**：把"等支付"拆成两阶段提交——下单时就预生成 clusterId、任务树、node/volume 数据、ECS 预创建，支付成功后**零参数续跑**，支付后的等待只剩真正装机。**第二层，正确性也是性能——不用返工的快才是真的快**：分布式锁加幂等闸门，同订单绝不重复预生成；ctl 用 actor Pod 锚定防双跑，宁可失败绝不并行（重复 kubeadm init 是灾难）；缩容失败有反向补偿，探测剩余节点不 Ready 自动扩回来。要说下一步我也准备好了：ctl 从轮询改事件驱动（现在每 ops 3s 无条件 requeue）、补云资源↔CR↔DB 三方对账回收孤儿资源。"

（预生成=话术 §1 阶段A；防双跑=mck §4.5；反向补偿=mck §9.4；演进=mck §10。）

**Q4 "瓶颈在哪？"**

"绝对瓶颈是 ansible 装集群本身——分钟级且**串行 nature**，一个 master 一个 master 来，这是 kubespray 的特性；其次 ctl 纯轮询在大集群数下产生大量无效 reconcile。所以优化思路不是造更快的安装器，而是**把等待前置消化**（预生成）+ **把失败快速暴露**（error 注解 + 兜底任务扫中间态）。"

（mck §9.5。）

### 8.2 lira

**Q1 "为什么要自研 CNI？解决了什么问题？"**

"场景是金融私有云自研云，要 **Pod 拿 VPC 原生 IP、与 VPC 互访、安全组下到 Pod 粒度**。现成方案三个都不合适：Flannel/VXLAN 是 overlay，封包开销加排查看不见对端；Calico 的 BGP underlay 我们网络环境跑不了；Terway 深度绑定阿里云 OpenAPI，我们是自研云、API 协议和认证体系完全不同。核心判断：**CNI 的差异本质在 IPAM 与云 API 集成层，数据面插件是通的**——自研就自研这一层，把云资源生命周期做成 K8s 原生体验。"

（cni §2.5；与 §3 快答"为什么不用 Terway/Calico"同一论点，此处展开成完整问题答法。）

**Q2 "做了哪些性能优化？"（分层口径：已实现 vs 设计稿，绝不混说）**

"分四层说。**已实现（自验跑通）**：一是 ENI 直通本身——去 overlay 封装解封装、无 NAT 转换，数据面只剩网卡；二是首包优化——169.254.1.1 假网关配**永久 ARP**（NUD_PERMANENT），消除首包 ARP 广播等待；三是 Service 流量单独路由 veth1 借道 host kube-proxy，保住 ENI 直通性能的同时 ClusterIP 不断。**调度侧**：ENI 配额 patch 成 Node 扩展资源 `msxf.com/eni` 让调度器 Filter 预检——节点满了 Pod 直接 Pending + FailedScheduling，不再 ContainerCreating 卡死到超时。**设计稿（评审稿，未上线）**：共享 ENI 把单节点密度从机型 ENI 上限提到 **ENI×9**（feature 分支已验证）；eBPF 方案把策略下发从 VPC API 秒级~十秒级变 map **毫秒级**、iptables 线性遍历变 map O(1)。**演进项（设计）**：ENI 池化预热，把冷启动的云 API 秒级延迟藏掉。"

（永久 ARP=cni §2.2；调度预检=cni §2.4；ENI×9=网络策略方案 §3；毫秒级=lira-full §4.4；池化=cni §4.1。）

**Q3 "自研过程中最难的问题是什么？"**

"三个真实的。**一，attach 异步的关联问题**：controller 热插网卡和插件装配置是两个时空，中间唯一可靠关联键是 MAC——按 MAC 反查 ifindex 是全链路最关键一步。**二，竞态**：Pod 重建要按 pod-uid 比对删旧建新；记账上 Initial 阶段云侧 ENI ID 还没回、统计口径漏账会超卖——这正是我后来写配额改造方案（Webhook + 记账重构）的动因。**三，参数 bug**：daemon 等 Bind 的指数退避总上限约 310s，CNI 插件 120s 就超时——参数没对齐，自己 review 出来的边界问题。"

（MAC 反查=cni §1/§2.2；竞态=lira-full §3.1；310s bug=cni §2.6。）

**Q4 "效果怎么样？有数字吗？"**

"自验环境跑通，量级口径：ADD 端到端**秒级**、主要耗时在云 API attach；纯数据面（拿到 IP 后装网卡路由）**毫秒级**。精确数没测过不编——推生产规模我现场给瓶颈模型：冷启动瓶颈在云 OpenAPI（所以演进是池化预热），稳态瓶颈不在数据面（netlink 操作毫秒级）。"

（cni §2.7/§3；"自验环境+瓶颈模型"这句就是数字纪律的标准出口。）

### 8.3 RDMA

**Q1 "RDMA 这块你具体做了什么？"（先分层再答，防冒进）**

"三层。**第一层，源码研读到代码级**：开源链路 sriov-network-device-plugin + multus + sriov-cni + whereabouts 我逐行读过——multus 的 deviceID 怎么从 PodResources API 取、sriov-cni ADD 六步、设备节点怎么挂——这层可以钻任意深度。**第二层，智算平台的网络设计是我负责**：一期用开源插件把 VF 注册成扩展资源先落地，演进设计 RdmaNic CRD，复用 lira 的"云资源→CR→状态机→调度感知"模式。**第三层划界**：RoCE/IB 交换机侧无损参数（PFC/ECN）是底座网络专项，我们方案只定义接口。"

（lira-full §5.2/§5.3/§5.4/§5.6。）

**Q2 "RDMA 部分解决了什么问题？"**

"问题定义：**把 RDMA 网卡变成 K8s 可调度的资源**——多机训练 NCCL 通信、PD 分离推理 KV 传输、GPUDirect RDMA 都挂在它上面。开源链路只解决"能用"，三件事解决不了：VF 账本只有数量没有粒度、netns 级 RDMA 设备隔离开源 master 没有（只有应用级）、NIC 故障没有隔离态管理。这三个缺口就是自研 RdmaNic 的动机——和 PodENI 完全同构，"配额前移调度器"这条路 lira 已趟过一遍。"

**Q2-深挖① ""可调度"具体怎么体现？别是个概念词"**（必被追问，背熟四步 + 三证据）

"四步链路：**①上报（节点有账本）**——sriov-network-device-plugin 按 `isRdma` 选择器发现 VF（Mellanox 读 `/sys/bus/pci/devices/<addr>/infiniband/` 拿 RDMA 设备名），ListAndWatch 把 VF 个数报给 kubelet，写进 `node.status.capacity["rdma/hca_shared"]`，再按已分配扣出 allocatable；**②请求（Pod 有语法）**——Pod 里 `resources.requests: rdma/hca_shared: 1` + nodeSelector 圈 RDMA 节点池——网卡从"主机预配置"变成 **Pod 的声明式需求**；**③调度（决策点在 Filter）**——NodeResourcesFit 插件原生支持扩展资源，requests > allocatable 直接过滤，失败事件就是 `Insufficient rdma/hca_shared`——Pod 不会调度过去才发现没网卡；**④分配（执行点在 Allocate）**——kubelet 调 device plugin Allocate 拿 DeviceSpec（`/dev/infiniband/uverbsX` 设备节点）+ PCIDEVICE_ 环境变量，multus 再按 PodResources API 的 deviceID 让 sriov-cni 把 VF 网卡搬进 Pod netns——设备节点和网卡同时到位。

可见证据三件套：`describe node` 的 allocatable 多一行 `rdma/hca_shared: N`；Pod yaml 里带着这个 request；节点打满报 `Insufficient rdma/hca_shared` 而不是 ContainerCreating 卡死。**这三样和 `nvidia.com/gpu`、`msxf.com/eni` 的表现一模一样——"同构"不是比喻，是同一套 K8s API**。"

**Q2-深挖② 三缺口各举一个具体场景**（防"缺口也是概念词"）

"**缺口一：VF 账本只有数量、没有粒度**。一期扩展资源解决的是"这个节点总共有几个 VF"；拓扑对齐开源其实也有（sriovdp 上报 NUMA 节点 + Topology Manager 钉同 NUMA）。真正缺的是 **VF 实体级账本**——哪个 VF 被哪个 Pod 占着、属于哪张 PF、什么状态：kubelet 按自身策略挑设备，Pod/调度器表达不了"指定/避开某个 VF 实体"。RdmaNic CR 的五个字段 `nodeName / pf / vfIndex / state / owner` 就是这本账。

**缺口二（面试必被反问"VF 本身不就隔离了吗"——先认同再分层）**。**数据面：对，VF 硬件隔离已经够了**——每 VF 独立 RDMA 设备实例（mlx5_N）+ IOMMU/PD 内存保护，QP/MR 资源私有，Pod 之间打不通对方内存，所以一期共享模式敢说"应用级隔离够用"。**shared 模式下没隔离住的是管理面三样**：①**枚举可见性**——容器 /sys 挂的是宿主机的，`ibv_devinfo` 能看到全节点 RDMA 设备列表（别人的 /dev 节点打不开，但设备存在性与拓扑看得到）；②**设备名不稳定**——mlx5_5 这类编号是宿主机按枚举顺序定的，应用要靠插件注入的 env（PCIDEVICE_*_INFO 里的 rdma_dev 名 + uverbs 路径）才知道用哪个设备；③**归属分家**——VF netdev 搬进了 Pod netns，ib_device 还留在宿主机 netns，管理语义不闭合。netns 级隔离（内核 RDMA exclusive 模式：设备随 netdev 一起 move 进 Pod netns——开关用法以内核版本文档为准）买到的是"只见自己 + 设备名确定 + 归属一致"，社区 sriov-cni master 没实现（`--grep rdma` 零 commit）、只有 fork/定制版做过。**定性要说准：这不是安全缺口，是多租户强合规与免配置场景的演进项——RdmaNic 的核心增益是 VF 账本粒度和故障隔离态，netns 隔离只是演进动机之一**。

**缺口三：故障无隔离态**。sriovdp 的 ListAndWatch Probe 未实现、设备列表静态——VF 坏了账本不更新，Pod 照样被调度到坏卡上。GPU 侧有成熟的掉卡时序（device plugin 健康摘除 = **拦新增、不杀存量**），RdmaNic 的 **Isolated 态**设计上就是把这套时序平移给网卡：daemon 探测故障 → 置 Isolated → 摘掉该 VF 可调度容量 → 存量 Pod 不动。"

（出处：四步链路=lira-full §5.2 五步口述 + Allocate 四字段 + mck-ai-platform-design §3.4；缺口一=§5.2 拓扑上报 + §5.4 CR 字段；缺口二=§5.2 追问"sriov-cni 没有 RDMA 代码"条；缺口三=§5.2 "Probe 未实现/设备列表静态" + GPU 掉卡时序 mck-gpu-platform-design §4.5.1。RdmaNic/Isolated 为设计稿口径。）

**Q3 "做了哪些优化？"（机制层面，无实测数字）**

"机制层面四层。**直通本身就是优化**：VF 独占、无 bridge 无 overlay，GPUDirect 让 NIC 与 GPU 直接 DMA 绕过 CPU；**拓扑对齐**：device plugin 上报每个 VF 的 NUMA 节点，Topology Manager 把 GPU/网卡/pinned memory 钉同一 NUMA——跨 NUMA 的注册内存访问是训练必须避免的代价；**收敛优化**：IP 是复用的，sriov-cni 在 UP 后主动发 GARP/Unsolicited NA，邻居收敛从"等 ARP 老化"变"立即通告"；**确定性分配**：whereabouts 按 IPPool CRD 记录让 Pod 重建拿回同 IP，减少上层重建。容器内 memlock/IPC_LOCK 是 pinned memory 注册的硬前提，列入节点初始化参数。收益数字不编——方案原则就是"不预设收益，以可复现实验为准"。"

（lira-full §5.2 链路优化四层；"不预设收益"=该文 §8 未确认项 4。）

**Q4 "没上线，怎么保证能落地？"**

"两条。**一期先开源后自研**：开源 k8s-rdma-shared-dev-plugin 把 VF 注册成 `rdma/hca_shared` 扩展资源，作业请求加 nodeSelector 选 RDMA 节点池就能跑 NCCL——这条路机制我解剖透了、风险最低；**POC 清单明确**：GPUDirect RDMA 需要的 IOMMU/vfio 主机配置是 POC 验证项。选型底气来自源码级认知——知道开源链路给什么（设备节点直挂）、不给什么（netns 隔离、按需分配），自研演进才有靶子。"

（lira-full §5.3/§5.6；GPU 方案同款答法见 checklist §3"没上线怎么保证落地"。）
