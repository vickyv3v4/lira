# Lira 全景面试话术 —— 主流程 / 配额与调度 / eBPF 网络策略 / RDMA for AI

> 目标岗位：智算云 PaaS 平台架构师（CNI + AI Infra 主线）
> 素材来源：本仓库 main 分支代码 + 五份方案材料（均标出处，可查证）
> 使用纪律：延续 interview-prep 系列——**已实现/已验证/方案稿分层讲，没测的数字不编，空白就说"方案层面设计了、这块没深入"**

---

## 0. 诚信分层表（开场先立住，全篇的定位框架）

| 模块 | 状态 | 素材出处 |
|---|---|---|
| 独占 ENI 三段式（CNI 插件 + daemon + controller + resource） | **代码已实现，自验跑通** | 本仓库 main：`cmd/lira*`、`pkg/controllers/`；interview-prep-cni.md |
| 共享 ENI（secondary IP + ipvlan 子口） | **分支实现**（feature-multi-eniip） | 《Lira共享ENI方案及实现》PDF；eBPF 方案文档头部引用 |
| 配额调度优化（MutatingWebhook + 记账改造） | **设计方案（草稿版）** | docs/PodENI-MutatingWebhook配额与调度优化方案.md |
| eBPF 网络策略（NetworkPolicy on 共享 ENI） | **设计方案（评审稿）** | 共享ENI-EBPF网络策略设计方案.md |
| Service 数据面 eBPF（替换 kube-proxy） | **设计方案（评审稿）** | SVC-eBPF实现方案-独占与共享ENI.md |
| RDMA 网络资源化（RdmaNic CRD / VF 插件） | **设计方案（未实施，演进项）** | mck-ai-platform-design.md §3.4；mck-gpu-platform-design.md |

**开场定位（一句话，主动说，把面试官的预期校准）：**

> "lira 是我在公司自研云上做的 CNI，主体代码我写完并自验跑通；共享 ENI 模式在功能分支上做过实现验证；配额调度优化和两份 eBPF 方案是我执笔的设计评审稿；RDMA 是我们智算平台方案里我负责的网络设计。已实现的部分可以往任何深度钻，方案部分我重点讲设计取舍和为什么这么选。"

---

## 1. 30 秒总电梯版（背熟，四块内容一网打尽）

> "我在公司私有云自研了 CNI 叫 lira，做 **Pod 直通 VPC 网络**：每个 Pod 独占一张弹性网卡，拿到 VPC 原生 IP，没有 overlay、没有 NAT，安全组直接下到 Pod 粒度。架构是**薄 CNI 插件 + 节点 daemon + 集中式 controller** 三段式，用 PodENI CRD 做云资源和 Pod 之间的协调载体，并把 ENI 配额上报成 K8s 扩展资源让调度器预检——和 device plugin 上报 GPU 完全同构。
>
> 围绕这条主线我做了三个延伸：**一是配额治理**，把 ENI 校验从'调度后控制器数数'前移成'调度器原生扩展资源过滤'，用 MutatingWebhook 给 Pod 注入 requests；**二是共享 ENI 模式**，一张 ENI 配多个 secondary IP 解决配额瓶颈，但 ipvlan 直投流量绕过协议栈、iptables 看不到，所以网络策略和 Service 数据面整体换成了 **eBPF**——tc 挂载点 + map 存策略，数据面只读 map、控制面只写 map；**三是把这套'云资源→CR→调度感知'的模式平移到 RDMA 网卡**，设计了 RdmaNic CRD，支撑 AI 训练的 GPUDirect RDMA 和 PD 分离推理的 KV 传输。"

每句话都能往下钻一层：网络模式→架构→配额→eBPF→AI，正好是本手册 §2–§5。

---

## 2. Lira 主要流程（已实现，可深钻）

### 2.1 架构口述（1 分钟，边说边画）

> "整体三段式。**CNI 插件 lira** 是 kubelet fork 的短命进程，只做数据面：调 daemon 拿 IP 配置，把 ENI 移进 Pod netns、配 IP 路由、建 Service 用 veth，回写 Pod event。**lira-daemon** 是节点常驻 DaemonSet，起 gRPC server over unix socket，从 informer cache 读 PodENI CR，等 Phase=Bind 才返回 IP/MAC/网关。**lira-controller** 是 controller-runtime 写的 Operator，watch 已调度的 Pod，调云 OpenAPI 创建 ENI、热插到 ECS，用 PodENI CR 状态机驱动，finalizer 保证云资源回收。**lira-resource** 把机型 ENI 配额 patch 成 Node 扩展资源 `msxf.com/eni`，按实际占用扣减 allocatable，调度器预检。"

（来源：interview-prep-cni.md §1 架构图）

### 2.2 ADD 全链路逐句稿（2 分钟，必须滚瓜烂熟）

> "1. Pod 调度到 Node，controller 的 pod controller watch 到——过滤条件是 NodeName 非空、非 hostNetwork。
> 2. 调云 OpenAPI **AllocateENI**，指定 VPC/子网/安全组，拿到 ENI ID/IP/MAC，创建 **PodENI CR**，带 finalizer 和 pod-uid annotation。
> 3. podeni controller reconcile：Phase=Initial，调 **AttachENI** 把网卡热插到 ECS，更新 Phase=**Bind**。
> 4. kubelet 起 sandbox，调 **CNI ADD**，stdin 传 netconf，CNI_ARGS 传 Pod 名/namespace。
> 5. 插件通过 unix socket 调 daemon 的 AllocIP，daemon 读 PodENI **指数退避等 Bind**，返回 IP/MAC/PodCIDR/网关。
> 6. 插件**按 MAC 反查主机上 ENI 的 ifindex**——因为 Attach 是异步的，网卡可能刚出现在主机上——然后 LinkSetNsFd 把 ENI 移入 Pod netns，改名 eth0，配 IP、默认路由走 VPC 网关、PodCIDR 路由。
> 7. 建 **veth 对**：Pod 侧 lira0 配同款 Pod IP，Service CIDR 路由 via 169.254.1.1，加对 169.254.1.1 的永久 ARP；host 侧配 169.254.1.1/32 加回 Pod IP 的 /32 路由。
> 8. 回写 Pod event（AllocIPSucceed 加耗时），输出 CNI Result 给 kubelet。"

（来源：interview-prep-cni.md §1 ADD 八步）

### 2.3 DEL 链路（30 秒）

> "删 Pod → CNI DEL 清 host 侧 veth 和路由 → pod controller 检测 sandbox 退出 → PodENI 置 Phase=Deleting → podeni controller **DetachENI → DeleteENI → 摘 finalizer** → CR 删除，云资源回收。finalizer 的意义就是防止 CR 删了云上 ENI 还在的泄漏；对'资源不存在'类错误码做幂等兜底，视为已删除直接放行。"

### 2.4 高频追问预案（五连）

- **为什么用 CRD 不在 daemon 里直接调 OpenAPI？** 三点：云 API 异步可能失败，需要可重入的协调，CR+reconcile 天然幂等断点续做；云资源拓扑持久化，controller 重启从 CR 续走不重复建 ENI；可观测，`kubectl get podeni` 直接看 IP/Node/Phase。和 Terway 的 PodENI、hybridnet 一致。
- **为什么按 MAC 找网卡？** 插件和 controller 解耦，Attach 异步完成后主机上唯一可靠的关联键是 MAC。
- **为什么 Pod 里双接口（ENI + veth lira0）？** ENI 直通后默认路由走 VPC 网关，但 VPC 网关不认识 ClusterIP——Service 的包出 ENI 就被丢。所以 Service CIDR 单独路由到 lira0 → 169.254.1.1 → host 上的 kube-proxy 做 Service 转换；VPC 流量走 ENI 保持原生 IP 和性能。Calico 的 169.254.1.1 同款。
- **ENI 怎么移进 Pod netns？** netlink LinkSetNsFd；netns 切换要 LockOSThread 锁 OS 线程。
- **竞态与一致性？** PodENI 名称=Pod 名一对一；Pod 重建比对 pod-uid annotation 不同则删旧建新；AllocIP 指数退避（10s 起、factor 2、5 步）等 Bind，插件侧 120s 超时。**主动讲参数 bug**：退避总上限约 310s 大于 CNI 120s 超时，参数没对齐——能指出自己系统的边界问题是强信号。

### 2.5 共享 ENI 过渡段（30 秒，引出 eBPF 的钩子）

> "独占模式的瓶颈是配额：机型决定单节点能挂几张 ENI。所以做了共享模式——一张 ENI 配多个 secondary IP，Pod 里用 ipvlan 子口，单节点 Pod 上限变成 ENI 数 × 9（每张 ENI 的辅助 IP 配额）。但共享模式一引入，**两个新问题**立刻出现：一是所有 Pod 共用 ENI 的安全组，云层面没法按 Pod 隔离；二是同 ENI Pod 互访走 ipvlan 二层直投、不进内核协议栈，iptables 根本看不到这些流量。这两个死结就是引入 eBPF 的直接原因。"

（来源：共享ENI-EBPF网络策略设计方案.md §1；单节点上限 ENI×9 出自 §3）

---

## 3. 配额管理与调度优化（方案稿——讲设计取舍）

### 3.1 现状问题（先讲故事，30 秒）

> "线上发现的问题链是这样的：Pod 对象**不请求** `msxf.com/eni` 扩展资源，调度器 NodeResourcesFit 完全不参与 ENI 判断，会把 Pod 调度到 ENI 已满的节点；配额校验点在 podeni_controller 的 Initial 阶段——**事后校验**。失败的表现是 Pod 长期 ContainerCreating、事件 CniCreateENIError，用户从调度事件里根本得不到真实原因。更糟的是记账有竞态：Allocatable 的统计口径是 `ENI.ID != ""`，而 Initial 阶段云侧 ENI 还没建、ID 为空——Pod A 调度进来时没扣账，Pod B 看着还有余量也调进来，A 建完 B 就超额了。"

（来源：docs/PodENI-MutatingWebhook配额与调度优化方案.md §2.1–2.2，`checkNodeENIConfig` 在 podeni_controller.go:188、统计口径在 resource.go:111）

### 3.2 方案一句话（核心）

> "**把配额校验从'调度后控制器数数'前移为'调度器原生扩展资源调度'。** Pod 若 requests `msxf.com/eni=1`，NodeResourcesFit 自动过滤 Allocatable 不足的节点——这和 NVIDIA device plugin 上报 `nvidia.com/gpu`、Terway 的 `alibabacloud.com/pod-eni` 是同一套机制，业界大规模验证过，零侵入。"

### 3.3 模块一：PodENIWebhook 口述（1 分钟）

> "改造分两个模块。**模块一是 lira-manager 里新增一个 MutatingWebhook**：Pod CREATE 时注入 `requests[msxf.com/eni]=1` 加一个识别 annotation。几个关键决策：只拦 CREATE 不拦 UPDATE——request 创建后不可变，拦截面降到最小；**只注入 requests 不注入 limits**——扩展资源不参与 QoS 和驱逐，limits 没意义；逻辑纯内存 match+patch，不做任何 IO，单次 <1ms；幂等——已带 request 的不重复注入；留 `mck.msxf.com/eni=off` 的 Pod 级逃生口；`failurePolicy=Ignore`——**webhook 挂了就退化为现状行为**（走旧路径事后校验），不会更糟，代价是必须配注入率指标告警防止静默降级。灰度用 namespaceSelector 按 label 放开 namespace。"

### 3.4 模块二：记账改造口述（30 秒）

> "**模块二是记账重构**，消灭竞态：统计口径从 `ENI.ID != ""` 改成 **PodENI 存在且未进 Deleting 就计入占用**——Initial/Created/Bind 全算，CR 创建先于云侧创建，Pod 被调度即占坑，语义和调度器视角对齐。同时把 1s 全量 List+patch 的轮询改成 **informer 事件驱动 + 60s 周期对账**，全局 nodeMutex 改按 nodeName 分锁。原 checkNodeENIConfig 保留为第二道防线。"

### 3.5 效果对比（一句话总结）

> "改造后：节点 ENI 满了，新 Pod 的表现从'ContainerCreating 卡死加看不懂的错'变成 **Pending、FailedScheduling: Insufficient msxf.com/eni**——原因直观；并发超额从'Initial 阶段竞态超卖'变成调度器 assumed-pod 记账天然无竞态；apiserver 压力从每秒两次全量 List 变成事件驱动。"

### 3.6 追问预案

- **为什么不用 ValidatingWebhook 直接拒绝？** 准入时点改 Pod 已来不及——校验的对象是"节点余量"这个动态状态，不是 Pod 本身；调度器 Filter 才是正确的时点。Webhook 只负责把 Pod 变成"声明需要这个资源"的形态。
- **kubelet 不是也会校验扩展资源吗？** 会，kubelet admission 按本机 Allocatable 校验 request，所以记账必须准——事件驱动+对账+偏差告警就是为这个。
- **和 GPU 调度的关系？** 同构：扩展资源上报→node allocatable→scheduler Filter。这套 ENI 治理经验平移到 GPU/RDMA 就是同一套骨架。

---

## 4. eBPF 网络策略 + Service 数据面（方案稿——讲原理和取舍）

### 4.1 为什么必须 eBPF（先甩两个死结）

> "共享 ENI 下按 Pod 做隔离有两个死结：**一**，所有 Pod 共用一张 ENI 的安全组，云层面无法按 Pod 隔离；**二**，同 ENI Pod 互访走 ipvlan 二层直投、不进内核协议栈，iptables 看不到这些包。而且 ipvlan 所有子口共用 ENI 的 MAC，身份只能用 IP 表达。所以方案是：**NetworkPolicy API 不变，执行层换成节点上的 eBPF，VPC 侧零改动、零新增 VPC 调用**。"

iptables vs eBPF 四点（备查）：看得到流量（iptables 看不到 ipvlan 直投，tc 挂在网卡上一律经过）；匹配开销（线性遍历 O(规则数) vs map 哈希 O(1)）；有状态（conntrack 静态 vs 自己建 ct map 可编程）；按 Pod 控制（链按接口建 vs 按 IP 查身份）。

### 4.2 eBPF 基础话术（面试官可能从零问起，30 秒）

> "eBPF 是内核提供的机制：一段受限的 C 程序编译后加载进内核，挂到固定事件点执行。**verifier** 加载时静态模拟所有分支——不能越界读内存、不能解空指针、不能无界循环、只能调白名单 helper——通过后 JIT 成本机指令，最坏情况是返回错误值，不会挂内核。程序对包做判定返回放行或丢弃；**map** 是内核态键值表，用户态和内核态都能读写，**热更新**——更新一次下一只包就查到新值，程序不用重启。我们用四种：hash 查身份、LRU 做连接表、lpm trie 做 CIDR 最长前缀、ringbuf 上报丢弃事件。"

挂载点关键顺序（内核源码核对过，体现深度）：**tc ingress 在 ipvlan 分发之前**（主口 ingress 能在分发前拦住所有入向包）；**tc egress 在 qdisc 之前**（slave 出向包切到主口发出时先过 tc egress）。

### 4.3 身份模型与挂载布局（1 分钟）

> "身份是两级映射：**PodIP → 身份ID**，身份 ID 是 namespace 加标签集合的哈希——相同 labels 的 Pod 共享身份，策略条目按身份对组织，不随 Pod 数膨胀。执行点是四个：**主口入向**拦全部入向（分发前）、**主口出向**拦全部 Pod 出向、**veth 出向**拦宿主机路由到 Pod 的流量（host 访问 Pod、SVC 后端在本节点走 veth 不走 ipvlan）、**Pod 口**拦同 ENI 互访——同 ENI 互访内核直投，主口两个钩子都看不到，Pod 口是唯一落点。四个点覆盖六条数据链路：节点↔Pod、跨节点、同节点跨 ENI、同 ENI、SVC 本地后端、集群外 CIDR。"

### 4.4 核心性质（金句）

> "整个方案最重要的性质是：**数据面只读 map，控制面只写 map**。策略变更、Pod 增删全部体现为 map 内容变化，转发程序自始至终不变——策略下发毫秒级生效，对比独占模式走 VPC API 秒级到十秒级、每次变更打 VPC 接口。"

配套机制：有状态用 ct LRU map（首包写五元组+超时，回包命中直接放行，LRU 满自动淘汰最旧）；**防伪造双层**——Pod 内 ipvlan 口校验源 IP=本 Pod 分配的 IP（防同 ENI 冒用），主口出向校验源属于本节点已分配 IP（防未分配 IP 出网）——共享 ENI 下 MAC 全同、VPC 无法区分，这两层是安全前提；抗篡改分层——宿主机三个程序 Pod 碰不到，Pod 内程序靠 daemon 周期 reconcile 重挂。

### 4.5 Service 数据面 eBPF（SVC-eBPF，接上同一个技术栈）

> "同一个技术栈顺手解决第二个问题：kube-proxy iptables 的规模痛点——规则线性遍历、Endpoint 变更全量重建、nf_conntrack 和网络策略两套状态。方案是三个 eBPF 程序：**svc 请求入口**（Pod 宿主 veth tc-ingress：查 svc4 选后端做 DNAT/SNAT）、**回包还原入口**（eth0 tc-ingress：ct 反查还原）、**host 起源入口**（eth0 egress 加 lo）。agent 放在 lira-daemon 里 watch Service/EndpointSlice——**map 是节点本地资源，写方就在本地**。一期 SNAT 语义与 kube-proxy 完全对齐，二期再做源保持让后端看到真实 Pod IP。"

三个必答追问（评审自答过的）：

- **为什么不上 Cilium？** Cilium 要接管 CNI 数据面，和 lira 的 ENI 直通/ipvlan 架构冲突——**取它的模型（map 结构、ct 语义），不引它的栈**。
- **为什么不用 IPVS？** 解决了规则规模，但仍在 netfilter 全路径、依赖 nf_conntrack、无法和网络策略共享连接状态，源保持无从谈起——目标不止是"快"。
- **灰度替换 kube-proxy 为什么安全？** 核心论据：**svc 的 DNAT 发生在请求者所在节点**——节点 A 用 eBPF、节点 B 用 kube-proxy，各自处理各自 Pod 的请求，互不交叉，不存在半个请求走新旧两半的中间态。逐节点灰度、逐节点回滚。
- **agent 挂了 Service 会全断吗？** 不会——map 是持久状态，数据面照常转发，影响仅是"变更停止生效"，重启自愈加 resync 修漂移。
- **连接内后端会漂移吗？** 不会——ct 记录已选后端，后续包命中 ct 直接复用；只有 ct 丢失（超时/淘汰/重启）才重选，那时 TCP 连接本身也断了。

### 4.6 与独占模式的关系（收口）

> "两个模式最终收敛成**一个 NetworkPolicy API、两个适配器**：独占模式翻译成安全组规则——慢、只有白名单语义、每次打 VPC 接口；共享模式翻译成 BPF map 条目——毫秒级、完整语义、零 VPC 调用。"

分期：一期管南北向和防伪造；二期补 NetworkPolicy 完整语义；三期 ct 有状态、metrics、跨节点 SVC 真实源还原。

---

## 5. RDMA 网络支持 for AI（设计方案——lira 与 AI Infra 的连接点）

### 5.1 为什么 AI 需要 RDMA（30 秒）

> "智算场景里 RDMA 是刚需：多机训练的 NCCL 通信、PD 分离推理的 KV 传输都依赖 RDMA 的低延迟；GPUDirect RDMA 让网卡和 GPU 直接搬数据绕过 CPU。网络侧分两层：**交换机侧**的 RoCE 无损参数（PFC/ECN）是底座团队的 P0 依赖项；**主机侧**的 VF 资源化是我们的活——把 RDMA 网卡变成 Kubernetes 可调度的资源。"

（来源：mck-ai-platform-design.md §2.1/§3.4；训练 BMS 网络走 RoCE/IB 见 mck-gpu-platform-design.md §4.1.2）

### 5.2 一期：开源插件先落地（30 秒）

> "一期用开源的 k8s-rdma-shared-dev-plugin，把 RDMA VF 注册成扩展资源 `rdma/hca_shared`，作业请求 `rdma/hca_shared: 1` 加 nodeSelector 选 RDMA 节点池；NCCL 经 VF 通信，GPUDirect RDMA 需要的 IOMMU/vfio 主机配置列为 POC 验证项。"

### 5.3 演进：RdmaNic CRD，复用 lira 模式（核心，1 分钟）

> "开源插件只解决'能用'，不解决'按需分配与隔离'。我的设计是**把 lira 的模式整体平移过来**：自研 RdmaNic CRD 加状态机（Allocatable → Assigned → Isolated），节点 daemon 管理 VF，调度侧加 Filter 做 NIC 配额校验——**和 PodENI 完全同构**。CR 上记录 nodeName、PF、vfIndex、state、owner Pod。这样获得三样开源插件给不了的能力：单 NIC 粒度的分配与隔离、NIC 故障的隔离态管理（对应 GPU 方案的摘卡时序）、以及**支撑 PD 分离推理的 KV 传输 QoS**。"

（来源：mck-ai-platform-design.md §3.4，含 RdmaNic CRD YAML 示例）

### 5.4 同构论证（被追问"凭什么说能平移"时）

> "三段同构：**云/硬件资源 → CR 状态机**（ENI 生命周期 ↔ VF 生命周期，都靠 finalizer 防泄漏）；**节点 daemon**（等 Bind 返回配置 ↔ VF 就绪返回配置）；**调度感知**（msxf.com/eni 扩展资源 ↔ rdma 扩展资源，都是 allocatable 打满即 Filter 掉）。lira 已经把'配额前移到调度器'的完整方案趟过一遍（§3 的 Webhook 方案），RDMA 直接继承。"

### 5.5 诚实边界（主动说）

> "RoCE/IB 训练网络本身（无损参数、组网）是独立网络专项，我们的方案只定义接口；RDMA 部分目前是设计，POC 清单里 GPUDirect RDMA 的 IOMMU/vfio 验证还没做。"

（来源：mck-gpu-platform-design.md §1.4 非目标——"RoCE/IB 训练网络设计：独立网络专项，本方案只定义接口"）

---

## 6. 全篇收口：一条主线（终面/开放题用）

> "我把 lira 这条线讲成一个演进故事：**第一步**做独占 ENI 直通，解决金融私有云里 Pod 和 VPC 互访的性能与安全管控，这步已实现自验；**第二步**发现配额瓶颈，做了两件事——ENI 配额上调度器（扩展资源），以及共享 ENI 把单节点密度提一个量级；**第三步**共享模式丢掉了隔离能力，iptables 又天然看不到 ipvlan 直投流量，所以网络策略和 Service 数据面整体换成 eBPF，数据面只读 map、控制面只写 map；**第四步**到了 AI 场景，网络需求从'连通'变成'RDMA 低延迟 + QoS'，而 lira 沉淀的'云资源→CR→状态机→调度感知'这套模式直接平移到 RDMA 网卡，RdmaNic CRD 和 PodENI 同构。**每一步都是上一步的自然延伸，机制是复用的，这是我对'平台工程'的理解——沉淀模式，而不是重复造轮子。"

---

## 7. 面试前 Checklist

- [ ] §1 电梯版背熟（30 秒，四块全覆盖）
- [ ] §2.2 ADD 八步 2 分钟口述（已实现部分，最高优先级）
- [ ] §2.4 五连追问每条能独立讲 1 分钟
- [ ] §3 配额问题→方案一句话→两模块，能画前后对比
- [ ] §4.1 两个死结 + §4.4"只读只写"金句背熟
- [ ] §4.5 三个"为什么不上 XX"备好
- [ ] §5.3 RdmaNic 同构论证三点
- [ ] §6 主线收口脱稿
- [ ] 诚信分层表（§0）开场主动讲——**分层定位本身就是加分项**
- [ ] 数字空着不编：ADD 端到端耗时、数据面耗时、attach 延迟、集群规模（interview-prep-cni.md §3 空表）

## 8. 未确认项（不编造清单）

1. 共享 ENI 分支（feature-multi-eniip）的实现细节以 PDF 主文档为准，eBPF 方案文档仅引用其接口命名（eth1/eth2=ENI、容器内 eth0=ipvlan 口、veth1=宿主通信口）。
2. eBPF 方案的挂载点顺序标注"内核源码已核对"，但具体内核版本（clsact 4.5 / LRU 4.10 / lpm 4.11 / ringbuf 5.8）是方案前提，未在目标环境逐台验证过——被问就说"方案里列了版本矩阵，实施前要 `uname -r` 逐项核对"。
3. RDMA 全部为设计：GPUDirect RDMA 的 IOMMU/vfio 配置验证是 POC 项；PD 分离（KV 传输依赖 RDMA）是二期 POC 前置项。
4. 性能数字（eBPF vs iptables、缓存命中收益等）方案明确"不预设收益数字，以可复现实验为准"——面试同样不给数字，给验证方法。
