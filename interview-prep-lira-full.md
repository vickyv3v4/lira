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
| 开源 RDMA 链路机制（sriovdp + multus + sriov-cni + whereabouts） | **源码逐行研读（非自研，落地选型依据）** | 本地两个仓库代码 + kubelet v0.36 deviceplugin API（§5.2） |

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
> 7. 建 **veth 对**（Pod 侧接口名 **veth1**，host 侧 `lira`+hash）：veth1 **不配 IP**，纯 Service 导流口——配 169.254.1.1/32 scope link + Service CIDR via 169.254.1.1（onlink）+ 节点 IP/32 via 169.254.1.1，加对 169.254.1.1 的永久 ARP；host 侧配 169.254.1.1/32 加回 Pod IP 的 /32 路由 + **PodIP→podVethMac 永久 ARP**（永久 ARP 两侧都写死，两侧 veth 的 MAC 都是插件自生成）。
> 8. 回写 Pod event（AllocIPSucceed 加耗时），输出 CNI Result 给 kubelet。"

（来源：interview-prep-cni.md §1 ADD 八步）

### 2.3 DEL 链路（30 秒）

> "删 Pod → CNI DEL 清 host 侧 veth 和路由 → pod controller 检测 sandbox 退出 → PodENI 置 Phase=Deleting → podeni controller **DetachENI → DeleteENI → 摘 finalizer** → CR 删除，云资源回收。finalizer 的意义就是防止 CR 删了云上 ENI 还在的泄漏；对'资源不存在'类错误码做幂等兜底，视为已删除直接放行。"

### 2.4 高频追问预案（五连）

- **为什么用 CRD 不在 daemon 里直接调 OpenAPI？** 三点：云 API 异步可能失败，需要可重入的协调，CR+reconcile 天然幂等断点续做；云资源拓扑持久化，controller 重启从 CR 续走不重复建 ENI；可观测，`kubectl get podeni` 直接看 IP/Node/Phase。和 Terway 的 PodENI、hybridnet 一致。
- **为什么按 MAC 找网卡？** 插件和 controller 解耦，Attach 异步完成后主机上唯一可靠的关联键是 MAC。
- **为什么 Pod 里双接口（ENI + veth1）？** ENI 直通后默认路由走 VPC 网关，但 VPC 网关不认识 ClusterIP——Service 的包出 ENI 就被丢。所以 Service CIDR 单独路由到 veth1 → 169.254.1.1 → host 上的 kube-proxy 做 Service 转换；VPC 流量走 ENI 保持原生 IP 和性能。Calico 的 169.254.1.1 同款。
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

## 5. RDMA 网络支持 for AI（开源机制解剖 + 设计方案——lira 与 AI Infra 的连接点）

### 5.1 为什么 AI 需要 RDMA（30 秒）

> "智算场景里 RDMA 是刚需：多机训练的 NCCL 通信、PD 分离推理的 KV 传输都依赖 RDMA 的低延迟；GPUDirect RDMA 让网卡和 GPU 直接搬数据绕过 CPU。网络侧分两层：**交换机侧**的 RoCE 无损参数（PFC/ECN）是底座团队的 P0 依赖项；**主机侧**的 VF 资源化是我们的活——把 RDMA 网卡变成 Kubernetes 可调度的资源。"

（来源：mck-ai-platform-design.md §2.1/§3.4；训练 BMS 网络走 RoCE/IB 见 mck-gpu-platform-design.md §4.1.2）

### 5.2 开源链路实现机制解剖（代码级，可深钻——不是"背方案"而是"读过源码"）

**一句话定位（先把认知框架立住）：**

> "K8s 里 RDMA 的容器可见性不是 CNI 给的，是 device plugin 通过 Device Plugin API 的 DeviceSpec 直挂的：kubelet 在 Allocate 阶段收到 `/dev/infiniband/uverbsX` 设备节点列表，创建容器时直接挂进去。CNI（sriov-cni）从头到尾不碰 RDMA——我通读了它 master 全部 git 历史，`--grep rdma` 零个 commit。所以容器 RDMA 是两条平行线：**设备挂载走 device plugin 层，数据面接口走 CNI 层**，各管各的。"

**五步链路口述（1 分钟，落地链路=multus + sriovdp + sriov-cni + whereabouts）：**

> "1. sriov-network-device-plugin 按配置 `isRdma: true` 选择器发现 VF：Mellanox 读 `/sys/bus/pci/devices/<addr>/infiniband/` 拿 RDMA 设备名（mlx5_5），Intel E810 走 devlink 参数 `enable_rdma`；把 VF 注册成扩展资源 `mellanox.com/mlnx_rdma`。2. Pod 请求该资源，kubelet 调 Allocate 返回 DeviceSpec + env + 可选 CDI annotation。3. multus 从 kubelet **PodResources API** 查到该 Pod 分到的 VF PCI 地址（deviceID），注入委托给 sriov-cni 的 CNI 配置。4. sriov-cni 拿着 deviceID 配 VLAN/MAC、把 VF netdevice 移进 Pod netns、改名、UP，再调 whereabouts 分 IP。5. 容器里同时具备 `/dev/infiniband/uverbsN`（kubelet 挂的）和 VF 网卡（CNI 配的），这就是 RDMA 应用看到的全部。"

**Allocate 到底返回什么（能背出原型是加分项）：**

> "gRPC 返回 `AllocateResponse`，每容器一个 `ContainerAllocateResponse`，四字段：`Devices []DeviceSpec`（HostPath/ContainerPath/Permissions——RDMA 就是 /dev/infiniband 下的设备节点）、`Mounts`、`Envs`（PCIDEVICE_ 前缀，含 _INFO JSON 里的 uverbs 路径和 rdma_dev 名）、`Annotations`（CDI 模式给运行时）。这四样 kubelet 原样下发 CRI，最后落在 OCI 容器的 device 配置里。"

**三个高频追问预案：**

- **"multus 查 deviceID 有什么用？在哪起作用？"** 作用是把"抽象资源账本"对齐到"真实硬件"：Pod 能跑 RDMA，前提是三个东西指同一个 VF——kubelet 记账并挂设备节点的那个、netns 里配 IP 的那个、扩展资源上报的那个；deviceID（PCI 地址）是三者的信物。multus 本身只注入不使用；落到 sriov-cni 代码上，deviceID 被翻译成**三个操作坐标**：① **(PF 名, VFID)**——VF 的 VLAN/MAC/限速/spoofchk 属性必须经 PF 的 netlink 按 VFID 下发，不能在 VF 自己接口上配；② **宿主 netdev 名**——SetupVF 搬进 Pod netns 的就是它（绑了 DPDK 驱动也从此判定）；③ **锁与占用标记的 key**——pci_allocator 防两个 Pod 抢同一 VF（旧 Pod DEL 与新 Pod ADD 竞态）。deviceID 为空 sriov-cni 直接报错"VF pci addr is required"，插件没有任何自主选卡逻辑——反证它就是唯一入口。
- **"节点/容器重启 RDMA 怎么恢复？"** 分三档：容器重启——sandbox netns 不重建，VF 和 IP 原样还在，kubelet 重建容器时 device manager 按记录重新挂设备节点，自动恢复；kubelet 重启——device manager 从 checkpoint 恢复分配，插件探测到 socket 变化自动重注册重 ListAndWatch，exclusive 模式下被占设备的 sysfs 文件没了、靠 devlink 参数兜底继续上报（这正是插件源码里明写的场景）；整节点重启——VF 回宿主 netns，Pod 重建走全流程 ADD，whereabouts 按 CRD 分配记录把同一个 IP 还给同一个 Pod，旧 netns 不存在时 CmdDel 对 NSNotExist 静默放行。
- **"sriov-cni 没有 RDMA 代码，RDMA 的 netns 隔离怎么保证？"** 这是开源链路的明确边界：共享模式靠"每 VF 独立 RDMA 设备实例（mlx5_N）+ 设备节点直挂"保证应用级隔离；netns 级 RDMA 设备隔离（把 RDMA 设备 move 进 Pod netns）在社区 master 里没有，只有 fork/定制版本实现过——这正是自研 RdmaNic 隔离态的演进动机之一。
- **"节点重启后，Pod 还能拿到上次的那个 VF 吗（vf1 会不会变 vf2）？"** 分两层：**硬件层**——VF 的唯一身份是 PCI 地址，固件决定 VF 枚举顺序，同机重启后 VF k 还是那个地址（前提：硬件/插槽/BIOS 不变）；**记账层**——"谁用哪个 VF"记在 kubelet device manager 的 checkpoint（/var/lib/kubelet/device-plugins/kubelet_checkpoint），节点重启后 Pod 重建走 Allocate 时同 podUID 直接复用记录的 deviceID，不重新挑。**会换 VF 的场景**：Pod 删了重建（新 UID 重挑）、checkpoint 丢失、换卡/换插槽/BIOS 变更导致 PCI 枚举变化、Pod 换节点。整条链路对"VF 换了"是容忍的：RDMA 设备名（mlx5_N）和 uverbs 编号重启后本就可能漂移，device plugin 每次分配从 sysfs 现查映射（rdmamap）、multus 每次 ADD 现查 PodResources——稳定锚点始终是 PCI 地址，IP 由 whereabouts 按记录归还。

**multus 拿 deviceID 的完整过程（multus master 源码逐行核对过，1 分钟）：**

> "先把结论立住：**deviceID 不是从 multus 配置文件读的，是 CNI ADD 时实时查 kubelet 的资源分配账本**。五步：**一，触发前提**——NAD 必须带 `k8s.v1.cni.cncf.io/resourceName` 注解，没有它 multus 压根不查资源、deviceID 为空，sriov-cni 直接报 'VF pci addr is required'——手写 NAD 忘带注解是运维第一坑。**二，取 Pod 身份**——用 CNI_ARGS 里的 Pod 名/namespace 去 API Server 拿完整 Pod 对象（含 UID）。**三，惰性建 resourceMap**——遇到第一个带注解的 NAD 才建：首选 dial `/var/lib/kubelet/pod-resources/kubelet.sock` 调 PodResources API 的 ListPodResources，拿 kubelet device manager 维护的'Pod→容器→资源名→设备ID'全量快照；**socket 文件不存在就降级**直接读 checkpoint 文件 `/var/lib/kubelet/device-plugins/kubelet_internal_checkpoint`。**四，匹配聚合**——API 路径按 Pod 名+namespace 匹配、checkpoint 路径按 PodUID 匹配，把该 Pod 所有容器的设备按资源名聚合、排序，得到 `{资源名: [PCI地址列表]}`。**五，按序取号注入**——用注解里的资源名查 map，取 `DeviceIDs[Index]` 后 Index++（同 Pod 挂多个同资源 NAD 依次分配），`addDeviceIDInConfList` 把 `"deviceID": "0000:xx"` 写进委托配置的每个 plugin JSON——sriov-cni 的 stdin 里就有了。"

**multus 侧追问预案（四连）：**

- **"NAD 不带 resourceName 注解会怎样？"** deviceID 为空，sriov-cni LoadConf 直接报 "VF pci addr is required"，插件没有任何自主选卡逻辑。SR-IOV Network Operator 生成的 NAD 会自动带这个注解；手写 NAD 排查第一点就是它。
- **"什么时候走 API、什么时候走 checkpoint？"** 按 socket 文件是否存在判定（`/var/lib/kubelet/pod-resources/kubelet.sock`）。两个后端的匹配键还不一样：API 按 Pod 名+namespace、checkpoint 按 PodUID——同名重建 Pod 的窗口期两者语义有差异，这是源码里明写的实现细节。
- **"Index++ 的分配语义有什么坑？"** 多容器多资源时 multus 把 Pod 全部容器的设备聚合进同一个 map，按'排序后列表+附件顺序'取号——是**顺序约定，不和具体容器强绑定**；容器 A 的网络附件可能拿到容器 B 请求的设备号。单容器单资源无感，多容器多资源池要小心。
- **"devinfo 文件（/var/run/k8s.cni.cncf.io/devinfo/）是 deviceID 来源吗？"** 不是——master 主链路里它是设备插件按 device-info-spec 写的辅助信息，multus 只在配置了 cniDeviceInfoPath 时把它复制到 runtime conf 目录（CopyDeviceInfoForCNIFromDP）供其他消费方用；**deviceID 的唯一来源就是 PodResources API / checkpoint**。可以主动讲这个认知修正："以为走 devinfo、读了源码才知道主链路是 PodResources API"——本身是加分信号。

**sriov-cni ADD 处理过程逐句稿（把五步里的第 4 步展开到插件内部，1 分钟）：**

> "multus 委托进来的 stdin 带 deviceID（VF 的 PCI 地址），sriov-cni 的 cmdAdd 分六步：**一，LoadConf**——校验 PCI 合法性，从 sysfs 反查 PF 名和 VFID；同时在 `/var/lib/cni/sriov` 对该 PCI 地址拿文件锁、查占用表，防旧 Pod 的 DEL 和新 Pod 的 ADD 对同一 VF 竞态；查 VF 的 host 网口名，查不到且绑了 DPDK 驱动就标记 DPDK 模式。**二，FillOriginalVfInfo**——把 VF 原始状态（admin MAC/VLAN/QoS/spoofchk/trust/限速/link state/MTU）读出来存进 netconf，这是 DEL 回滚的依据。**三，ApplyVFConfig**——在 PF 的 netlink 上按 VFID 下发 NAD 声明的属性：VLAN/QoS/proto、admin MAC、min/max 限速、spoofchk、trust、link_state。**四，SetupVF**——VF netdev 先搬进临时 netns、改名成 Pod 侧接口名（默认 net1）、清掉 altname，再搬进 Pod netns，设 effective MAC、开 arp_notify/ndisc_notify 和 optimistic DAD、UP。**五，IPAM**——`ipam.ExecAdd` 把整个配置委托给 whereabouts，拿回 IP 后在 Pod netns 里 ConfigureIface 配地址和路由。**六，收尾**——netconf 缓存到 `/var/lib/cni/sriov/<containerID>-<ifName>` 给 DEL 用，占用表登记 PCI 已分配，等 carrier（200ms 窗口）后主动发 GARP/Unsolicited NA——IP 是复用的，邻居缓存里可能还挂着上一个 Pod 的 MAC。"

**sriov-cni DEL 处理（30 秒）：**

> "cmdDel：从缓存读 netconf——读不到就静默成功，否则 kubelet 重试 DEL 会死循环；拿 PCI 锁；`ipam.ExecDel` 释放 IP；**ResetVFConfig 按缓存的原始状态逐项还原 PF 侧属性**；ReleaseVF 在 Pod netns 里 down、改回宿主机名、还原 effective MAC 和 MTU、搬回宿主机 netns；占用表摘除。两个边界值得主动讲：netns 已不存在（节点重启过）对 NSNotExist 静默放行，这是 CNI 规范要求；**ResetVFConfig 必须在 ReleaseVF 之前**——源码注释明说有的驱动在 trust off 状态下对已搬走的 VF 做 reset 会报错，所以先经 PF 还原属性、再搬接口。"

**VF 预创建（前置条件，30 秒）：**

> "VF 必须在插件启动前建好——vf-setup.md 原话 'requires SR-IOV VFs to be created on the host ahead of startup'，device plugin 只发现不创建。ConnectX 是两段式：**固件层** mlxconfig `SRIOV_EN=1 NUM_OF_VFS=N`（持久化，要重启）；**驱动层** `echo N > sriov_numvfs`（每次开机要重做）。RDMA 字符设备不用手动建——VF 被 mlx5_core 绑定后内核自动注册 ib 设备、生成 `/dev/infiniband/*`。插件对 VF 变化无感：ListAndWatch 的 Probe 未实现、设备列表静态，**改 VF 数量必须重启 sriovdp**。生产上这类主机配置交给 sriov-network-operator 或开机脚本。"

**CNI 层追问预案（四连）：**

- **"为什么 VF 属性在 PF 上配、不在 VF 口配？"** VLAN/spoofchk/trust/限速是 PF 的硬件资源属性，netlink 的 IFLA_VF_* 只能经 PF 下发，容器里根本看不到 PF——这正是 deviceID 被翻译成 (PF, VFID) 坐标的原因（呼应上一组追问①）。
- **"cmdAdd 中途失败会留半配置吗？"** 不会——defer 钩子里接口已建成则 ReleaseVF、并无条件 ResetVFConfig，IPAM 失败同样 ExecDel 回滚；真正危险的窗口是 netconf 缓存写完之后进程崩，但那时 DEL 的输入还在缓存里，能走完整清理。
- **"为什么先搬临时 netns 改名、不直接搬进 Pod netns？"** 改名（含清 altname）在临时 netns 里完成、成功才进 Pod netns——避免把还叫原名的接口塞进去后改名失败、把半状态留在 Pod netns；回滚路径也短，renameAndMoveLink 直接从临时 netns 搬回宿主。
- **"MTU 谁定的？whereabouts 和 host-local 差别？"** MTU 继承 PF（ApplyVFConfig 从 pfLink 读），NAD 没有 MTU 字段，要改就改 PF；whereabouts 的分配记录在集群侧 IPPool CRD（带 Pod UID + TTL 回收孤儿 IP），多节点不冲突、Pod 重建拿回同 IP——host-local 是节点本地文件账本，跨节点必然冲突。

**链路优化方式（RDMA 数据面，机制层面——只讲机制和代码依据，收益数字按全篇纪律不编）：**

> "分四层说。**第一层，直通本身就是优化**：VF 独占直通，无 bridge、无 overlay，数据路径只有网卡本身；DPDK 场景再进一步走 vfio-pci 用户态收发包，是 kernel bypass 的另一条路（设备侧的体现就是 Allocate 里的 `/dev/vfio/*` DeviceSpec 和 PCIDEVICE 环境变量）；GPUDirect RDMA 让 NIC 与 GPU 直接 DMA，IOMMU/vfio 配置是 POC 项。**第二层，拓扑对齐**：device plugin 给每个 VF 上报 NUMA 节点（读 sysfs numa_node，`excludeTopology` 可关），Topology Manager 把 GPU/网卡/大页内存钉在同一 NUMA——RDMA 注册的 pinned memory 跨 NUMA 访问是必须避免的代价。**第三层，参数**：MTU 跟 PF（大帧少包数）；NCCL 的 RoCE 依赖交换机侧 PFC/ECN 无损底座（§5.1 已划给网络专项）；容器内 memlock/IPC_LOCK 是 pinned memory 注册的硬前提（rdma 文档原话）。**第四层，CNI 层把时间花在收敛上**：GARP/Unsolicited NA 把 IP 复用后的邻居收敛从'等 ARP 老化/超时'变成 UP 后主动立即通告；PCI 文件锁防 ADD/DEL 竞态是正确性优化；whereabouts 确定性分配让 Pod 重建拿回同 IP，减少上层重建。隔离边界再强调一次：共享模式下每 VF 独立 RDMA 设备实例是应用级隔离，netns 级 RDMA 隔离开源没有——这是 RdmaNic 隔离态的动机（§5.4）。"

（来源：sriov-network-device-plugin / sriov-cni 两个本地仓库源码逐行（cni.go cmdAdd/cmdDel、sriov.go VF 状态机、pci_allocator、rdma.go、devices/api.go）+ **multus-cni master 源码（2026-10 经 GitHub API 逐行核对：k8sclient.go getKubernetesDelegate、kubeletclient.go、checkpoint.go、types conf.go addDeviceIDInConfList）** + sriovdp docs/vf-setup.md（VF 预创建原话）、docs/rdma/README.md（IPC_LOCK）、sriov-cni docs/configuration-reference.md（NAD 参数域）+ kubelet v0.36 deviceplugin API 类型 + sriov-cni `git log --grep rdma` 零命中；checkpoint 行为属 kubelet 通用机制，细节以部署版本源码为准；链路优化部分为机制层面共识 + 代码依据，无实测数字）

### 5.3 一期：开源插件先落地（30 秒）

> "一期用开源的 k8s-rdma-shared-dev-plugin，把 RDMA VF 注册成扩展资源 `rdma/hca_shared`，作业请求 `rdma/hca_shared: 1` 加 nodeSelector 选 RDMA 节点池；NCCL 经 VF 通信，GPUDirect RDMA 需要的 IOMMU/vfio 主机配置列为 POC 验证项。选型前提是我把这套 Device Plugin 机制的底层解剖透了（§5.2）——知道它给的是什么（设备节点直挂）、不给的是什么（netns 级 RDMA 隔离），自研演进才有靶子。"

### 5.4 演进：RdmaNic CRD，复用 lira 模式（核心，1 分钟）

> "开源插件只解决'能用'，不解决'按需分配与隔离'。我的设计是**把 lira 的模式整体平移过来**：自研 RdmaNic CRD 加状态机（Allocatable → Assigned → Isolated），节点 daemon 管理 VF，调度侧加 Filter 做 NIC 配额校验——**和 PodENI 完全同构**。CR 上记录 nodeName、PF、vfIndex、state、owner Pod。这样获得三样开源插件给不了的能力：单 NIC 粒度的分配与隔离、NIC 故障的隔离态管理（对应 GPU 方案的摘卡时序）、以及**支撑 PD 分离推理的 KV 传输 QoS**。"

（来源：mck-ai-platform-design.md §3.4，含 RdmaNic CRD YAML 示例）

### 5.5 同构论证（被追问"凭什么说能平移"时）

> "三段同构：**云/硬件资源 → CR 状态机**（ENI 生命周期 ↔ VF 生命周期，都靠 finalizer 防泄漏）；**节点 daemon**（等 Bind 返回配置 ↔ VF 就绪返回配置）；**调度感知**（msxf.com/eni 扩展资源 ↔ rdma 扩展资源，都是 allocatable 打满即 Filter 掉）。lira 已经把'配额前移到调度器'的完整方案趟过一遍（§3 的 Webhook 方案），RDMA 直接继承。"

### 5.6 诚实边界（主动说）

> "RoCE/IB 训练网络本身（无损参数、组网）是独立网络专项，我们的方案只定义接口；开源链路机制我读到了代码级（§5.2），但没有在集群上实际运维过；自研 RdmaNic 目前是设计，POC 清单里 GPUDirect RDMA 的 IOMMU/vfio 验证还没做。"

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
- [ ] §5.2 开源链路：五步口述 + Allocate 四字段 + deviceID 三作用 + VF 重启归属四连追问；multus 取号五步（注解前提/双后端降级/匹配键差异/Index++ 语义）+ multus 追问四连；sriov-cni ADD 逐句稿/DEL 两个边界 + VF 预创建两段式 + 链路优化四层 + CNI 层追问四连
- [ ] §5.4 RdmaNic 同构论证三点
- [ ] §6 主线收口脱稿
- [ ] 诚信分层表（§0）开场主动讲——**分层定位本身就是加分项**
- [ ] 数字空着不编：ADD 端到端耗时、数据面耗时、attach 延迟、集群规模（interview-prep-cni.md §3 空表）

## 8. 未确认项（不编造清单）

1. 共享 ENI 分支（feature-multi-eniip）的实现细节以 PDF 主文档为准，eBPF 方案文档仅引用其接口命名（eth1/eth2=ENI、容器内 eth0=ipvlan 口、veth1=宿主通信口）。
2. eBPF 方案的挂载点顺序标注"内核源码已核对"，但具体内核版本（clsact 4.5 / LRU 4.10 / lpm 4.11 / ringbuf 5.8）是方案前提，未在目标环境逐台验证过——被问就说"方案里列了版本矩阵，实施前要 `uname -r` 逐项核对"。
3. RDMA 分层：开源链路机制（§5.2）为源码研读结论、代码级可深钻，但未在集群实操验证；自研 RdmaNic 全部为设计——GPUDirect RDMA 的 IOMMU/vfio 配置验证是 POC 项，PD 分离（KV 传输依赖 RDMA）是二期 POC 前置项。kubelet device manager checkpoint / PodResources API 属 kubelet 通用机制（非本项目代码），细节以所部署 K8s 版本源码为准。
4. 性能数字（eBPF vs iptables、缓存命中收益等）方案明确"不预设收益数字，以可复现实验为准"——面试同样不给数字，给验证方法。
5. multus deviceID 机制结论核对自 multus-cni **master 分支**源码（2026-10，GitHub API 逐行读取 k8sclient.go / kubeletclient.go / checkpoint.go / conf.go）；所部署 multus 版本的确切行为（PodResources 支持的引入版本、匹配键细节、DRA 叠加是否存在）**未考证**——被问就讲 master 行为并注明核对方法：`multus --version` 对应 tag 的源码复核一遍。
