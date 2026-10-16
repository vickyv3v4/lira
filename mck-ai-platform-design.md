# 马上云 · 智算平台技术方案（落地版）

| 项 | 内容 |
|---|---|
| 方案代号 | MCK-AI |
| 版本 | V1.1 · 设计评审稿（未实施） |
| 日期 | 2026-10-16 |
| 上位输入 | 《马上云智算架构规划图》：LLMOPS / 智算平台 / 马上云底座 三层，图例 = 已具备 / 规划中 / 待确认 / 依赖 |
| 关联文档 | 《基于 MCK+HAMi+Volcano 的智算算力方案设计》（mck-gpu-platform-design.md，下称"算力详设"）——隔离分级/XID 分级/摘卡时序/容量边界等机制细节的专项详设 |
| 本文档定位 | 回答"用了什么技术、怎么部署、怎么配置、数据怎么流"：组件清单+版本、部署拓扑、CRD/配置样例、关键链路实现、分期验收。行业现状与场景推导见算力详设 §1/§2，不重复 |

---

## 1. 方案定位与技术栈总表

**定位**：在马上云底座上，把 GPU/PPU/NPU 异构算力转化为可切分、可调度、可保障、可经营的智算资源服务。全部能力以插件包 + CR 叠加在 MCK 之上，不改 MCK 内核。

### 1.1 技术栈总表（核心组件清单）

| 组件 | 版本基线 | 形态 | 职责 | 部署位置 |
|---|---|---|---|---|
| MCK | 现网 | 产品 | 集群生命周期/节点池/插件体系/监控计量底座 | 全部集群 |
| Volcano | v1.16.0（2026-10-14 发布） | Deployment×3（scheduler/controllers/admission） | 批调度：gang/队列/抢占/reclaim/binpack/设备打分 | 每池集群 |
| HAMi（volcano-vgpu 路线） | v2.10.0（2026-08-21 发布；源码研究基于 2.6.1） | DaemonSet（device-plugin）+ 镜像注入（HAMi-core） | GPU 池化与显存/算力切分，注册 volcano.sh/vgpu-* 扩展资源 | 每池集群 GPU 节点 |
| DCGM-Exporter | v3.x | DaemonSet | GPU 指标与 XID 事件采集 | 每池集群 GPU 节点 |
| RDMA 设备插件 | k8s-rdma-shared-dev-plugin（开源） | DaemonSet | RDMA VF 资源化（rdma/hca_* 扩展资源） | 每池集群 RDMA 节点 |
| Fluid + JuiceFS | Fluid 1.x / JuiceFS 1.2.x | CRD + DaemonSet（缓存）+ CSI | 数据集编排与分布式缓存 | 每池集群 |
| Training Operator（Kubeflow） | 1.8+（2.x 更名 Kubeflow Trainer） | Deployment | PyTorchJob/MPIJob 角色编排 | 每池集群 |
| vLLM | 当期稳定版（PD 分离特性以调研为准） | 容器镜像 | LLM 推理引擎主线 | 业务侧 |
| KEDA | 2.x | Deployment | 推理自定义指标弹性（Prometheus 触发器） | 每池集群 |
| Prometheus + Alertmanager | 2.x | StatefulSet | 指标存储与告警（对接公司监控栈方式待确认） | 管理集群/池集群 |
| Argo Workflows | 3.5+ | Deployment | LLMOPS 流水线执行内核 | 管理集群 |
| MLflow | 2.x | Deployment + MySQL + S3 | 试验跟踪/模型注册内核 | 管理集群 |
| Harbor + Dragonfly | 2.x | 现网 Harbor + 新增 P2P | 镜像仓库与千卡 P2P 分发 | 管理集群 |
| 自研控制面 | — | Deployment | device-manager（设备台账）/quota-controller（潮汐配额）/usage-collector（计量）/event-collector（事件） | 管理集群（带各池 kubeconfig） |
| 推理网关 | 自研（Go） | Deployment | 路由/限流/token 配额/计费埋点 | 管理集群 |

> 版本纪律：仅 Volcano/HAMi 两个版本经源码与官方发布核对过；其余组件版本以 POC 首轮"版本核对"为准（§7 未确认项）。Volcano 与 HAMi 的兼容组合见算力详设 §4.4（HAMi 官方文档：volcano-vgpu 路线不需要安装 HAMi scheduler）。

### 1.2 白板能力 → 技术实现映射总表

| 白板能力 | 实现组件 | 关键配置/CR | 详节 |
|---|---|---|---|
| 异构资源池化 | HAMi volcano-vgpu-device-plugin | 四元组适配矩阵 ConfigMap | §3.1 |
| GPU SHARE | HAMi-core（LD_PRELOAD 显存/算力拦截）；A100 级走 MIG | 资源名 volcano.sh/vgpu-number / vgpu-memory / vgpu-cores | §3.1 |
| 容量配额 | Volcano Queue capability + ResourceQuota | Queue YAML | §3.2 |
| 多队列调度 | Volcano Queue + DRF + reclaim | scheduler conf | §3.2 |
| 特殊设备亲和调度 | 节点卡型标签 + NodeAffinity | 标签规范 gpu.msxf.com/model=A800 | §3.2 |
| 碎片调度 | Volcano binpack 插件 + 小规格作业下沉 | scheduler conf | §3.2 |
| 抢占/优先级 | PriorityClass 三级 + preempt/reclaim 动作 | PriorityClass YAML | §3.2 |
| 潮汐调度 | 自研 quota-controller（watch Nacos 潮汐表 → 改 Queue） | Queue 更新 | §3.2 |
| 在离线混部 | GPU 生产池不混部；CPU 沿用既有超分方案（webhook + 自研 Score 插件） | — | — |
| 弹性伸缩 | KEDA（推理）/ gang 弹性（训练）/ MCK scale 链路（节点） | ScaledObject YAML | §3.5 |
| 分布式缓存 | Fluid Dataset + JuiceFSRuntime | YAML | §3.3 |
| AI 调度增维 | Volcano HyperNode（拓扑）/ numa-aware；中期 DRA（K8s 1.34 GA） | HyperNode CR | §3.2 |
| 故障感知/诊断 | DCGM-Exporter + Prometheus 告警规则 + event-collector | PromRule YAML | §3.6 |
| 故障迁移/自愈 | device plugin 健康摘除（拦新增不杀存量）+ Volcano gang 重调度 + device-manager 状态机 | 摘卡时序 | §3.6 |
| PD 分离推理 | vLLM PD 分离（二期 POC，前置 RDMA） | — | §3.5 |

---

## 2. 马上云上下文与部署拓扑

### 2.1 上下文盘点（每行落到具体集成点）

| 马上云提供 | 状态 | 智算平台的集成点 |
|---|---|---|
| MCK 容器服务 | 已具备 | 集群经创建五阶段交付；GPU 组件以插件包经 cluster_feature 流水线装机；计量复用 MCK 计量模块 |
| ECS/裸金属 | 已具备 | 训练/核心推理 = 裸金属 GPU 直通（无虚拟化损耗）；开发调试 = ECS |
| 多 AZ 多集群 | 已具备 | 分区×卡型 → 一池一集群（poolCode），元集群纳管；不做跨池统一调度（见算力详设 §4.8 容量边界） |
| AI 插件集成 | 机制已具备 | §2.3 插件包规范 |
| 设备网络拓扑 | 规划·P0 | 底座上报卡拓扑（NVLink/PCIe，nvidia-smi topo 类数据）+ 网拓扑（HCA 所在交换层）→ 拓扑 API → HyperNode 建模 |
| RDMA 网络 | 规划·P0 | RoCE 无损（PFC/ECN，交换机侧，底座负责）；主机侧 VF 资源化（§3.4） |
| 分布式/对象存储 | 依赖 | S3 兼容对象存储 = L0；池内 JuiceFS 缓存 = L1；Dataset/PVC = L2（算力详设 §4.6） |
| GPU/PPU/NPU | 依赖 | 四元组矩阵逐卡型准入；国产卡一期只做独占不承诺切分 |

### 2.2 部署拓扑

```
管理集群（MCK 纳管）
 ├─ 自研控制面: device-manager / quota-controller / usage-collector / event-collector
 ├─ LLMOPS 内核: Argo Workflows / MLflow / 模型仓库元数据服务 / 推理网关
 └─ 监控: Prometheus(远端存储) / Alertmanager / 对接公司云监控(方式待确认)
        │ kubeconfig（只读+CR 写入）
        ▼
GPU 池集群 ×N（一池一集群：分区×卡型）
 ├─ 调度: volcano-scheduler / volcano-controllers / volcano-admission
 ├─ 作业: Training Operator（PyTorchJob/MPIJob）/ KEDA
 ├─ 数据: Fluid（Dataset CR）+ JuiceFSRuntime 缓存节点 + CSI
 └─ 节点侧 DaemonSet: volcano-vgpu-device-plugin / DCGM-Exporter / rdma-shared-dev-plugin
```

原则：**调度与节点组件随池集群一体交付（插件包），跨池服务集中在管理集群**——单池故障不扩散，组件升级按池灰度。

### 2.3 MCK 插件包规范（AI 套件）

一套 AI 插件包 = chart + CRD 清单 + values（卡型/版本矩阵参数）+ cluster_feature 接入，池集群创建时按 values 自动装机：

| 插件包 | 内容 |
|---|---|
| volcano | volcano 三组件 + scheduler conf ConfigMap + Queue 初始化（按池参数） |
| hami-vgpu | volcano-vgpu-device-plugin DaemonSet + hook-config + 四元组矩阵 ConfigMap |
| ai-observe | DCGM-Exporter + Prometheus 告警规则 + 事件上报端点 |
| ai-data | Fluid CRD + JuiceFSRuntime 模板 + 对象存储挂载参数 |
| ai-rdma | rdma-shared-dev-plugin + NIC 白名单配置（依赖底座 RDMA 就绪） |

---

## 3. 智算平台层实现

### 3.1 GPU 池化与切分（HAMi volcano-vgpu 路线）

**组件与机制**：每 GPU 节点跑 volcano-vgpu-device-plugin（DaemonSet），经 k8s device plugin API 注册扩展资源；Pod 请求 `volcano.sh/vgpu-number`（卡数）、`volcano.sh/vgpu-memory`（显存额度，MB）、`volcano.sh/vgpu-cores`（算力比例，可选）。运行时隔离由 HAMi-core 完成：设备插件向 Pod 注入 LD_PRELOAD 库，拦截 CUDA 调用做显存计量与限制——**应用级隔离，强度低于硬件级，这是 L-vGPU 定级的前提**（隔离分级与 MIG 边界见算力详设 §4.2）。

**卡型准入（四元组适配矩阵，ConfigMap 下发）**：

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: gpu-adapt-matrix
  namespace: mck-ai-system
data:
  matrix.yaml: |
    cardTypes:
    - model: 4090D
      driver: ">=550"          # 驱动主版本
      os: [kylinV10, openEuler, ubuntu20.04]
      shareMode: vgpu          # 可切分
      ecsPassthrough: true
    - model: A800
      driver: ">=535"
      shareMode: [vgpu, mig]   # 双模式，按节点池定
    - model: H200
      driver: ">=550"
      shareMode: [vgpu]
    - model: 910A
      driver: "CANN-7.x"       # 国产卡按 CANN 版本轴
      shareMode: exclusive     # 一期只做独占
```

**切分定级落地**：MCK 节点池创建时按卡型打标签（`gpu.msxf.com/model=A800`、`gpu.msxf.com/share=vgpu|mig|exclusive`），device-plugin 按 pool values 决定注册 vGPU 资源还是 MIG 资源；**同一节点池内切分模式固定，禁止原地变更**（算力详设 §4.1.4 兼容性治理）。

**作业侧请求样例**（vGPU 切分，1 卡 8GB 显存额度）：

```yaml
resources:
  limits:
    volcano.sh/vgpu-number: "1"
    volcano.sh/vgpu-memory: "8192"
```

### 3.2 调度体系（Volcano v1.16）

**部署**：volcano-scheduler 以独立调度器部署，智算作业 Pod 显式 `schedulerName: volcano`，**默认调度器不动**——通用 CPU 负载不受影响（不改 MCK 内核原则的落点）。

**调度器配置**（scheduler conf，参数以 v1.16 文档校准）：

```yaml
actions: "enqueue, allocate, backfill, reclaim, preempt"
tiers:
- plugins:
  - name: priority        # PriorityClass 排序
  - name: gang            # gang 满足才放行（整组入队）
  - name: conformance
- plugins:
  - name: overcommit
  - name: drf             # 队列间主导资源公平
  - name: predicates
  - name: deviceshare
    args:
      VGPUEnable: true    # vGPU 设备分配打分
  - name: binpack         # 装箱填满，压碎片
  - name: nodeorder
```

**队列体系**（每池 4 条 Queue，随插件包初始化）：

```yaml
apiVersion: scheduling.volcano.sh/v1beta1
kind: Queue
metadata:
  name: pool-a800-train
spec:
  weight: 3                    # 队列间共享权重
  reclaimable: true            # 允许借出资源被回收
  capability:                  # 队列上限（配额中心/潮汐控制器下发）
    nvidia.com/gpu: 512
  deserved:                    # 保障额度
    nvidia.com/gpu: 256
```

| 队列 | 优先级（PriorityClass） | 用途 |
|---|---|---|
| pool-*-infer | prod-inference（1000000） | 生产推理，最高优，不被抢占 |
| pool-*-train | prod-training（800000） | 生产训练，可向开发队列 reclaim |
| pool-*-dev | dev-debug（100000） | 开发调试，可被回收（作业挂起保 ckpt） |
| pool-*-guard | prod-inference | 底座保障类系统作业 |

**各能力实现落点**：
- **抢占/归还**：preempt 动作 + PriorityClass 三级；潮汐借还由 quota-controller 编排（§下方）。
- **碎片调度**：binpack 插件优先填满已用节点；小于 2 卡的碎片只放行 dev 队列小规格作业（准入策略由 quota-controller 校验），维护窗口做节点级整合。
- **特殊设备亲和**：作业按卡型提交 `nodeSelector: gpu.msxf.com/model: 910A`；国产卡/CANN 版本亲和同轴。
- **拓扑增维**：底座拓扑 API 就绪后，按交换层建 HyperNode（`hypernode.volcano.sh`），多机训练优先同 HyperNode 内分配，跨层降级需 dev 队列审批。

**潮汐借还链路（具体实现）**：

```
Nacos 潮汐表（如 22:00 dev 队列 capability: gpu 0 → train +256）
  → quota-controller（自研 Go，watch Nacos）更新 Queue.spec.capability/deserved
  → Volcano reclaim 动作回收 dev 队列超额资源
  → dev 作业收到 SIGTERM（PreStop hook 执行 ckpt 保存）
  → 作业挂起（Volcano Job 状态 Pending 保留 PodGroup）
次日凌晨反向：dev 队列 capability 恢复 → 挂起作业自动重新入队
```

### 3.3 数据与缓存（Fluid + JuiceFS）

```yaml
apiVersion: data.fluid.io/v1alpha1
kind: Dataset
metadata:
  name: sft-corpus
spec:
  mounts:
  - mountPoint: s3://obs-ai/corpus/sft/    # L0 对象存储
    name: sft
---
apiVersion: data.fluid.io/v1alpha1
kind: JuiceFSRuntime
metadata:
  name: sft-corpus
spec:
  replicas: 3                               # 缓存集群规格
  tieredStore:
    levels:
    - mediumtype: ssd
      quota: 512Gi
```

训练 Pod 挂 `pvc://sft-corpus` 即完成 L0→L1 透明加速；Fluid 注入数据亲和信息，配合 binpack 优先调度到缓存命中节点。ckpt 写路径：本地 NVMe → 异步上传对象存储（训练框架承担，平台保证 PVC 与对象存储联通性）。

### 3.4 RDMA 网络（P0 依赖项）

- **一期落地**：k8s-rdma-shared-dev-plugin（开源）将 RDMA VF 注册为扩展资源（`rdma/hca_shared`），作业请求 `rdma/hca_shared: 1` + `nodeSelector` 选 RDMA 节点池；NCCL 经 VF 通信，GPUDirect RDMA 需 IOMMU/vfio 配置验证（POC 项）。
- **演进（复用 lira CNI 模式）**：自研 RdmaNic CRD + 状态机（Allocatable→Assigned→Isolated）+ 节点 daemon + 调度 Filter 扩展（NIC 配额校验）——与 PodENI 同构，获得单 NIC 粒度的分配与隔离能力，支撑 PD 分离的 KV 传输 QoS。
- **交换机侧**（底座团队）：RoCE 无损参数（PFC/ECN）按 Mellanox 官方基线配置；IB 网络按高端池规划。

```yaml
# RdmaNic CRD 设计示例（演进项）
apiVersion: network.lira.msxf.com/v1alpha1
kind: RdmaNic
status:
  nodeName: gpu-node-17
  pf: ens3f0
  vfIndex: 7
  state: Assigned        # Allocatable / Assigned / Isolated
  owner: pod:train-7w2xp
```

### 3.5 推理服务实现

**一期形态**（Deployment + KEDA，不引入 KServe）：

```yaml
# vLLM Deployment 关键段
spec:
  template:
    spec:
      containers:
      - name: vllm
        image: harbor.msxf.com/ai/vllm:0.x-tp1
        args: ["--model", "/models/Qwen2.5-32B-Instruct", "--max-model-len", "32768"]
        resources:
          limits:
            volcano.sh/vgpu-number: "2"
            volcano.sh/vgpu-memory: "45056"
        startupProbe:            # 冷启动门禁：模型加载+warmup 完成才进负载
          httpGet: {path: /health, port: 8000}
          failureThreshold: 60   # 配合 PVC 模型缓存，容忍分钟级冷启动
          periodSeconds: 10
---
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: vllm-poweragent
spec:
  scaleTargetRef: {name: vllm-poweragent}
  minReplicaCount: 2
  maxReplicaCount: 16
  triggers:
  - type: prometheus
    metadata:
      serverAddress: http://prometheus.monitoring:9090
      query: max(vllm:num_requests_waiting{deployment="vllm-poweragent"})
      threshold: "8"          # 排队深度触发；GPU util 不作信号（算力详设 §1.2.3）
```

伸缩信号清单：排队深度、KV cache 占用率、TTFT P95（vLLM /metrics 暴露，指标名以版本核对）。**节点级弹性**走 MCK 扩容链路（scale playbook），池内 KEDA 只做副本级。

**PD 分离（二期 POC）**：vLLM 主线方案（Prefill/Decode 独立实例 + 网关按角色路由），KV 传输依赖 RDMA；POC 前置项 = §3.4 RdmaNic 或 VF 资源化就绪。

**推理网关（自研 Go，模块清单）**：上游选择（按部署排队深度/TTFT 上报加权）、租户 AK/SK 认证（复用公司 IAM，接口待确认）、token 配额限流（Redis 令牌桶）、计费埋点（usage event → Kafka）、灰度分流（按租户/比例）。通用南北向入口保持 Nginx-Ingress，网关只承载推理语义。

### 3.6 监控与故障保障实现

**指标采集**：DCGM-Exporter（GPU util/显存/温度/ECC/功耗/XID）+ vLLM /metrics + Volcano（PodGroup/Queue 水位）+ Fluid（缓存命中率）。series 容量按分层模型规划（算力详设 §4.8）。

**XID 告警与分级**（Prometheus Rule，指标名以 dcgm-exporter 版本核对）：

```yaml
- alert: GpuXidDeviceLevel
  expr: increase(DCGM_FI_DEV_XID_EVENTS{model=~"A800|H200"}[5m]) > 0
  labels: {severity: device}        # XID 13/48/63/64 → 设备级
- alert: GpuXidAppLevel
  expr: increase(DCGM_FI_DEV_XID_EVENTS{...}[5m]) > 0
  labels: {severity: app}           # XID 31/43/45 → 应用级，只杀作业不摘卡
```

**掉卡闭环的工程机制**（对应算力详设 §4.5.1 时序）：

| T | 动作 | 承担组件 |
|---|---|---|
| T+0 | XID 事件 → Prometheus 告警 | DCGM-Exporter/Alertmanager |
| T+1min | event-collector 消费告警 → device-manager 台账置"降级" | 自研 |
| T+2min | **坏卡摘除**：device-plugin 经 nvml 健康检查把坏卡从 allocatable 摘除——新 Pod 拿不到该卡，存量 Pod 不杀（"只拦新增不杀存量"由 device plugin API 原生保证） | volcano-vgpu-device-plugin |
| T+3~5min | 训练：watchdog 判定 NCCL 失败 → 作业按重试编排重新入队，Volcano gang 调度到健康卡；推理：Deployment 重建副本 + 网关健康检查摘流 | Training Operator/Volcano/网关 |
| T+10min | 工单创建 → 状态机"降级→隔离→维修"；节点级故障才打 taint | device-manager |
| 回归 | 维修后 dcgm diag + nccl-tests 单卡/P2P 验证通过 → 台账回"在线"，恢复分配 | device-manager |

**训练重试编排（模板默认值）**：maxRetry=3，指数退避 5/15/30min，超限挂起等待人工；ckpt 每 30min（大模型）/10min（小模型）落对象存储——恢复成本上限 ≈ 256 卡时/次（512 卡池，算力详设 §4.5.2）。

**推理保障**：startupProbe 门禁 + 发布用 surge 策略（从水位起量）+ KV cache/TTFT 驱动 HPA（§3.5）。

### 3.7 计量与经营实现

- **注解规范**：所有智算作业强制携带 `mck.msxf.com/billing-tag: dept=,app=,env=`（quota-controller 准入校验，缺注解拒绝入队）。
- **采集**：usage-collector watch 各池 Pod/Job 完成事件（只读 kubeconfig）→ 生成 usage event（申请量口径为主，用量经营为辅，算力详设 §4.7）→ Kafka → 计费系统（对接点待确认）。
- **推理计量**：网关按 token 用量埋点（vLLM usage 响应字段），离线批量按作业申请量。
- **对账**：5min 三方（设备台账↔调度记录↔计量事件）+ 1h 水位核对，差异进工单（复用 MCK 对账模式）。

---

## 4. 与 LLMOPS 层的对接契约

| 契约 | 具体形态 |
|---|---|
| 训练提交 | Argo Workflow step 创建 PyTorchJob（queue/billing 注解 + Fluid PVC 挂载 + PriorityClass） |
| 推理发布 | Argo Workflow step 创建/更新 vLLM Deployment + Service + KEDA ScaledObject（二期换 InferenceService） |
| 数据资产 | 模型/数据集 = 对象存储路径 + 元数据 API；训练侧统一经 Fluid Dataset 声明 |
| 配额 | LLMOPS 需求审批通过 → 调用 quota-controller API 写 Queue capability |
| 计量/经营 | usage event 双写 LLMOPS 成本看板与计费系统 |

---

## 5. 端到端链路（组件级走查）

**链路 A · 供给（卡→可调度算力）**：马上云交付 GPU 节点 → MCK 纳管（基线预检：驱动/内核/OS 对四元组矩阵）→ AI 插件包装机 → device-plugin 注册扩展资源 → Queue capability 更新 → 池可售。

**链路 B · 运行（提交→计费）**：Argo 提交 PyTorchJob（schedulerName=volcano + queue 注解）→ Training Operator 生成 Pod → Volcano enqueue（gang 校验 PodGroup minMember）→ allocate（predicates → deviceshare/binpack 打分 → bind）→ kubelet：lira CNI 分网 + HAMi hook 注入 LD_PRELOAD + Fluid CSI 挂缓存卷 → 运行（DCGM/vLLM 指标上报）→ 完成 → usage-collector 计量 → 产物注册模型仓库。

**链路 C · 故障（XID→回归）**：见 §3.6 掉卡闭环表，SLO：摘卡决策 ≤1min、训练恢复 ≤15min、推理重建 ≤5min（设计值，演练验证）。

---

## 6. 分期落地与验收标准

| 期 | 交付物 | 验收标准（可测） |
|---|---|---|
| 一期（已投产） | 4090/4090D 池 + volcano-vgpu 切分 + 基础引擎镜像 | 现网已验收 |
| 二期 | 四元组矩阵扩 A800/H200/910A；纳管四步法；Volcano 队列体系；Fluid；保障最小闭环；LLMOPS 最小集 | ①三卡型预检通过并投产 ②512 卡 gang 作业 P90 入队 ≤2min（设计值）③reclaim 演练：借出资源回收时 dev 作业 ckpt 挂起成功 ④缓存命中后训练启动时间较直连对象存储下降 ≥50%（量级目标）⑤故障注入演练：XID → 10min 内完成隔离+重调度 |
| 三期 | 潮汐/抢占/增维启动；摘卡自动化；经营/成本/AK-SK；推理网关；PD 分离 POC | 潮汐表自动借还演练；摘卡全自动闭环（无人工 kubectl）；token 计量对账差异 ≤1%（设计值） |
| 四期 | 千卡规模化；HyperNode 拓扑调度全面化；多 AZ | 千卡单池容量演练（算力详设 §4.8 边界） |

POC 清单（先于二期立项，见算力详设 §7）：Volcano+HAMi 兼容组合验证、hang 检测与 gang 重调度演练、摘卡自动化脚本化、Fluid×Volcano 数据亲和收益、910A 适配验证。

---

## 7. 风险与未确认项

**风险**：开源组件版本矩阵碎片（Volcano/HAMi/vLLM/Fluid 升级节奏不一）→ 插件包 values 固定版本组合 + 固定升级窗口；国产卡栈成熟度 → 逐卡型准入不承诺切分；RDMA/存储底座进度 → P0 清单跨团队对齐，强依赖项排 POC 后。

**未确认项（POC 首轮核对）**：
1. MCK 现网 K8s 版本与各组件兼容性（Volcano v1.16/HAMi v2.10/Fluid 1.x 对 K8s 版本要求）。
2. 公司监控栈与 Prometheus 的对接方式（categraf/云监控/自建 Prometheus 共存架构）。
3. dcgm-exporter 当期版本 XID 指标名与告警规则字段。
4. vLLM 当期版本 /metrics 指标名与 PD 分离特性状态。
5. 计费系统对接接口（usage event 消费方式）与公司 IAM/AK-SK 接口。
6. cluster-autoscaler 与 MCK 扩容链路的集成方式（当前按 MCK scale playbook 人工/工单触发设计）。
7. Training Operator 当期名称与版本（1.x/2.x 更名 Kubeflow Trainer）及其与 Volcano gang 的集成参数。
8. Volcano Queue capability 对扩展资源的支持边界、binpack/deviceshare 参数项，以 v1.16 文档校准。
9. GPUDirect RDMA 的 IOMMU/vfio 主机配置与性能验证。
10. PPU 指代与国产卡推理/训练栈（白板判读待与原图核对）。
