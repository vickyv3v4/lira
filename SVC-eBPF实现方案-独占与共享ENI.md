# Service 数据面 eBPF 实现方案（独占ENI + 共享ENI）

> 关联文档：《Lira共享ENI方案及实现》《共享ENI-EBPF网络策略设计方案》
> 本文档目标：方案可被挑战——每个决策给出备选与取舍，每条链路走到闭环，每个故障有归属。

## 1. 问题定义与目标

### 1.1 现状

两种模式下容器侧结构完全一致：eth0 为业务口（ipvlan 或 ENI 直通，默认路由走 VPC 网关），veth1 为宿主机通信口，ServiceCIDR（192.168/16）路由指向 veth1 的 169.254.1.1。svc 流量因此全部经宿主机，由 kube-proxy 的 iptables 规则完成 DNAT/SNAT；从主文档跨节点链路的标注看，回包经过节点 IP（SNAT），由内核 conntrack 做反向还原。

### 1.2 现状的问题

1. **规模**：iptables 规则线性遍历，规则数与 Service 数 × 后端数成正比，Service 规模上去后每个包的匹配成本上升，且每次 Endpoint 变更触发规则重建；
2. **状态重复**：依赖 nf_conntrack，与网络策略（另一套状态）无法共享；
3. **源信息丢失**：SNAT 后后端看到的是节点 IP，无法按真实源做策略与审计（现状即如此，本方案一期沿用、二期解决）。

### 1.3 目标

- 数据面换成 eBPF：O(1) 查表、map 增量更新、连接表自主管理；
- Service / EndpointSlice API 不变，用户无感知；
- 独占 ENI 与共享 ENI 两种模式一套程序；
- 逐节点灰度替换 kube-proxy，可回滚。

### 1.4 非目标（防止范围发散）

- 不做 XDP 层加速（入向-only，覆盖不了出向与 host 起源流量，见 §2）；
- 不做 DSR（Direct Server Return，需 VPC 配合，三期后评估）；
- 不做 SCTP；
- 不改变 Service 语义（externalTrafficPolicy 等按 K8s 定义实现）。

## 2. 备选方案对比

| 备选 | 结论 | 理由 |
| --- | --- | --- |
| kube-proxy iptables 调优 | 否 | 线性遍历与全量刷规则的本质不变，只能推迟问题 |
| kube-proxy IPVS 模式 | 否 | 解决规则规模，但仍是 netfilter 全路径 + nf_conntrack；无法与网络策略共享连接状态；源保持（二期）无从谈起 |
| 引入 Cilium 整体替换 | 否 | Cilium 要接管 CNI 数据面，与 lira 的 ENI 直通/ipvlan 架构冲突；引入第二套网络体系，运维成本大于收益。只取其模型（map 结构、ct 设计），不引其栈 |
| **eBPF svc 数据面（本方案）** | 采用 | 与网络策略方案共用基础设施（pin 目录、map、agent 骨架），逐节点替换风险可控 |

## 3. 总体结构

### 3.1 为什么两种模式能共用一套程序

1. 容器侧结构相同（eth0 + veth1，ServiceCIDR 均指 veth1）；
2. ClusterIP 不是 VPC 可路由地址，svc 流量在两种模式下都必然经宿主机中转；
3. 因此请求入口（pod 宿主 veth）、本地后端投递（liraxxxx）、回包还原（eth0）是同一批设备。

**唯一差异：远端后端回包回到本节点后的落点。** 共享模式 ENI 在宿主机 netns（回包可经 ENI 主口）；独占模式 ENI 在 pod netns（回包直达 pod 内）。一期全 SNAT 语义下回包统一从 eth0 回，该差异不生效；二期源保持时才分叉（§5.3）。

### 3.2 挂载点

三个程序按职责命名：

| 程序 | 位置 | 职责 |
| --- | --- | --- |
| svc请求入口 | pod 宿主 veth（lirayyyy）tc-ingress | ct 查已有连接 → 查 svc4 → 选后端 → DNAT+SNAT → 本地后端 `bpf_redirect(liraxxxx)`；远端交宿主机路由 |
| 回包还原入口 | eth0（节点主网卡）tc-ingress | ① ct 反查命中 → 回包还原 → redirect(lirayyyy)；② ct 未命中 → NodePort/ExternalIP 新连接入口 |
| host起源入口 | eth0 tc-egress（另挂 lo） | host 起源的 svc 请求 DNAT；ct 标记的包直接放行（防回环） |

（二期新增：共享模式在 ENI 主口入向程序里集成源保持还原；独占模式在 pod 内 ENI 挂还原程序，需 reconcile。）

一期三个程序全部在宿主机 netns，pod 无法触碰（安全边界，§10）。

## 4. 数据结构

| Map | 类型 | Key → Value | 说明 |
| --- | --- | --- | --- |
| svc4 | hash | (IP, port, proto) → svc_id + 算法 | ClusterIP 用实际 IP；NodePort 用 0.0.0.0（与 kube-proxy 任意本机地址可访的语义一致） |
| backends | hash | (svc_id, 序号) → 后端 IP:port + rev_nat_id | Endpoint 变更只重写该 svc 的槽位 |
| rev_nat | hash | (rev_nat_id, 后端五元组) → 前端五元组 | 反向还原依据 |
| ct | LRU hash | 五元组+方向 → 状态/超时/**已选后端** | 与网络策略方案共用；**ct 同时承担"连接内后端一致性"**：后续包 ct 命中即复用已选后端，不再重新负载均衡 |
| snat | LRU hash | (出口IP, 协议, 端口) → 原始元组 | BPF 内做 SNAT 必须自管端口池：分配即在 snat/ct 记账，LRU 回收空闲端口 |
| affinity | LRU hash（可选） | ClientIP → 后端 | sessionAffinity=ClientIP |

**ct 语义（关键设计）**：svc请求入口对每个新连接先按"原始五元组+方向"查 ct；命中则直接套用记录的 NAT 结果（后端不变）；未命中才查 svc4 选后端并写 ct。TCP 中断连接靠 ct 的方向位区分请求/回程，回程命中时执行反向还原。

## 5. 主干流程

### 5.1 控制面

svc agent 放在 **lira-daemon**（每节点一个），watch Service / EndpointSlice：

- 为什么在 daemon 不在 lira-controller：map 是节点本地资源，写方就在本地；每节点 watch 全集群 Service 与 kube-proxy/cilium-agent 的成熟模型等价，不引入新的 controller→daemon 分发通道；
- 更新顺序：**先写 backends / rev_nat，后写 svc4**（保证 svc4 出现时后端已就绪）；删除反序（先摘 svc4 停新连接，再清后端）；
- 后端摘除：联动删除 ct 中指向该后端的条目（否则存量连接黑洞到超时）；
- agent 重启：map 已 pin，状态不丢；重启后 list-watch 全量 resync 与 map 比对，修复漂移。

### 5.2 数据面一期（SNAT 语义与 kube-proxy 对齐）

**① pod → svc，后端本节点（闭环）**

```
请求: pod1(ClusterIP) → veth1 → svc请求入口: ct未命中 → svc4命中 → 选pod2
      → DNAT dst→10.0.8.67, SNAT src→节点IP(端口从snat池分配)
      → 写ct/snat → bpf_redirect(liraxxxx) → pod2经veth1收到
回程: pod2(dst=节点IP) → 经其业务口 → VPC → 本节点eth0
      → 回包还原入口: ct反查命中 → src还原ClusterIP、dst还原pod1 → redirect(lirayyyy) → pod1
```

**② pod → svc，后端远端节点**：svc请求入口同样 DNAT+SNAT 后返回 TC_ACT_OK，交宿主机路由出 eth0；回包必然回到本节点 eth0（源是本节点 IP），回包还原入口统一还原。链路与①对称，闭环。

**③ NodePort / ExternalIP 入向**：外部包到 eth0 → 回包还原入口先查 ct（回包场景）→ 未命中查 svc4（NodePort）→ DNAT+SNAT → 本地 redirect / 远端交路由；回包经还原后 `bpf_redirect(eth0)` 出站。

**④ host → svc**：host起源入口（eth0-egress / lo）拦截本机发出的 ClusterIP 流量 → DNAT → 本地 redirect；回包仍走回包还原入口。

**⑤ hairpin（pod 访问自身所在的 svc 且选中自己）**：DNAT 后发现后端=本 pod → 照常 redirect(liraxxxx) 回到自己，回程走②同款路径（回包经 VPC 回 eth0 还原）。可用性闭环，代价是回包一次 VPC hairpin，与 kube-proxy 现状一致。

**⑥ 异常路径闭环**：

| 场景 | 行为 |
| --- | --- |
| 回包还原入口 ct 未命中（ct 被淘汰/节点重启） | 包继续进协议栈，目的=节点IP 无监听 → RST，客户端重试新建连接。与 kube-proxy 丢 conntrack 的行为一致 |
| svc4 未命中（非 svc 流量/未就绪 Service） | TC_ACT_OK 原样放行，交协议栈（无 svc 处理时一切如常） |
| backends 空（Service 无后端） | 一期取 TC_ACT_OK 交协议栈得到 refused——语义与现状对齐 |
| externalTrafficPolicy=Local 且本节点无后端 | 丢弃（K8s 语义规定，与 kube-proxy 一致） |
| UDP（DNS） | ct 按 UDP 超时（秒级）回收；LRU 本身面向高 churn 设计 |

### 5.3 数据面二期（源保持，两模式分叉）

一期 SNAT 去掉后，pod 源 IP 可直达后端（VPC 可路由）：

| | 共享 ENI | 独占 ENI |
| --- | --- | --- |
| 本地后端回包 | pod2 → VPC → 本节点 ENI 主口 → **主口入向程序内 ct 反查还原**（无新程序） | pod2 → VPC → pod1 netns 内的 ENI → **pod 内 ENI 还原程序**（pod 内挂程序） |
| 收益 | 后端看到真实源，网络策略按真实身份判定；少一次 VPC hairpin | 同左 |
| 代价 | 无新增 pod 内程序 | 新增 pod 内程序（特权 pod 可摘，需 reconcile，与网络策略方案的 pod 内程序同一机制） |

## 6. 生命周期与状态一致性

| 事件 | 处理 | 闭环依据 |
| --- | --- | --- |
| Service 创建 | 写 backends/rev_nat → 写 svc4 | 数据面可见即一致 |
| Endpoint 扩缩 | 重写该 svc 槽位；缩容联动清 ct | 存量连接不黑洞 |
| Service 删除 | 摘 svc4 → 清 backends/rev_nat | 先停新连接再清状态 |
| daemon 重启 | 程序/map 已 pin，重挂缺失的 attach 点；resync 修漂移 | 数据面中断时间 = 重启时长，等价 kube-proxy 重启 |
| 节点重启 | daemon 启动时全量重建 map 并加载程序，在 CNI 就绪前完成 | ct 丢失 → 存量连接重试，与现状一致 |
| LRU 淘汰 | 优先淘汰冷条目；TCP 中途淘汰会导致该连接下次包被当新连接重新选后端 → 容量按 连接速率×超时 规划，写满告警 | §7 容量项 |

## 7. 容量规划

| Map | 规划依据 |
| --- | --- |
| svc4 / backends / rev_nat | 集群 Service 数与平均后端数（如 5k svc × 10 后端量级，内存 MB 级）；写满时 agent 报警并拒绝新 Service，**存量不缩**（只增不减的失败模式） |
| ct | 并发连接速率 × 超时时长；LRU 满后淘汰冷连接 |
| snat | 出口 IP 端口空间（65535-1024）/ 端口占用时长；按 NodePort 并发与跨节点 svc 流量规划 |

## 8. 故障模式分析

| 故障 | 影响面 | 恢复 |
| --- | --- | --- |
| agent（daemon 内）崩溃 | 数据面继续工作（map 是持久状态）；仅 Service 变更停止生效 | daemon 重启自愈；节点 svc 变更期间告警 |
| 程序 attach 失败（内核差异） | daemon 启动时失败即不上报就绪 | 该节点维持 kube-proxy，灰度自然跳过 |
| eBPF 程序逻辑 bug | 限定在所在节点 | 节点级回滚开关（§9 灰度）；ringbuf 丢弃计数告警 |
| map 写满 | 新 Service/新连接失败 | 告警 + 容量规划；LRU 类自回收 |
| 节点宕机 | 其上连接中断（与 kube-proxy 一致，conntrack 同样丢失） | 客户端重试 |
| 配置漂移（map 与 k8s 不一致） | 策略与实际后端不符 | resync 周期全量比对 |

## 9. 灰度、回滚与行为对齐

**逐节点替换为什么安全（核心论据）**：svc 的 DNAT 发生在**请求者所在节点**。节点 A 用 eBPF、节点 B 用 kube-proxy 时，A 上 pod 的请求由 A 的数据面处理、B 上由 B 处理，互不交叉；Service/Endpoint 是全局对象，两边都 watch 同一数据源。不存在"半个请求走新、半个走旧"的中间态。

灰度步骤（节点级开关，daemon 配置）：

1. 校验内核能力（同网络策略方案的版本矩阵）；
2. 加载程序、建 map、agent 首轮同步完成、探活通过；
3. 该节点停 kube-proxy（DaemonSet nodeSelector 逐节点）；
4. 行为对齐验证清单：ClusterIP 四层连通、NodePort、sessionAffinity、UDP DNS 高频压测、回包源地址、externalTrafficPolicy=Local、hairpin；
5. 回滚 = 反向操作。一期 SNAT 语义与 kube-proxy 完全一致，是可对齐、可 diff 验证的前提。

## 10. 安全边界

- 三个程序与全部 map 在宿主机 netns，pod 无 CAP 权限不可触碰；加载需要节点 CAP_BPF/CAP_SYS_ADMIN，与 kube-proxy 写 iptables 的权限等级一致；
- 一期 SNAT 后源身份丢失由网络策略方案的 slave 侧防伪造独立兜底（两方案互不依赖对方的正确性）；
- 二期 pod 内 ENI 还原程序在 pod netns，复用网络策略方案的 reconcile 机制与风险结论。

## 11. 性能验证方案

不预设收益数字，以可复现实验为准：

1. **同硬件 A/B**：同一节点分别以 kube-proxy / 本方案承载同一组 Service，压测 TCP/UDP 短连接与长连接 p50/p99；
2. **规模曲线**：Service 数 100 → 1000 → 5000，对比每连接建立延迟的退化斜率（预期 iptables 随规模上升、eBPF 平坦——此为设计预期，由实验证实或证伪）；
3. **churn 测试**：Endpoint 高频变更时规则/map 更新延迟与数据面抖动。

## 12. 已知限制

| 限制 | 原因 | 对策/接受理由 |
| --- | --- | --- |
| 一期保留 SNAT | 行为对齐优先，灰度与回滚要求 diff 可测 | 二期源保持已设计（§5.3） |
| NodePort 仅覆盖 eth0 与 lo | 共享模式如需 ENI 主 IP 暴露 NodePort，在主口入向程序加查表即可；独占模式 ENI 在 pod netns，与 kube-proxy 现状一致 | 与现状对齐 |
| hostNetwork 后端 | DNAT 后走本机协议栈 | ct 还原闭环，语义与 kube-proxy 一致 |
| TCP 连接被 LRU 淘汰会断连 | 状态表有限容量 | 容量规划 + 满载告警 + BPF LRU 冷热倾向 |
| localhost 访问 ClusterIP | 需挂 lo（host起源入口） | 已纳入挂载清单 |

## 13. 评审关注点自答

**Q1：eBPF 程序出 bug 会不会断网？**
verifier 保证内存/循环安全（最坏是返回错值，不会 panic 内核）；逻辑 bug 影响面限定单节点；灰度隔离 + 节点级回滚；ringbuf 上报丢弃计数，异常率告警先于业务感知。

**Q2：为什么不用 IPVS？**
IPVS 解决规则规模，但仍在 netfilter 全路径、依赖 nf_conntrack、无法与网络策略共享 ct，源保持（二期）无从谈起。本方案的目标不止是"快"。

**Q3：为什么不整体上 Cilium？**
Cilium 要接管 CNI 数据面，与 ENI 直通/ipvlan 架构冲突。取其成熟设计（map 结构、ct 语义），不引入其栈。

**Q4：agent 挂了 svc 会全断吗？**
不会。map 是持久状态，数据面照常转发；影响仅是"变更停止生效"。daemon 重启自愈并 resync 修漂移。

**Q5：灰度期间新旧数据面会不会打架？**
不会。DNAT 发生在请求者所在节点，逐节点替换天然隔离（§9 首段）。

**Q6：SNAT 在 BPF 里端口怎么管？**
snat map 记账 + 端口池分配，LRU 回收空闲端口；与 ct 联动（释放连接即释放端口）。

**Q7：连接内后端会漂移吗？**
不会。ct 记录已选后端，后续包命中 ct 直接复用（§4 ct 语义）；只有 ct 丢失（超时/淘汰/重启）才重新选择，此时 TCP 连接本身也已中断。

**Q8：DNS 高频短连接会不会把 LRU 打爆？**
UDP ct 超时秒级、LRU 面向高 churn 设计；容量按 QPS × 超时规划并压测验证（§11-3）。

**Q9：Headless Service 呢？**
无 ClusterIP，DNS 直接返回后端 IP，流量走 pod 直连路径，不进入本数据面。闭环。

**Q10：与网络策略方案什么关系？**
共用 pin 目录、ct map、agent 骨架；部署相互独立。共享模式二期源保持后，svc 流量在网络策略方案的 veth出向检查点自动获得真实源判定，策略方案无需改动。

## 14. 分期与验收标准

| 阶段 | 内容 | 验收标准 |
| --- | --- | --- |
| 一期 | 三个程序 + SNAT 语义对齐 kube-proxy，逐节点灰度 | §9-4 验证清单全通过；单节点长期运行（含 Endpoint churn）无漂移 |
| 二期 | 源保持（共享模式主口入向集成 / 独占模式 pod 内程序），与网络策略真实源判定打通 | 后端可见真实源；策略对 svc 流量按 pod 身份生效 |
| 三期 | maglev、per-svc metrics、healthCheckNodePort 由 agent 提供 | LB 一致性测试；指标可视 |
