# Lira 自研 CNI 面试准备手册

> 目标岗位：智算云 PaaS 平台架构师（凸显 CNI + GPU 调度）
> 本手册基于 lira 代码真实实现整理，所有"你自己的数字"处需要你实测/回忆填入。

---

## 0. 一句话定位（30 秒电梯版，必须背熟）

> "我在公司私有云上自研过一个 CNI，叫 lira，做的是 **Pod 直通 VPC 网络**——每个 Pod 独占一张弹性网卡（ENI），拿到的是 VPC 原生 IP，没有 overlay、没有 NAT，安全组可以直接下到 Pod 粒度。整体是**薄 CNI 插件 + 节点 daemon + 集中式 controller** 的三段式架构，用 PodENI CRD 做云资源和 Pod 之间的协调载体，并且把节点的 ENI 配额上报成 Kubernetes 扩展资源，让调度器在调度阶段就能感知网卡余量。类似阿里云 Terway 的独占 ENI 模式，针对的是我们自研云平台的 OpenAPI。"

这段话覆盖了：自研动机、网络模式、架构分层、CRD 扩展能力、调度联动、对标物。面试官任何一条都能往下钻，而你已经把框架立住了。

---

## 1. 讲故事的三层结构（5 分钟展开版提纲）

面试官说"讲讲你这个 CNI"时，按 **动机 → 架构 → 一条 ADD 全链路 → 设计决策 → 效果与反思** 五步讲：

### 第一步：动机（为什么自研，30 秒）
- 业务背景：金融私有云，Pod 需要和 VPC 内云主机、中间件、数据库直接互访，overlay 方案（VXLAN）有封包开销且排查困难，NAT 方案丢失 Pod 源 IP 无法做细粒度安全管控。
- 云平台是自研的（有 VPC/ENI OpenAPI），具备做直通网络的条件。
- 结论：做 **ENI 直通（passthrough）** 模式的 CNI，对标 Terway 独占 ENI 模式。

### 第二步：架构（画图，1 分钟）
```
                    ┌─────────────────────────────────────────────────┐
                    │                  控制平面（集中式）                │
                    │   ┌─────────────────────────────────────────┐   │
   watch Pod ──────►│   │  lira-controller (controller-runtime)    │   │
                    │   │   ├─ pod controller：watch 已调度的 Pod    │   │
                    │   │   │    └─ OpenAPI AllocateENI → 建 PodENI │   │
                    │   │   └─ podeni controller：状态机             │   │
                    │   │        Initial → AttachENI → Bind → 删除   │   │
                    │   └─────────────────────────────────────────┘   │
                    │   ┌─────────────────────────────────────────┐   │
                    │   │  lira-resource：Node 扩展资源上报         │   │
                    │   │   msxf.com/eni → capacity / allocatable  │   │
                    │   └─────────────────────────────────────────┘   │
                    └──────────────────┬──────────────────────────────┘
                                       │ 云平台 OpenAPI（ENI 增删/挂载/卸载）
                                       ▼
┌───────────────────────────── Node (ECS) ─────────────────────────────┐
│  kubelet ──调用──► CNI 插件 lira（二进制，stdin 传 netconf）            │
│                        │ gRPC over unix socket (/var/run/lira/)      │
│                        ▼                                              │
│                     lira-daemon (DaemonSet)                           │
│                        │ 读 PodENI CR，等 Phase=Bind                   │
│                        ▼                                              │
│  数据面：  ENI(eth1) ──move──► Pod netns（直通，VPC IP）                │
│            veth 对：host 端 ◄────► Pod 内 veth1（承载 Service 流量）    │
│            host：169.254.1.1 + /32 回程路由 + 永久 ARP                  │
└──────────────────────────────────────────────────────────────────────┘
```
组件职责一句话版：
- **lira（CNI 插件）**：只做数据面。调 daemon 拿 IP 配置 → 把 ENI 移入 Pod netns、配 IP/路由 → 建 Service 用 veth → 回写 Pod event。
- **lira-daemon**：节点常驻，gRPC server，从 informer cache 读 PodENI，等 Bind 后返回 IP/MAC/网关。
- **lira-controller**：watch Pod，调云 OpenAPI 创建 ENI、挂载到 ECS；用 PodENI CR 状态机驱动，finalizer 保证云资源回收。
- **lira-resource**：把 ENI 配额 patch 成 Node 扩展资源 `msxf.com/eni`，按实际占用扣减 allocatable → **调度器预检**。

### 第三步：一条 ADD 全链路（2 分钟，最能体现深度，必须滚瓜烂熟）
1. Pod 调度到 Node → controller 的 **pod controller** watch 到（过滤条件：NodeName 非空、非 hostNetwork）。
2. 调云 OpenAPI **AllocateENI**（指定 VPC/Subnet/安全组）→ 拿到 ENI ID/IP/MAC → 创建 **PodENI CR**（带 finalizer、pod-uid annotation）。
3. **podeni controller** reconcile：Phase=Initial → 调 **AttachENI**（热插到 ECS）→ 更新 Phase=**Bind**。
4. kubelet 起 sandbox → 调 **CNI ADD**（stdin 传 netconf，CNI_ARGS 传 Pod 名/namespace/infra container id）。
5. 插件通过 **unix socket gRPC** 调 daemon 的 AllocIP；daemon 读 PodENI，**指数退避**等 Phase=Bind，返回 IP/MAC/PodCIDR/网关。
6. 插件**按 MAC 反查主机上 ENI 的 ifindex**（Attach 是异步的，网卡已出现在主机上）→ `LinkSetNsFd` 把 ENI 移入 Pod netns → 改名 eth0 → 配 IP、默认路由（gw=VPC 网关）、PodCIDR 路由。
7. 建 **veth 对**（Pod 侧接口名 **veth1**，host 侧 `lira`+pod名 hash）：veth1 **不配 IP**，纯 Service 导流口（代码里地址段被注释，passthrough_eni_linux.go:239）——配 169.254.1.1/32 scope link + Service CIDR via 169.254.1.1（onlink）+ 节点 IP/32 via 169.254.1.1，加对 169.254.1.1 的**永久 ARP**；host 侧配 169.254.1.1/32 + 回 Pod IP 的 /32 路由 + **PodIP→podVethMac 永久 ARP**（永久 ARP 两侧都写死；两侧 veth 的 MAC 都是插件 GenerateMac 自生成——"generate mac by self, do not wait veth mac ready"，所以表项内容在创建 veth 之前就确定）。
8. 回写 Pod event（AllocIPSucceed + 耗时），输出 CNI Result 给 kubelet。

> 讲的时候点出两个"为什么"：为什么按 MAC 找网卡（插件和 controller 解耦，唯一可靠关联键是 MAC）；为什么要 veth（见设计决策 4）。

DEL 链路（反向，30 秒）：
删 Pod → CNI DEL 清 host 侧 veth/路由 → pod controller 检测 sandbox 退出 → PodENI Phase=Deleting → podeni controller **DetachENI → DeleteENI → 去 finalizer** → CR 删除，云资源回收。

### 第四步：关键设计决策（每条都能单独被追问，见第 2 节追问库）

### 第五步：效果与反思（数据 + 主动讲弱点）
- 自验环境跑通：Pod 拿 VPC IP、Pod↔VPC 云主机互访、ClusterIP/NodePort 正常、Pod 重建 IP 正确回收、controller 重启后 reconcile 恢复。
- 主动讲演进方向（见第 4 节）——架构师面试"反思"比"功能多"更加分。

---

## 2. 高频追问库（按主题，★ 是几乎必问）

### 2.1 CNI 基础
- ★ **CNI 的调用模型是什么？** kubelet 按容器运行时的 CRI 请求，fork `/opt/cni/bin/lira` 进程，stdin 传 JSON 配置（cniVersion/name/plugins），环境变量 CNI_COMMAND=ADD/DEL/CHECK、CNI_CONTAINERID、CNI_NETNS、CNI_IFNAME、CNI_PATH；K8s 特有参数（Pod 名等）走 CNI_ARGS 或 config 里 kubeconfig 段。插件执行完往 stdout 打 Result（1.0 用 types/100 结构）。插件是无状态短命进程——**这就是为什么重逻辑要放 daemon**。
- ★ **你插件和 daemon 之间为什么用 gRPC over unix socket？** 插件每次 fork 都要新建进程，不能持有 informer cache 和长连接；daemon 常驻持有 K8s 缓存。unix socket 有文件权限（我们 0600）天然做访问控制，比 TCP 简单安全。 daemon 用文件锁防多实例。
- CHECK 实现了吗？→ 老实说早期版本是空实现（netns 配置静态校验价值有限），后续计划补链路级校验（IP 是否在、路由是否存在、veth 对端是否存活）。主动说反而加分。

### 2.2 数据面细节（体现"真做过"）
- ★ **为什么 Pod 里要双接口（ENI + veth1）？** ENI 直通后 Pod 的默认路由走 VPC 网关，但 VPC 网关不认识 ClusterIP——访问 Service 的包出 ENI 就被丢。解法：Service CIDR 单独路由到 veth1 → 169.254.1.1（host veth）→ 交给 host 上的 kube-proxy（iptables/IPVS）做 Service 转换。VPC 流量走 ENI 保持高性能与原生 IP，Service 流量借道 host。这是 Calico（169.254.1.1 方案）/Terway 的成熟做法。（追问"veth1 配什么 IP"：**不配 IP**——Pod IP 唯一归属 eth0，两个接口配同 IP 会有 ARP 二义性；veth1 只做导流口。）
- ★ **169.254.1.1 是什么？为什么要永久 ARP（NUD_PERMANENT）？** link-local 假网关，host veth 端配了这个地址。配永久静态 ARP 表项是为了避免每次发包都 ARP 广播等待解析——降低首包延迟，也避免某些场景 ARP 不通。Calico 同款。
- **host 侧 169.254.1.1/32 和回程 /32 路由的作用？** host 上 veth 对端只需要一个锚点地址；/32 scope link 路由保证 host → Pod 的回包/探活流量能精准送达（不依赖子网广播域，直通网络里 Pod 不在 host 的 L2 里）。
- **ENI 怎么"移"进 Pod netns？** `netlink LinkSetNsFd`，把主机 netns 的 fd 传给 setns 系统调用把网卡搬家；之后在 Pod netns 里 rename + up + 配地址路由。整个过程插件的 goroutine 需要锁定 OS 线程做 netns 切换（`runtime.LockOSThread` + containernetworking/plugins 的 ns 包）。
- **失败回滚？** AllocIP 拿到配置但数据面装配置失败时，插件反向调 daemon 的 ReleaseIP，防止云侧资源悬挂；attach 中的网卡搬回 host netns 的 defer 兜底逻辑也有。
- **限速/NetworkPolicy？** 当前版本没做。可以讲思路：限速走 TC 的 HTB/fq + netlink，或 eBPF；NetworkPolicy 在直通模式下 host 上用 iptables 规则按 Pod IP 做，或集成 Cilium/eBPF。

### 2.3 控制面与 CRD（体现 Operator 能力，对应 JD 第 3 条）
- ★ **为什么用 CRD（PodENI）而不是直接在 daemon 里调 OpenAPI？** 三点：①云 API 是异步且可能失败，需要可重入的协调——CR + reconcile 状态机天然幂等、失败重试、断点续做；②把云资源拓扑持久化，controller 重启后能从 CR 状态继续，不会重复创建 ENI；③可观测：kubectl get podeni 直接看 IP/Node/Phase，还能配 printcolumn。这个思路和阿里 Terway 的 PodENI、Sparknet(hybridnet) 一致。
- ★ **状态机怎么设计的？** Phase：Initial（ENI 已创建）→ Bind（已 attach 到 ECS）→ Deleting。Pod 侧触发 Deleting，podeni controller 负责 Detach → Delete → 摘 finalizer。
- ★ **Finalizer 解决什么问题？** 防止云资源泄漏：CR 有 finalizer 时 k8s 只标记 DeletionTimestamp 不真删，controller 必须先把云上 ENI 卸载+删除，才移除 finalizer 让 CR 消失。对错误码做幂等兜底（"请求资源不存在"/"此网卡未绑定设备"视为已删除，直接放行）。
- **并发与竞态怎么处理？** MaxConcurrentReconciles=2；PodENI 名称=Pod 名称一对一，Pod 重建时比对 pod-uid annotation，不同则删旧 CR 重建；Allocation 时如果 CR 已存在且 phase=Bind 且 UID 相同直接复用。所有 status 更新先 DeepCopy 再 Update，冲突则 reconcile 重试。
- **leader election？** controller manager 开了 leader election（lease），多副本部署只有一个 active。
- **回调环？** pod controller 只写 CR，podeni controller 只管 ENI 生命周期，daemon 只读——单向依赖，避免控制环互踢。

### 2.4 调度联动（★ 你的差异化亮点 + GPU 桥梁，见第 5 节）
- ★ **ENI 配额怎么让调度器感知？** 机型决定节点能挂几张 ENI。lira-resource 把配额 patch 进 `node.status.capacity["msxf.com/eni"]`，再按 PodENI 实际占用扣减 allocatable。kube-scheduler 对扩展资源有原生支持，allocatable 打满即 Filter 阶段过滤掉该节点 → **Pod 不会调度过去才发现网卡不够**。
- **为什么不用 webhook 或者默认调度器打分？** 扩展资源是 K8s 原生机制，零侵入；webhook 校验是准入时点，改 Pod 已经来不及。这正是 NVIDIA device plugin 上报 `nvidia.com/gpu` 的同一套机制。

### 2.5 对比题
- ★ **为什么不直接用 Terway/Calico/Flannel？** Flannel/VXLAN 是 overlay，排查难、有封包开销；Calico IPIP/BGP 在我们的网络环境（无法跑 BGP underlay）不可行；Terway 深度绑定阿里云 OpenAPI，我们平台是自研云，API 协议/认证体系（租户、云账号、requestId 透传）完全不同。核心论点：**CNI 的差异本质在 IPAM 与云 API 的集成层，数据面插件是通的**。
- **和 Cilium 比？** Cilium 强在 eBPF 数据面（NetworkPolicy/可观测/服务网格加速），但我们第一优先级是"VPC 原生 IP 直通"，且自研云 API 集成必须自己做，二者不冲突，后续可以组合。
- **和 Spiderpool/underlay 类社区方案比？** Spiderpool 解决的是裸金属/underlay 场景的 IP 池管理，我们是云平台 ENI 生命周期直通，ENI 的创建/挂载必须走云 API，这是本质差异。

### 2.6 故障场景题（架构师必考）
- ★ **Pod 起来了但没 IP，怎么排查？** 链路分层法：①kubectl describe pod 看 event（lira 会回写 AllocIPFailed/AllocIPSucceed + 耗时）→ ②没有 event 说明 CNI 没被调或 daemon 不通：查节点 kubelet 日志、lira.socket 是否存在、daemon 是否活 → ③CR 层面：kubectl get podeni，Phase 停在哪？Initial=Attach 失败（查云 API/配额），Bind 但没 IP=数据面问题（MAC 反查 ifindex 失败/Attach 异步延迟）→ ④云控制台对账 ENI 是否存在、是否挂到对的 ECS。
- **CR 删了但云上 ENI 还在（孤儿资源）？** 有 finalizer 正常不会发生；若 controller 在 Detach 与 Delete 之间崩溃重试仍会被 reconcile 补齐。终极兜底是**对账任务**：定时 list 云上 ENI 与 CR 比对，删除无主的——这是当前版本的演进点，主动说出来。
- **节点重启后网卡会乱吗？** Pod netns 随容器销毁，host 上 ENI 重新可见；CR 还在（Phase=Bind），新 Pod 的 AllocIP 会重新等 Bind；存量 ENI 由云平台挂载关系保证。可补讲：启动时做一次 host 网卡与 CR 的对账更稳。
- **ENI attach 是异步的，竞态怎么处理？** 插件端指数退避（10s 起步、factor 2、5 步）等 Bind；插件侧 120s 超时保护。**主动指出现状 bug 级细节**：退避总时长上限(约 310s) > CNI 120s 超时，参数需对齐——能指出自己系统的边界问题是强信号。

### 2.7 规模与性能
- **瓶颈在哪？** ①云 OpenAPI 的创建/attach 延迟（秒级）是冷启动主要耗时 → 池化预热；②controller 全量 list 节点每秒 patch → 改 informer event 驱动；③数据面本身 netlink 操作是毫秒级，不是瓶颈。
- **你测过什么数字？** （填你的真实数据）建议准备：单 Pod ADD 端到端耗时、纯数据面耗时、并发创建 N 个 Pod 的表现。

---

## 3. 需要你提前准备的"自己的数字"（面试前实测填空）

| 指标 | 说明 | 你的数 |
|---|---|---|
| ADD 端到端 | CNI ADD 从调用到返回（含等 Bind） | ___ |
| 纯数据面耗时 | 拿到 IP 后装网卡+路由+veth | ___ |
| ENI attach 云侧延迟 | AllocateENI→AttachENI 完成 | ___ |
| 集群规模（自验） | 节点数 / Pod 数 | ___ |
| 单节点 ENI 上限 | 你测试机型的配额 | ___ |

> 没测过的不要编精确数，说量级即可（"秒级，主要耗时在云 API""数据面毫秒级"），并主动说"这是自验环境，生产规模数据我愿意现场推导瓶颈模型"。

---

## 4. 主动讲弱点与演进路线（架构师加分项，准备 3 条）

1. **冷启动优化：ENI 池化预热**。当前 Pod 创建后才 AllocateENI + Attach（秒级）。演进：节点初始化时预创建并 attach 一批 ENI 成"资源池"，Pod 到来即绑定（毫秒级），低峰回收；类似 Terway 的 ENI/IPOVRD 池化模式。再进一步用 **secondary IP 模式**（一张 ENI 配多个辅助 IP）把配额利用率提升一个量级。
2. **对账与自愈**：定时对账云上 ENI ↔ PodENI CR，回收孤儿资源；节点启动时对账 host 网卡；补 CHECK 命令做数据面巡检。
3. **工程健壮性**：lira-resource 目前 1s 全量 list → 改 SharedInformer 事件驱动；退避参数与 CNI 超时对齐；补限速（TC）与 NetworkPolicy；多网卡（多 ENI 注入）支持 AI 场景（数据面/存储面分离）。

---

## 5. GPU 调度叙事（从 lira 自然桥接）

lira 没有 GPU 代码，**不要硬扯**。桥接逻辑：

> "我在 CNI 里做 ENI 扩展资源上报和调度预检（msxf.com/eni），这套机制和 NVIDIA device plugin 上报 nvidia.com/gpu 完全同构——都是 device plugin/扩展资源 → node allocatable → scheduler Filter。所以我理解 GPU 调度的骨架，区别在于 GPU 的复杂度在共享、拓扑和 gang 语义上。"

然后展示对 GPU 调度体系的分层认知（按面试深浅取舍）：

1. **资源上报层**：device plugin（gRPC 上报 + PodResources API 供 kubelet 分配）→ 新方向 **DRA（Dynamic Resource Allocation，KEP-4381）**：ResourceSlice/ResourceClaim/ResourceClaimTemplate，从"整数个 GPU"走向带属性（型号/显存/拓扑）的资源模型。
2. **共享策略**：vGPU（软件切分/厂商 MPT）、MIG（硬件隔离，A100+，需调度感知 MIG 拓扑）、时间片（默认 compute mode）、显存虚拟化。对应产品：阿里 gpushare/内核级 cGPU、腾讯 qGPU、火山 GPU 分享、社区 **HAMi**（显存硬限制+均衡调度）。
3. **调度器扩展**：scheduler framework 四个插件点——Filter（显存/型号过滤）、Score（bin-packing vs spread 策略）、Reserve（锁卡）、PostBind；**拓扑感知**：NVLink/PCIe 亲和，多卡 Pod 优先同机、训练场景带宽敏感；**gang scheduling**：分布式训练 all-or-nothing，用 Volcano/coscheduling 插件防止部分 Pod 先起死锁资源。
4. **多租户与队列**：配额、优先级、抢占、公平性（Volcano queue、Kueue）。
5. **推理平台特有**：负载感知调度（不只用 GPU 利用率）、显存感知 autoscaler、推理网关路由（_kv cache 感知_）。

准备一个你真实的 GPU 相关经历（哪怕是调研/POC/方案设计）按这个框架讲。若没有实操，就明说"生产实操以 CNI 为主，GPU 调度我做过体系化调研/POC"，然后讲第 3 条 scheduler framework 讲透——**深度比广度可信**。

---

## 6. JD 逐条映射（面试对表）

| JD 要求 | 你的应答素材 |
|---|---|
| 2. 主流容器网络原理 + 自研 CNI 经验 | lira 全链路（本手册） |
| 3. 阅读源码 + Operator/CRD/Controller/Webhook | lira 就是 controller-runtime 写的；讲 reconcile 幂等、finalizer、leader election、informer cache；Webhook 主动讲思路（Pod 准入校验/IP 扩展） |
| 1/4. 大规模生产经验、性能/高可用/升级 | 填你的真实集群经历；用 2.6/2.7 的瓶颈模型展示方法论 |
| 6. GPU Kubernetes / AI Infra | 第 5 节框架 + 你的真实经历 |
| 5. 参与/研究开源项目 | 提你研究过 Terway/Calico/Cilium 源码的具体点（如 Terway 的 PodENI 设计、Calico 169.254.1.1） |
| 7. 竞品分析 | 2.5 对比题即答案，补 OpenShift OVN-K8s/Rancher 的网络栈一句话认知 |

---

## 7. 面试前 Checklist

- [ ] 默画架构图（第 1 节）纸上能画出来
- [ ] ADD 八步时序口述 2 分钟内完成
- [ ] 背熟 30 秒电梯版
- [ ] 表 3 的数字实测/回忆填空
- [ ] 准备 1 个真实排查故事（最好网络相关）
- [ ] 第 4 节演进路线 3 条能脱稿讲
- [ ] GPU 框架第 5 节 + 你的真实 GPU 经历定位清楚
- [ ] 反问环节准备 2 个问题（如：平台当前网络方案痛点、GPU 池化路线 vGPU/MIG/DRA 的取舍）
