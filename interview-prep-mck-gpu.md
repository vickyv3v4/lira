# MCK 智算方案（HAMi + Volcano）· 设计与面试准备手册

> **定位声明（面试先讲这句）**：本文档是**方案设计 + 源码研究**，不是生产实施记录。MCK 侧结论全部来自 gc-mck-portal / res / ctl / plt 四仓库读码（详见 `interview-prep-mck.md`）；HAMi 侧结论来自 **HAMi-2.6.1 源码**（含 cmd/pkg/docs 全量结构）与官方文档 `how-to-use-volcano-vgpu.md`；CPU 超分方法论来自《马上云-计算资源池 k8s 集群按照资源池维度超分技术设计方案》（2024/9 内审版）。**方案未在生产上线**，面试按"我把一条已验证的 CPU 云管道延伸设计到 GPU"来讲，绝不按"我们生产在跑"来讲。

---

## 0. 一句话定位（30 秒电梯版）

> "MCK 是我们自研的托管 Kubernetes 产品，从订单→ECS→装机→组件→计费是一条完整闭环，但目前是纯 CPU 容器云。我做的智算方案是：**不推翻任何一条现有链路**，用 Volcano 解决'批量作业的队列化调度'、用 HAMi-core 解决'GPU 显存/算力切分'，把 GPU 节点交付、组件栈安装、队列注册、作业闭环、计费对账做成同一条管道的**能力延伸**——节点级的东西走 kubespray playbook，集群级的东西走 cluster_feature 管道，经营级的东西走超分对账。整条链每一步都复用了 MCK 已被生产验证过的机制。"

这段话的杀伤力在于：**每个名词都能落到 MCK 已存在的机制上**（playbook 白名单 / cluster_feature / task 表 / 云监控计费 / 资源池超分），面试官往下钻任何一层，你都在讲"已被验证的东西怎么被复用"，而不是空中楼阁。

---

## 1. 现状 → 智算的映射表（整个设计的合法性来源）

这是本方案最重要的一张表：**左边全部真实存在且有代码级证据，右边是 1:1 延伸**。

| 能力 | MCK 现状（CPU 云，真实存在） | 智算需要 | 差距与补法 |
|---|---|---|---|
| 节点交付 | 订单→ECS→DNAT→kubespray（`cluster.yml`/`scale.yml`） | GPU 节点入池 | **新增节点级 playbook**：NVIDIA driver + nvidia-container-toolkit + containerd nvidia runtime；GPU 机型校验复用下单校验链 |
| 组件安装 | `cluster_feature` 表 + post_hook_main 子任务（calico/nginx-ingress/categraf），`install_status` 状态机（ClusterFeatureMapper.xml:54） | Volcano / HAMi-core / DCGM 安装与升级 | **新增 feature 类型**：volcano / hami-vgpu / dcgm-exporter，扩展 depends_on 依赖 |
| 调度 | default-scheduler；**超分方案已积累 kube-scheduler fork 经验**（codelab 仓库 v0.24.9_lpf，Oversale Score 插件） | 队列 + gang 调度 + GPU 感知 | 集群级 feature 安装 Volcano（scheduler/controller/admission），`deviceshare.VGPUEnable: true` |
| GPU 切分 | 无 | 显存/算力隔离 | volcano-vgpu-device-plugin（HAMi 组织维护，基于 HAMi-core） |
| 配额 | k8s 原生 ResourceQuota（静态） | 资源池维度可弹性配额 | Volcano Queue（capability/deserved/weight），**poolCode 维度对齐超分方案** |
| 计费 | 云监控推 STOP/START/RELEASE 计费事件；categraf 采集 | 卡时/vGPU 时计量 | DCGM exporter + kube-state-metrics → **口径扩展，对接方式需与云监控 SRE 对齐**（CPU 超分 PDF 有同款对齐先例） |
| 超分 | **CPU 节点放大**：MutatingWebhook 改 Allocatable × 超分比 + OverSaleJob 打真实利用率注解 + Score 插件过载保护（pool 级 setRate） | GPU 显存超分/切分 | **方法论继承**：pool 级超分率 + 设备级切分 + 监控对账；差异点见 §3 链路 4 |
| 多集群 | 元集群按 poolCode 划分，res 两级 client 工厂，kubeconfig 收敛在 res | GPU 资源池纳管 | 完全复用，零改动 |
| 租户凭证 | res `CreateKubeConfig` 签发受限 kubeconfig（Secret 下发） | 租户提交训练作业 | **直接复用**，租户凭它向工作集群提交 vcjob |
| 存储 | 平台已有 CSI（egp-csi-nas / nfs，超分 PDF node describe 可证）；MCK volume 随节点交付（**存算耦合**） | 数据集/模型/checkpoint 共享存储 + 缓存加速 | **新增数据层**（Fluid/JuiceFS 独立缓存集群），见 §3.5 |

> **面试点**：先亮这张表再讲链路。面试官立刻明白：你不是"学了个 HAMi 硬凑方案"，而是"拿着自家平台的资产清单找 GPU 的最佳拼装位"。**架构师岗位考的就是这个映射能力**。

---

## 2. 总体架构（五层，纸上能画出来）

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ 经营层  pool 超分率/水位对账 · 卡时计费(云监控) · 资源中心                        │
│         （继承 CPU 超分: cboot pool setRate 思路 → gpuRate）                   │
├─────────────────────────────────────────────────────────────────────────────┤
│ 业务层  gc-mck-portal: GPU 节点订单/扩容 · cluster_feature(智算组件)           │
│         gc-mck-res: 落 CR · Queue 注册 · 租户 kubeconfig 签发                 │
├─────────────────────────────────────────────────────────────────────────────┤
│ 调度层  工作集群内: Volcano Scheduler(actions: enqueue/allocate/backfill;      │
│         plugins: gang/drf/proportion/deviceshare[VGPUEnable]/predicates)     │
│         + Volcano Controller(vcjob/PodGroup 生命周期) + Admission             │
├─────────────────────────────────────────────────────────────────────────────┤
│ 设备层  volcano-vgpu-device-plugin DaemonSet(GPU节点, HAMi 组织维护)           │
│         Node capacity: volcano.sh/gpu-number / gpu-memory                    │
│         隔离: HAMi-core(LD_PRELOAD CUDA hook: 显存上限/算力比例)               │
├─────────────────────────────────────────────────────────────────────────────┤
│ 节点层  GPU ECS ← 订单 → DNAT → kubespray(新增 gpu playbook) → 打标           │
│         MCK 元集群按 poolCode 纳管, CR namespace = mck-<tenantId> (零改动)     │
├─────────────────────────────────────────────────────────────────────────────┤
│ 数据层  L0 对象存储(S3)/NFS ← L1 独立CPU缓存集群(Fluid/JuiceFS, 不与GPU混部)   │
│         L2 Dataset CR + PVC(RWX 共享读) · 缓存亲和调度                         │
│         (存算分离: GPU 节点无状态化, 数据不随节点走)                           │
└─────────────────────────────────────────────────────────────────────────────┘
```

**层与层之间的接口全是 MCK/K8s 原生对象**：portal↔res 是 HTTP，res↔元集群是 CR/CM，节点层↔设备层是 device plugin API，调度层↔设备层是 extended resource + scheduler plugin，作业↔数据层是 PVC/Dataset CR，业务层↔经营层是 Kafka 事件 + 监控指标。**没有发明任何一个新协议。**

---

## 3. 五条闭环链路（主菜）

> 闭环的定义（面试直接讲这个框架）：每条链路必须四要素齐备——**正向流、状态回流、失败分支、兜底对账**。MCK 的 CPU 链路正因为四要素齐了才算"生产可用"，智算链路逐条对照补齐。

### 3.1 链路 1：GPU 节点交付闭环（最像现有链路，先讲这条热身）

**正向流**（对照 MCK 创建集群五阶段，只标差异）：

```
下单(校验GPU机型/驱动版本) → 支付落库(新增 gpu_config) → 建 GPU ECS → DNAT
→ 生成 hosts/vars CM(新增 gpu_vars: 驱动版本/内核头文件/nvidia 源)
→ res 落 Cluster + ClusterOperation CR
→ ctl 跑 cluster.yml / scale.yml
   └─ preHook: ping.yml → disable-firewalld.yml → 【新】gpu-driver.yml
      (内核devel匹配+驱动离线安装+锁定内核版本+modprobe)
      【新】gpu-runtime.yml (nvidia-container-toolkit + containerd nvidia runtime)
   └─ cluster.yml 装 k8s
   └─ postHook: kubeconfig.yml → 【新】node-label.yml(打 mck.msxf.com/gpu=true,
      nvidia.com/gpu.product=A100, mck.msxf.com/ecsId=<id>)
→ ops=Succeeded → 回调 portal → 节点入池 → 推云监控计费(GPU 机型)
```

**状态回流**：ClusterOperation conditions 精确匹配 clusterOpId（`<clusterId>-addnode<N>`），portal 按 conditions 找本次操作 —— **原机制零改动**。

**失败分支**：GPU 专属失败点两个 —— ①驱动与内核版本不匹配（playbook 内 rescue 分支回滚 rpm + 告警明确报"kernel-driver mismatch"）；②GPU ECS 机型无货（下单校验链预检 + 对齐 MCK 预创建 ECS 的两阶段提交）。失败回滚**复用 `scale_out_failed_rollback`**（删 DNAT → 查/删 ECS → 重算节点数 → cluster → running）。

**兜底对账**：`taskScaleOutClusterNode` 等 @CloudTask 兜底扫描不变；新增对账项——DB 节点 ↔ 实际 Node 的 GPU 标签 ↔ 计费机型三方一致（进 §3.4 对账器）。

> **面试金句**：「GPU 节点交付对我而言不是新问题——是把 driver 和 runtime 这类**节点级、幂等、可重跑**的步骤，按 MCK 已有的 playbook 白名单模式补进 preHook。driver 装失败和 mount-xfs-pquota 失败的处理路径完全一样。」

### 3.2 链路 2：智算组件栈安装闭环（feature 管道延伸）

**安装序（有依赖，必须在设计里显式化）**：

```
driver(链路1的playbook) → nvidia-container-toolkit(链路1)
→ Volcano(scheduler+controller+admission, 队列配置)
→ volcano-vgpu-device-plugin DaemonSet(仅GPU节点, nodeSelector)
→ DCGM exporter → (可选)HAMi scheduler-dashboard
```

**机制**：全部作为新增 `cluster_feature` 类型，走 portal post_hook_main 子任务模式（现网已有 serviceaccount/kube-state-metrics/mck-categraf 三类先例），每个 feature 有独立 `install_status`(not_installed→installing→installed/failed) + `feature_status`。

**关键设计——节点级与集群级分治**：

| 粒度 | 走什么 | 例子 |
|---|---|---|
| 节点级（每台 GPU 机都要） | kubespray playbook（preHook/postHook） | driver、toolkit、containerd runtime、内核锁定 |
| 集群级（一份组件） | cluster_feature 管道（post_hook 子任务） | Volcano、device plugin、DCGM |

> **面试点**：这个分治不是拍脑袋——MCK 现有机制就是这么分的：`mount-xfs-pquota`（节点级）走 playbook，`kube-state-metrics`（集群级）走 post_hook。我做的只是**判断每个新组件落在哪一栏**。

**状态回流**：DaemonSet ready 数轮询机制**原样复用**——现网 categraf 安装就是"每 10s 轮询 DaemonSet ready 数直到等于节点数（超时 300s）"，device plugin 的等待逻辑同构；回写 cluster_feature.install_status。

**失败分支**：device plugin DaemonSet 不 ready 的两大原因（driver 版本不匹配 / nvidia runtime 未注册）在 feature 安装子任务的 fail_reason 里落结构化错误码，而不是笼统 failed——对齐 MCK "ansible 失败原因从 CR 注解一路回传到界面"的既有设计。

**升级闭环**：Volcano/HAMi 升级也走 feature（新版本=新 feature 版本号），对齐"集群变更必须过 ClusterOperation 管道"的纪律。

### 3.3 链路 3：作业闭环（训练任务从提交到计费）

**正向流**：

```
租户(持 res 签发的受限 kubeconfig) → 在工作集群提交 vcjob(或裸 Pod)
  metadata: schedulerName: volcano, labels: volcano.sh/queue-name=<poolQueue>
  resources.limits: volcano.sh/vgpu-number / vgpu-memory / vgpu-cores
→ Volcano Admission 校验 queue 存在性/配额
→ vcjob → PodGroup(minAvailable=N) → enqueue(gang 满足才 Inqueue)
→ Volcano Scheduler: allocate
   ├─ proportion/DRF: 队列间公平
   ├─ deviceshare(VGPUEnable): 按 vgpu-memory/cores 选卡
   └─ predicates/nodeorder/binpack
→ 绑定 → device plugin 在节点侧完成设备分配(CUDA_VISIBLE_DEVICES + LD_PRELOAD
   HAMi-core hook 注入显存上限/算力比例)
→ 任务运行 → vgpu-usage 指标 → 计量
```

**状态回流**：PodGroup/vcjob 状态机（Pending→Inqueue→Running→Completed/Failed/Unknown）→ 两条出口：①MVP 阶段——留在集群内，租户自己 kubectl 看，平台只保证"调度配额层"闭环；②产品化阶段——portal 新增 AI 任务表，事件回流生成计费流水。**先 ① 后 ②，明确分期是设计成熟度的体现。**

**失败分支**：gang 调度的核心价值就是失败分支明确——minAvailable 不满足则整组不启动（不留一半浪费卡）；`pending / 事件: unschedulable` 携带原因（队列配额不足 vs 节点显存碎片）回传租户。**Volcano 的 Pending 原因可观测性是选它的直接理由之一。**

**兜底对账**：Queue capability 与节点真实 GPU 容量定期对账（§3.4）；僵尸 vcjob（Pod 卡在 Allocating 超 X 分钟）由 Volcano controller 策略清理，平台侧新增巡检告警。

> **为什么作业闭环敢说"逻辑正确"**：租户凭证签发（res CreateKubeConfig）、队列注册、配额控制、状态观测四段全部有现成机制对应，新增的只有"Queue 注册"和"计量口径"两个点。

### 3.4 链路 4：资源对账与超分闭环（继承 CPU 超分方法论——最有辨识度的一条）

CPU 侧已内审的方案结构：**pool 级超分率（cboot setRate）→ OverSaleJob 定时从 Prometheus 算真实利用率打 Node 注解 → MutatingWebhook 放大 Allocatable → 自研 Score 插件做 0.3/0.8 双卡点过载保护 → 监控/告警/分阶段上线**。

GPU 侧逐项映射：

| CPU 超分组件 | GPU 智算对应物 | 差异点（主动讲） |
|---|---|---|
| pool cpuRate（cboot setRate） | pool gpuRate（显存维度超分率，**默认 1.0 起步**） | 显存是不可压缩资源，OOM 代价远高于 CPU 抢占；**一期"切分"不"超分"** |
| Node Allocatable 放大（webhook） | device plugin 上报 `volcano.sh/gpu-memory` 总量 | GPU 不需要改 Node status——切分发生在设备插件层，**没有"心跳覆盖"难题** |
| OverSaleJob 10min 定时算利用率 | DCGM exporter 采集真实显存/算力利用率 | GPU 利用率要分维度：SM 活跃 / 显存占用 / 显存带宽 |
| Score 插件 0.3/0.8 双卡点 | deviceshare 插件 + 水位控制 | 二期可在 Volcano 侧加过载保护插件（同构于 Oversale 插件） |
| pool_monitor 表 cpu_limit 列 | pool_monitor 扩展 gpu 字段（同款 DDL 思路） | — |
| webhook failurePolicy=Ignore + 停掉即回原值 | device plugin 挂了 = 新 pod 不能用 GPU，存量 pod 不受影响 | 天然更安全的失败半径 |

**对账闭环（新增一个对账器，结构同构 OverSaleJob 双定时任务）**：

```
定时任务1(5min): 对账三方一致
  DB 节点账(poolCode) ↔ 集群真实 GPU Node(标签筛选) ↔ Queue capability 汇总
  不一致 → 以集群真实状态为准修 Queue + 落差异事件
定时任务2(1h): 水位巡检
  池内已分配显存/物理显存 > 阈值(如85%) → 告警
  池长期低利用率 → 触发"降超分率/缩容评估"事件
→ Kafka 事件 → portal: 高水位自动生成扩容预生成单(半自动,人审批)
                    低水位触发缩容建议(走 §3.1 缩容链路,带反向补偿)
```

> **面试金句**：「CPU 超分我们踩过的所有坑——监控取值要跟 SRE 对齐、webhook 性能要压测、超分率只能由大往小调、分阶段上线——在 GPU 侧全部前置成了设计约束。显存不可压缩，所以我一期直接把超分率钉死在 1.0，把"切分"和"超分"分成两个阶段。**方案的可信度来自方法论的继承，而不是参数的拍脑袋**。」

### 3.5 链路 5：存算分离与数据集闭环（补存储层——同时填 JD"容器存储"空白）

**为什么智算必须有这层（三个 IO 特征决定）**：模型文件大且共享读（7B FP16 ≈ 14GB、70B ≈ 140GB，几十个作业反复加载同一模型，不能每次冷读对象存储）；训练数据集 TB 级多作业共享读；checkpoint 写入带宽敏感（训练中断恢复的命脉）。

**现状锚点（真实证据）**：平台现网已接 CSI——超分 PDF 的 node describe 里有 `csi.volume.kubernetes.io/nodeid: {"egp-csi-nas":...,"nfs.csi.k8s.io":...}`；而 MCK 的 volume 表是"卷随节点交付、生命周期依附节点"——**这是存算耦合形态，GPU 时代必须反转**。

**"分离"的三层含义**：

| 分离点 | 内容 | 带来的收益 |
|---|---|---|
| 节点分离 | 缓存集群是**独立 CPU 节点池**，不与 GPU 混部 | GPU 节点无状态化——缩容/故障替换不碰数据（呼应链路 1 缩容反向补偿：数据不在节点上，缩错不丢） |
| 生命周期分离 | 数据集独立于集群/节点存在 | 集群删了数据集还在（MCK volume 表做不到的），跨集群复用同一份模型 |
| 调度协同 | 缓存预热状态打 node label → 作业缓存亲和调度 | 避免冷读；Fluid × Volcano 是社区常用组合（集成机制 POC 核实，见未确认项 9） |

**目标架构**：

```
L0 数据源层   对象存储(S3协议)/NFS —— 数据的唯一权威副本
L1 缓存加速层 独立 CPU 缓存集群(JuiceFS/CubeFS/Fluid+缓存Runtime)
              —— 用便宜机器扛吞吐, GPU 池只算不存
L2 编排层     Fluid Dataset CR(数据集抽象+缓存可观测) + PV/PVC(RWX 只读共享)
```

**闭环四要素**：

```
正向流:   数据上传对象存储 → 注册 Dataset CR → 缓存预热(Runtime 扩容+warmup)
          → 作业挂 PVC → 缓存亲和调度 → 运行
状态回流: 缓存命中率/容量/IO 指标进云监控(与 DCGM 同源思路)
失败分支: 预热失败不阻塞提交——降级直读 L0 + 告警; checkpoint 写失败=任务级告警
兜底对账: Dataset 引用计数(无人引用的缓存自动回收) + 缓存集群容量水位
```

**分期**：二期只做 NFS/PV 共享读（RWX 基础版，零新组件）；三期上 Fluid 缓存集群 + 数据集产品化。**一期明确不做**——GPU 池规模小、作业形态未定，先不引入缓存复杂度。

> **面试金句**：「MCK 现在的存储观是"卷跟着节点走"——这在 CPU 云成立，在 GPU 云必须反过来："**数据不动、算力流动**"。判断标准很朴素：缩容一台 GPU 机时，我不应该有一秒钟在担心数据。」

---

## 4. 关键设计决策（到接口/CR 粒度）

### 4.1 Cluster CR 扩展

```yaml
# clusters.mck.msxf.com 新增 spec 字段（保持向后兼容，字段为空=纯 CPU 集群）
gpuConfig:
  enabled: true
  driverVersion: "550.54.15"
  cudaVersion: "12.4"
  cards:                      # 机型白名单，下单校验链用
    - model: A100-80G
      count: 8
  shareMode: vgpu             # vgpu | mig | exclusive
  memoryFraction: 1.0         # 显存切分总量/物理显存，一期固定 1.0
```

playbook 白名单（entrypoint.go 的 `ActionInfo` 表）新增：`gpu-driver.yml` / `gpu-runtime.yml` / `gpu-health.yml`（nvidia-smi 自检进 precheck 体系）。**白名单模式的好处照旧成立**：加 GPU 能力 = 加白名单项 + 加 playbook，业务侧零改动。

### 4.2 Queue 注册与容量模型

- 命名：工作集群内 `queue-pool-<poolCode>`（池级默认队列）+ 租户子队列（Volcano hierarchy，weight 分配）。
- capacity = Σ（节点 `volcano.sh/gpu-memory` × memoryFraction）由对账器维护，**人不去手填容量**——这是闭环的关键：容量来源唯一（对账器），变更唯一入口（链路 1/缩容链路）。
- `deserved/weight` 用于多租户保底抢占（reclaim action），对齐"订单驱动的保底资源 + 弹性共享"的云产品惯例。

### 4.3 计费口径（设计明确到"事件"）

- 独占卡：卡时（对齐现有 ECS 计费粒度）。
- vGPU：`vgpu-memory/vgpu-cores` 折算比例 × 卡时；数据源 = kube-state-metrics（pod limits）+ DCGM（真实用量），**取申请量为计费口径、用量为经营分析口径**（与主流云厂商一致，避免按用量计费的争议）。
- 事件：复用云监控计费通道，新增 GPU 维度计量事件。**与云监控 SRE 的口径对齐是明确待办**（CPU 超分 PDF 里"跟云监控 sre 对齐 node status 字段"是同款先例，我直接引用它证明这个流程的存在）。

### 4.4 调度整合选型（双调度器问题——必考题）

| 路线 | 组成 | 优点 | 缺点 | 结论 |
|---|---|---|---|---|
| **A. Volcano + volcano-vgpu-device-plugin**（HAMi 官方文档路线） | Volcano 为集群唯一调度器（gang/queue/proportion），HAMi 只出 device-plugin + HAMi-core 隔离 | 单调度器无冲突；gang+队列+切分一次到位；官方文档明确支持（Volcano >1.9，`deviceshare.VGPUEnable: true`） | 放弃 HAMi 自身调度器的高级策略 | **主选** |
| B. HAMi scheduler 为主 + Volcano 仅作 controller | schedulerName=hami-scheduler，用 vcjob/PodGroup 但调度走 HAMi | HAMi 策略丰富、异构卡管理强 | **双调度器冲突**：PodGroup 的 gang 语义没人保证（HAMi 2.10 已开始补 PodGroup/gang 支持，劣势在收敛，但仍非官方整合路线）；两套 scheduler 配置/版本漂移 | 仅当"纯推理、无 gang 需求"时可行 |
| C. 双集群分治 | 训练集群=A 路线；推理集群=HAMi scheduler 独立集群 | 各取所长 | 多一套集群形态，MCK 多集群能力虽在但运维成本翻倍 | 演进选项，不是 MVP |

> **面试点**：路线 A 的判定依据是 HAMi 官方文档原话——"You DON'T need to install HAMi when using volcano-vgpu"。**用上游自己的话收敛选型，比引用任何博客都硬**。同时补一句：HAMi 的价值在方案里没丢——HAMi-core（隔离）、volcano-vgpu-device-plugin（设备层）、以及未来国产卡接入（HAMi 支持 Ascend 910B/寒武纪/海光/燧原/摩尔线程，docs 目录可查）都是 HAMi 体系资产。

### 4.5 MIG 与隔离等级（"隔离"必须讲准确，这里最容易翻车）

- HAMi-core 的隔离是**应用态 CUDA API 拦截（LD_PRELOAD hook）**：显存上限靠拦截 cudaMalloc，算力比例靠拦截计算 API。**不是硬件级隔离**，进程绕过 CUDA API（如 NCCL 直通、CUDA graph 的某些路径）存在理论上逃逸面。
- 硬隔离只有两条路：**MIG**（A100/H100 物理切分，HAMi 支持 dynamic MIG）或独立卡独占。
- 选型矩阵：

| 场景 | 模式 | 理由 |
|---|---|---|
| 大模型训练（多机多卡） | 独占 + gang（`nvidia.com/gpu` 原生请求，不走切分） | NCCL/拓扑亲和，切分反而损害性能与确定性 |
| 推理/小模型训练/开发调试 | vGPU 切分（vgpu-memory/cores） | 利用率优先，隔离要求可接受 |
| 多租户强隔离合规场景 | MIG | 物理隔离，卡型受限 |

---

## 5. 分期计划（讲"闭环"必须配节奏，否则像画饼）

| 期 | 目标 | 闭环内容 | 不做什么（明确说） |
|---|---|---|---|
| 一期 | GPU 集群交付 + 组件栈 | 链路 1 + 2：GPU 节点订单→装机→feature 安装→节点打标→计费 | 作业产品化、超分（memoryFraction=1.0） |
| 二期 | 队列化调度 + 作业自闭环 + 存储基础版 | 链路 3：Queue/vcjob/gang/vgpu 切分 + 租户提交闭环（集群内）；存储：NFS/PV 共享读（RWX） | 跨池调度、缓存加速层 |
| 三期 | 经营闭环 + 存算分离完整版 | 链路 4：对账器、水位、计费口径落地；链路 5：Fluid 缓存集群 + 数据集产品化 | 自动缩容（人工审批前置） |
| 演进 | 异构与联邦 | 国产卡（HAMi 异构 device support）、K8s DRA 方向、Karmada 多池联邦 | — |

> **面试点**：分期的每条边界都是"砍需求"的决策——一期不做超分（不可压缩资源风险）、二期不做产品化（先验证调度）、三期不做自动缩容（误缩容不可逆）。**敢砍比敢做更显架构成熟度。**

---

## 6. 风险清单（面试主动讲 = 加分）

1. **隔离强度**：HAMi-core 是应用态 hook 非硬件隔离；对 NCCL 等绕过路径的隔离覆盖有限 → 大训练一律独占模式规避。
2. **GPU 节点 OS 兼容**：驱动与内核版本强绑定，国产 OS（Kylin/openEuler）上 driver 离线源是 playbook 的第一难点——MCK 已有 enable-repo（支持国产 OS 离线源）经验可复用。
3. **device plugin 失败半径**：plugin 挂掉后存量 pod 正常、新 pod 拿不到设备 → 可接受，但要告警；比 CPU 超分 webhook 挂掉的影响面更可控。
4. **显存碎片**：切分模式下碎片导致"容量够但调度不上"→ 靠 binpack 插件 + 对账器暴露碎片率指标。
5. **版本耦合**：Volcano 版本（需 >1.9）× HAMi-core × driver 版本三者兼容矩阵要在 POC 阶段钉死并进镜像构建流程。
6. **计费口径未对齐**：见 §4.3，明确待办不是设计缺陷。

---

## 7. 面试可用性评估（直接回答你的问题）

**结论：可以作为面试材料，而且对"智算云 PaaS 平台架构师"这个 JD 是强加分项——但必须按"设计方案 + 源码研究"定位讲，有四条红线。**

为什么可以（对应 JD 逐条）：

- JD 第 6 条"GPU Kubernetes / AI Infra"：原文写了"参与/研究"层级的要求。你手里的东西是**完整的方案设计 + HAMi 源码级研究 + 与自家平台的无缝映射**——这恰好是"研究"的满分答卷。面试官真正筛的是"能不能把 GPU 调度问题放进自家平台语境里想"，不是"装没装过"。
- 架构师岗的考察本质：**方法论迁移**。你最有力的叙事恰恰是："CPU 云的每一条被验证过的机制（playbook 白名单/feature 管道/task 表/超分对账/云监控计费），我都能指出它在 GPU 方案里的对应物"——这是没做过 MCK 的人讲不出来的。
- 与已有两份材料的关系：`interview-prep-mck.md`（真实经历，主菜）+ `interview-prep-cni.md`（真实经历）+ 本文（设计推演）。**前两份建立"我做过"的信用，本文展示"我能设计"的高度**，比例大约 7:3——设计部分讲 20 分钟里最多 6 分钟。

四条红线（违反任何一条，材料从加分变减分）：

1. **不说"我们生产在跑"**。标准表述：「这套方案我做到了设计评审深度，包含 CR 定义、playbook 白名单扩展、四条闭环链路和分期计划；CPU 侧的方法论是生产验证过的，GPU 侧还没有实际负载跑过。」
2. **不编生产细节**。不编"我们 A100 集群多大规模""HAMi 踩过什么坑"。被问生产数据就用 MCK CPU 云的真实数字 + "GPU 规模是我设计时的容量假设"。
3. **HAMi/Volcano 事实必须标注来源**。你讲的每个版本号/参数都应能落到"HAMi-2.6.1 源码目录 X / 官方文档 Y"——本文件引用的事实都标了出处，§9 列了"未确认项"，背熟。
4. **被追问深度时的诚实台阶**：「这块我读到 X 层，再往下是我给 POC 排的验证项」——然后接 POC 清单（见下）。

**POC 清单（"没上线怎么验证"的标准答案，背熟）**：

1. 显存隔离精度 benchmark：双 pod 共享一卡，各限 3G，跑压测看越界行为与性能损耗（HAMi 官方有 benchmark 文档）。
2. gang 调度故障演练：kill 掉 8 卡任务中的 1 个 pod，验证整组重拉与卡释放。
3. driver playbook 幂等重跑：同一节点连续执行 3 次 gpu-driver.yml，验证无副作用。
4. 对账器混沌测试：手动改 Queue capability / 删节点标签，验证 5 分钟内收敛。
5. 计费口径：pod 申请量 vs DCGM 用量的差异报表，跟财务/云监控确认口径。

---

## 8. 高频追问库

- ★ **为什么选 Volcano 而不是 k8s 原生 scheduler + 自己写队列？**
  gang 语义（minAvailable 整组调度）原生不具备，自己写在 scheduler framework 里等于重写半个 Volcano；Queue/DRF/reclaim 是生产验证的插件体系；且我们 CPU 超分已有 kube-scheduler fork 维护负担，**能不自研调度器就不自研**。
- ★ **为什么 HAMi 不整个上，只用它的 device-plugin/core？**
  官方文档明确 volcano-vgpu 路线不需要 HAMi scheduler；双调度器的 gang 语义冲突是硬伤；HAMi 的核心资产（HAMi-core 隔离 + 异构卡支持）在路线 A 里全部保留。如果面试官问"那 HAMi scheduler 的负载感知调度你不想要了？"——答：那是二期后按需评估的点，且它要求换掉默认调度器，风险收益比不适合第一版。
- ★ **GPU 节点装 driver 为什么走 playbook 而不是 DaemonSet？**
  节点级幂等装机是 kubespray 的本职（试想 scale.yml 加节点的场景，DaemonSet 装机要额外处理"新节点先于 DS 就绪"的窗口期）；且 MCK 所有节点级变更都有 playbook 先例，审计口径统一。
- ★ **没在生产跑过，你怎么保证方案能落地？**
  四条闭环链路的每一环都有 MCK 生产验证的对应机制托底（映射表 §1）；剩下的是三个新组件的行为验证，全部进了 POC 清单；分期计划保证一期只碰"节点交付"这个风险最低、与现有链路差异最小的环节。
- ★ **超分在 GPU 上你敢做吗？**
  一期不做。显存不可压缩、OOM 代价高，一期把"切分"做对（超分率 1.0）；二三层把对账/水位闭环建起来之后，超分率才成为可调参数——这和 CPU 超分"先 sit 稳定运行再上租户环境"的审慎节奏完全一致。
- **租户怎么提交任务？绕过平台怎么办？**
  租户只拿受限 kubeconfig（res 签发）；配额在 Queue 层而不是 RBAC 层，绕过提交最多占自己队列的份额；平台侧对账器保证 Queue capacity 真实。
- **多机多卡训练的网络（RDMA/GPUDirect）考虑了吗？**
  一期不做分布式高速互联（MCK 网络侧是共享 ENI VPC 直通，带宽满足推理/单机训练）；多机 NCCL 场景列入演进（需 RoCE/IB 网络，是独立网络方案，不在本方案画饼）。拓扑感知调度上游已有独立可复用的解——**Volcano 1.16 的 HyperNode 控制器**（拓扑发现 + 超节点管理，独立部署），演进时直接复用而非自研。
- **存算分离在你的方案里怎么体现？**
  三层分离：节点分离（缓存集群独立 CPU 池，GPU 无状态化）、生命周期分离（数据集独立于集群）、调度协同（缓存亲和）；现状反差是现成的引子——MCK volume 随节点交付是存算耦合，超分 PDF 里 `egp-csi-nas` 的 CSI 注解证明平台有存储底子；一期不引入缓存的理由（规模小、复杂度后置）；对象存储选型和 Fluid×Volcano 集成机制如实说"未定稿，POC 核实"。
- **这方案跟阿里 ACK GPU / 腾讯 TKE GPU 比呢？**
  它们绑定自家云 API 和控制面；MCK 智算的差异化在**订单驱动的资源池经营层**（超分对账 + 半自动扩缩容是自家 CPU 云验证过的方法论），以及 kubespray 层对国产 OS/国产卡的可控定制空间。

---

## 9. 理解深度要求：两个项目各要懂到哪一层（版本现状 2026-10-15 核实）

**版本现状（GitHub Releases 核实）**：

| 项目 | 最新稳定版 | 发布日期 | 你手上的版本 | 面试口径 |
|---|---|---|---|---|
| HAMi | v2.10.0 | 2026-08-21（2.9: 2026-05-19） | 精读过 2.6.1 源码 | "精读 2.6.1，2.x 架构稳定；2.10 的 PodGroup/gang、动态 MIG 重构、DRA driver 独立出去我扫过 changelog" |
| Volcano | v1.16.0 | 2026-10-14（1.15.3/1.14.6/1.13.5 补丁: 2026-09-30） | 文档级 + 方案使用其 CR/插件模型 | "vGPU 整合 1.9+ 即支持；1.16 刚发布的 NamespaceQueue/fairshare/HyperNode/DRA scoring 我关注了" |

> **面试点（时效性加分，主动讲）**：
> ① Volcano 1.16 的 **NamespaceQueue**（租户在自己 namespace 内管理队列层级与配额，管理员授权资源池）——与 MCK 的 `mck-<tenantId>` 租户 namespace 模型**天然对齐**，比原方案的"池级 Queue + hierarchy 子队列"更贴，方案用语直接升级；
> ② **HyperNode**（独立部署的拓扑控制器）给多机训练拓扑亲和提供了上游件；
> ③ HAMi 2.10 把 DRA driver 独立成单独仓库（如 ascend-dra-driver），Volcano 1.16 加入 DRA scoring —— **两个项目都在向 K8s DRA 收敛**，"演进到 DRA"从此有据可依；
> ④ HAMi 2.10 新增 PodGroup/gang 支持说明上游自己也在解 gang 问题——路线 B 的劣势在上游收敛中，但官方整合路线仍是 volcano-vgpu。

### 9.1 三层深度模型

| 层 | 要求 | 判断标准 |
|---|---|---|
| **L1 方案层（必须）** | HAMi：组件拓扑（scheduler / device-plugin / HAMi-core / vGPUmonitor）、两条整合路线、资源名（`volcano.sh/vgpu-*` vs `nvidia.com/gpu`）、隔离等级（应用态 hook / MIG / 独占） | 能画出整合架构图；能讲"一条 Pod 从提交到拿到卡"的完整数据流 |
| **L2 机制层（防御纵深）** | HAMi：注解流转（`hami.io/vgpu-devices-to-allocate` → `allocated`）、NodeLock 语义、配额下发（env + LD_PRELOAD）；Volcano：session 调度循环、capability/deserved/weight 各管什么、reclaim vs preempt 区别、Pending 原因可观测性 | 被钻"它怎么实现的"能答到字段/流程级；报不出源码行号没关系 |
| **L3 源码层（一口深井即可，不求通读）** | 选 1–2 个点打穿（建议：HAMi scheduler 的 bind 流程，或 device plugin 注册/上报机制——本地有 2.6.1 源码，成本最低） | 被问"读过源码吗"，能讲出 1 个具体机制到源码级 |

**不需要达到的**：Volcano 全部插件源码通读、contributor 级理解、生产调参经验——被问到就用 §7 POC 清单接。

### 9.2 自查五问（全部能答 = 够格讲方案）

1. 一条 GPU Pod 从 kubectl apply 到容器内拿到 `CUDA_VISIBLE_DEVICES`，中间经过哪些组件，哪些注解被写入、谁写的？
2. PodGroup minAvailable=8 而可用卡只有 5 时，Volcano 的行为链（enqueue→Pending→事件原因）？
3. 申请 vgpu-memory 3G 的容器试图分配第 4G 时发生什么？在哪一层拦截？绕过 CUDA API 会怎样？
4. Queue 的 capability / deserved / weight 各自决定什么？reclaim 什么时候触发？
5. device plugin 挂了，存量 Pod / 新建 Pod 各是什么行为，你的对账器会看到什么？

---

## 10. 未确认项（面试不要编造，被追问就说"这块我没深入/没验证"）

1. **Volcano vGPU 的精确版本边界**：HAMi 官方文档只写 "volcano > 1.9"；具体哪个 minor 引入 `deviceshare.VGPUEnable` 参数名未逐版本核实（本地无 Volcano 源码）。最新 v1.16.0（2026-10-14）远超该边界，POC 前钉死版本。
2. **Node capacity 字段名**：官方文档示例里 Node 上报的是 `volcano.sh/gpu-number/gpu-memory`，Pod 申请用的是 `volcano.sh/vgpu-number` 前缀——两套前缀的对应关系需在 POC 时以实际部署为准，文档示例可能与版本有关。
3. **HAMi scheduler 内部形态**：2.6.1 源码显示它是独立二进制（cobra + httprouter，含 webhook 与 extender 路由），但与 kube-scheduler framework 的确切关系（fork 还是纯 extender）未逐行确认——面试只说"独立调度器组件 + webhook 机制"，不深入其内部注册方式。
4. **HAMi-core 隔离的具体覆盖面**（哪些 CUDA API 被拦截、NCCL 路径的实际行为）：未做源码级阅读，只引用官方定位（基于 HAMi-core 的设备共享）。隔离强度结论以 POC benchmark 为准。
5. **云监控计费接口细节**：GPU 计量事件的具体 API/字段未确认（PDF 中 CPU 侧也只是"与云监控 sre 对齐"待办），面试讲"口径设计"不讲"接口已通"。
6. **MCK portal 侧 AI 任务表**：链路 3 产品化阶段才需要，表结构未设计——主动说"MVP 不含"。
7. **超分 PDF 的署名与参与度**：该方案作者是江振瑜，你按"团队方案，我掌握其机制并迁移设计"来讲，除非你实际参与——**参与度你自己最清楚，按实际讲**。
8. **对象存储与缓存组件选型**：L0 用什么（公司存储产品线 / 开源 S3 兼容）、L1 选 JuiceFS/CubeFS/Fluid 系，均未调研定稿——面试讲"选型维度"（S3 兼容性、元数据引擎、私有云部署成本），不讲"已选定"。
9. **Fluid × Volcano 缓存亲和集成的具体机制**（标签协同还是 scheduler plugin）：未核实，POC 项。

---

## 11. 面试前 Checklist

- [ ] §0 电梯版脱稿 30 秒
- [ ] §1 映射表能默写 5 行以上（证明"复用自有资产"）
- [ ] §3.4 CPU→GPU 超分映射表（最有辨识度的一条，能讲 2 分钟）
- [ ] §3.5 存算分离三层含义 + 现状反差（volume 随节点走 = 存算耦合 → "数据不动、算力流动"）
- [ ] §4.4 三条路线的取舍理由，尤其是"官方文档原话收敛选型"
- [ ] §4.5 隔离等级：HAMi-core=应用态 hook，MIG=硬隔离，大训练=独占
- [ ] §5 分期计划的三个"不做什么"
- [ ] §7 四条红线 + POC 清单五条
- [ ] §9 版本现状表 + 三层深度模型 + 自查五问（被问"懂多深"时的标准答案）
- [ ] §10 未确认项记牢
- [ ] 与另外两份材料的比例：真实经历为主（mck/cni），本文讲 5–6 分钟收口
