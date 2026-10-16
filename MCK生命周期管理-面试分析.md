# MCK（托管 Kubernetes 容器云）生命周期管理 —— 源码分析与面试准备

> 基于 gc-mck-ctl / gc-mck-plt / gc-mck-portal / gc-mck-res 四仓库源码分析（2026-10）。
> 用途：智算云 PaaS 平台架构师面试准备。文中 `仓库/路径:行号` 为代码出处，可作为"我读过源码"的证据。

---

## 1. 项目全景

### 1.1 一句话定位

MCK 是金融行业私有云上的**托管 Kubernetes 服务**（对标 ACK/Rancher 的自研实现）：用户通过订单系统购买集群，平台自动完成**云主机开通 → K8s 集群部署（Ansible/Kubespray）→ 监控组件下发 → 计费/履约闭环**，并以声明式 CR + Operator 模式管理集群的**创建、扩缩容、升级、删除、健康巡检**全生命周期。

> MCK 全称在代码中未显式展开（README 为模板），从语境推断为 "Managed Cloud Kubernetes"。面试时表述为"我们内部的托管 K8s 产品代号 MCK"即可，不要强行展开。

### 1.2 四层架构

```
┌─────────────────────────────────────────────────────────────────────┐
│  订单系统 / 前端 Portal / 云监控（外部系统）                            │
└──────────────┬──────────────────────────────────────────────────────┘
               │ REST / Kafka(order.topic.status-change 等)
┌──────────────▼──────────────────────────────┐  ┌───────────────────┐
│ gc-mck-portal（Java，P 层·编排调度器）          │  │ gc-mck-plt(Java)  │
│ · 订单事件驱动 + Task 状态机(MySQL task 表)     │  │ 后管运维查询/授权   │
│ · 调 ECS/VPC/GNAT/KMS 开通云资源(前管)         │  │ (只读代理+RAM授权) │
│ · 生成 hosts/vars ConfigMap 内容 → 下发 R 层   │  └────────┬──────────┘
│ · 接收 ctl 回调闭环、失败回滚、22个兜底任务       │           │ 只读
└──────────────┬──────────────────────────────┘           │
               │ HTTP(签名鉴权) /resource/mck-service/v1/*  │
┌──────────────▼──────────────────────────────┐           │
│ gc-mck-res（Go，R 层·资源落库代理）              │◄──────────┘
│ · client-go 多集群客户端(按 poolCode, gcache)   │
│ · 落 Namespace/SA/RBAC/ConfigMap/Secret/DaemonSet│
│ · 创建 Cluster / ClusterOperation CR           │
└──────────────┬──────────────────────────────┘
               │ K8s API（管理集群=资源池 pool）
┌──────────────▼──────────────────────────────┐
│ gc-mck-ctl（Go，Operator·执行面，kubean 血统）    │
│ · watch 双 CR → 生成 entrypoint.sh → K8s Job    │
│ · Job 内 ansible-playbook + kubespray fork      │
│   (cluster.yml/scale.yml/remove-node.yml/…)     │
│ · finalizer + 回调 portal 更新状态               │
└──────────────┬──────────────────────────────┘
               │ SSH/Ansible
┌──────────────▼──────────────────────────────┐
│ 工作集群机器（ECS 虚机：3 master + N worker）      │
└─────────────────────────────────────────────┘
```

**多集群模型**：cboot 平台的"资源池"（pool，如 `16-C001-pool001`）= **管理集群**；MCK 在其上用 Ansible 部署的集群 = **工作集群**。res 按 poolCode 维护多套 kubeconfig（gcache 缓存 5 分钟），工作集群 kubeconfig 存放在管理集群的 `{clusterId}-kubeconf` ConfigMap 中。

### 1.3 四仓库职责与技术栈

| 仓库 | 角色 | 技术栈 | 关键事实 |
|---|---|---|---|
| **gc-mck-portal** | P 层：编排调度器（用户面） | Java 11 / Spring Boot 2.3 / MySQL(gcloud_mck) / Kafka / Redisson / Sentinel / Nacos / Vault | 订单事件驱动，10 张表，22 个 `@CloudTask` 兜底任务，10 个下游 HTTP 客户端，无 K8s 直连（fabric8 仅作 DTO） |
| **gc-mck-plt** | 后管面：查询+授权网关 | Java 8-11 / Spring Boot / MyBatis-Plus / Sentinel | 只读共享库 `cluster` 表 + 代理 CR YAML 查看（经 res）+ RAM 授权校验，**无写操作、无 MQ** |
| **gc-mck-res** | R 层：K8s 资源落库代理 | Go 1.20 / gin / client-go v0.24 / controller-runtime(仅 CRD 类型) / 自研注册中心 | 无数据库，状态全在 K8s；多集群 client 管理；为 ctl 建 SA+RBAC |
| **gc-mck-ctl** | 执行面：集群生命周期 Operator | Go 1.17 / Kubebuilder v3 / controller-runtime v0.11 / kubespray v2.25.0 fork | 双 CRD（`mck.msxf.com`），设计移植自开源 **kubean**；Job 跑 Ansible；回调 portal |

调用链总览：**portal（编排/写）/ plt（展示/读）→ [cloud-gateway 签名鉴权] → res（落 K8s 资源）→ K8s CR → ctl（Operator 调谐）→ K8s Job（Ansible）→ 工作集群机器**；ctl 通过 `/noauth/mck-service/v1/cluster/update|delete` 回调 portal 闭环。

---

## 2. 核心设计抽象（面试必讲）

### 2.1 双 CRD：目标态与操作分离（kubean 血统）

CRD group `mck.msxf.com/v1`，shortName `c` / `cp`：

**① Cluster（目标态，一个集群一个 CR）** — `gc-mck-ctl/api/v1/cluster_types.go:28-46`
- `clusterType`（如 `ProprietaryKubernetes`）、`clusterAlias`
- `platformInfo`：region/azone/resourcePoolId/tenantId/cloudAccountId（多租户+多资源池）
- `hostsConfRef` / `varsConfRef` / `kubeConfRef` / `preCheckRef`：**DataRef{Namespace,Name}** 指向 ConfigMap——hosts.yml 是 Ansible inventory（kube_control_plane/kube_node/etcd 分组），group_vars.yml 是 K8s 版本/网络(calico)/容器运行时/镜像仓库等全量变量
- `timeoutMins`（默认 60）→ Job 的 ActiveDeadlineSeconds
- `status.phase`：`creating / running / failed` + conditions（记录每次由哪个 ClusterOperation 产生）

**② ClusterOperation（操作态，一次变更一个 CR）** — `gc-mck-ctl/api/v1/clusteroperation_types.go:33-62`
- `cluster`（目标集群）、`image`（spray-job 执行镜像）
- `clusterAction` + `preHook[]` + `postHook[]`（ActionInfo：`playbook|shell`，支持 ConfigMap 注入自定义 playbook）
- `hostsConfRef/varsConfRef`（Operator 从 Cluster **快照复制**）、`entrypointSHRef`（Operator 生成）
- `status`：opsStatus（`Running/Succeeded/Failed/Scaling/ScaleSucceeded/ScaleFailed`）、jobRef、`digest`（spec MD5 防篡改）、`hasModified`

**面试话术**：这是典型的 **"目标态(CR) + 操作流(CR)"分离**模式：Cluster 声明"要什么"，ClusterOperation 声明"做什么"，一次操作一个 CR 天然带出了**审计记录、幂等边界、并发隔离**三重能力。设计移植自开源 kubean（`entrypoint_test.go` 版权头 `kubean-io`），我们做了私有化改造。

### 2.2 三层状态机分离

| 层 | 载体 | 状态 |
|---|---|---|
| K8s CR 层 | Cluster.status.phase | creating → running / failed（扩缩容失败不影响集群 phase，见 `cluster_controller.go:179-188`） |
| K8s CR 层 | ClusterOperation.status | (空)→Running/Scaling→Succeeded/Failed/ScaleSucceeded/ScaleFailed |
| 业务 DB 层 | portal `cluster` 表 clusterStatus | initial → creating → running/failed；scaling_out→scale_out_failed→scale_out_rolling_back；scaling_in→scale_in_failed；stopping→Stopped；deleting→k8s_deleted→deleted；unavailable |
| 任务层 | portal `task` 表（main/sub 两级） | creating/executing/done/failed + 过程态（k8s_node_deleted、scale_in_rolling_back、running_wait_send_pay_notice…） |

**驱动方式**：portal 靠 **Kafka 事件（订单/ECS/组件）+ 22 个 @CloudTask 兜底定时任务**；ctl 靠 **周期 requeue（cluster 15s / ops 3s / job 5s）轮询 Job conditions**——不依赖 informer 事件推送，实现简单、收敛可预期。

### 2.3 数据引用模式（ConfigMap 即 Inventory）

集群的"部署输入"（hosts.yml、group_vars.yml）不在 CR 里，而在 ConfigMap 里，CR 用 DataRef 引用。好处：
1. CR 保持轻量，大文本（inventory 可含几百节点）不进 etcd 通知风暴；
2. **快照隔离**：每次操作前 Operator 把 CM 复制一份带时间戳后缀的副本挂给本次 Job（`BackUpDataRef`, `clusteroperation_controller.go:508-557`），并发操作互不干扰；
3. 变更可审计：CM 有 ownerReference 级联 GC。

---

## 3. 生命周期全流程

### 3.1 创建集群（最完整的主干流程，面试重点讲）

**阶段一：下单同步校验 + 预创建（订单系统 → portal）**
```
POST /portal/mck-service/web/v1/cluster/checkClusterCreate
 → ClusterCreateCheckHandler → OrderCreateHandlerService.concurrentHandleOrderCreateCheck
    ├ 限购校验：一次≤10套、CPU≥2核/内存≥4G、master/worker≤10
    ├ 商品项解析：cluster/master/worker/master-volume/worker-volume
    ├ 网络校验：pod CIDR 8~24、service CIDR 16~24、三者不重叠
    ├ 预生成 clusterId，ECS preCreate 预占资源
    ├ 生成主任务 cluster_create + 子任务(master_ecs_create/worker_ecs_create/…)
    │   批量落 task/node/volume 表，资源上报资源中心
    └ 返回订单回调响应
```
出处：`OrderCreateHandlerService.java:389-456, 965-1069`。

**阶段二：订单 Kafka 事件 → 落库 + 正式开通云资源**
```
Kafka order.topic.status-change → OrderEvent.eventOrderCreate → handleOrderCreate
    ├ Redisson 分布式锁 order:{orderId} 防重
    ├ cluster 表落库：state=creating / clusterStatus=initial
    │   namespace = mck-{tenantId}（租户隔离），密码 KMS 加密
    ├ 建 VPC 安全组、GNAT SNAT 绑 VPC
    └ 正式调 ECS createEcsAndRetry 开通 master/worker 虚机
```
出处：`OrderCreateHandlerService.java:184-272, 1308-1353`。

**阶段三：ECS 就绪事件**
```
Kafka ecs.topic.status-initInstances → EcsEvent
    ├ 更新节点任务 done、建 DNAT（端口映射→SSH 通达）、落 node_network 表
    └ 全部子任务 done 后由兜底任务 TaskCreateCluster 推进主任务
```
出处：`EcsEvent.java:123-193`、`TaskCreateCluster.java:46-60`。

**阶段四：下发 R 层（声明式部署输入）**
```
主任务推进：clusterStatus initial→creating，发布事件中心 mckResourceOkCallRes
 → Kafka mckevent.topic.mckResourceOkCallRes 回环自消费 → ConsumerBySelf
    ├ 用 node/node_network 表生成 hosts ConfigMap（Ansible inventory）
    ├ freemarker 模板渲染 vars ConfigMap（group_vars）
    └ POST {mck-res}/resource/mck-service/v1/cluster/create
```
res 侧在**管理集群**依次落：Namespace → `gc-mck-ctl` SA + ClusterRoleBinding（按 namespace 授权 Operator）→ hosts/vars ConfigMap → **Cluster CR** → **ClusterOperation CR（`{clusterId}-ops`，action=cluster.yml + pre/post hooks）**。出处：`ConsumerBySelf.java:136-170, 332-351`；`gc-mck-res/pkg/biz/cluster_service.go:45-191`。

**阶段五：Operator 调谐（声明式 → 命令式）**
```
gc-mck-ctl ClusterOperationReconciler 流水线（clusteroperation_controller.go:79-235）：
  终态短路 → TrySuspendPod(防双跑) → 校验 DataRef → 补 DDL/ownerRef/digest
  → BackUpDataRef(快照) → 生成 entrypoint.sh(ConfigMap)
  → 创建 K8s Job（BackoffLimit=0，Never，挂载 hosts/vars/entrypoint）
Job 内执行：ansible-playbook -i /conf/hosts.yml -e @/conf/group_vars.yml
            /kubespray/cluster.yml（preHook: ping.yml、disable-firewalld.yml
            postHook: kubeconfig.yml、cluster-info.yml）
```

**阶段六：回调收尾 + 后置组件**
```
Job 完成 → Operator 轮询 Job conditions(5s) 置终态 → Cluster controller 汇聚 conditions
 → 推导 phase → **先回调 portal /noauth/.../cluster/update，成功才落盘 phase**
 → portal ClusterCreatingHandlerService：phase=running
    ├ 主任务 DONE，集群置 running
    ├ 发布履约成功事件：订单完成 + 资源中心 RUNNING + 云监控按量计费开始
    └ post-hook：经 res 给工作集群下发 Categraf(DaemonSet)/ServiceAccount/Secret/
       ClusterRoleBinding/kube-state-metrics（checksum 注解强制滚动更新）
    非 running → 主任务 FAILED → 履约失败 + 释放预占资源
```
出处：`cluster_controller.go:143-215`；`ClusterCreatingHandlerService.java:46-88`；`ClusterCategrafCreateHandler.java:77-168`。

另外，`kubeconfig.yml` playbook 会从 master 拉取 kubeconfig → 建 `{clusterId}-kubeconf` ConfigMap → `kubectl patch` 回写 `Cluster.spec.kubeConfRef`——这是 res/portal 后续访问**工作集群**的凭据通道（`gc-mck-ctl/playbooks/kubeconfig.yml:73-92`）。

### 3.2 扩容（scale-out）

```
POST /checkClusterNodeScale → 扩容校验 → 预创建 worker ECS + 主任务 cluster_node_scale_out
 → 订单批量变配事件 → ECS 就绪 → 集群置 scaling_out → 生成新 hosts(inventory 加节点)
 → res POST /cluster/changeNode → 校验 Cluster CR phase==running
 → 建 ClusterOperation：action=scale.yml，extraArgs="--limit=<node1>,<node2>"
 → Operator 起 Job 执行（opsStatus=Scaling）→ 回调 → running
 失败：handleRollBackTask 删 DNAT/删 ECS 回滚（scale_out_rolling_back）
```
出处：`OrderScaleOutHandlerService.java:127-167`；`cluster_service.go:327-414`；kubespray fork 中为 NAT 环境注入了自定义 `skip-gather-ips` role 跳过 fallback_ips 收集（`kubespray/playbooks/scale.yml:20`）。

### 3.3 缩容（scale-in）

```
校验：集群 running、节点网络全为 gnat、缩容后 worker≥1
 → remove-node.yml，extraArgs="-e node=<node>,..."（entrypoint 自动追加 skip_confirmation=true）
 → opsStatus=Scaling → 回调成功后 portal 侧清理：改 hosts CM → 删 DNAT/ECS/安全组
 → 停止监控计费推送
```
出处：`ClusterScaleInCheckHandler.java:58-84`；`TaskForShrinkageServiceImpl.java:212-857`；`entrypoint.go:104-106`。

### 3.4 删除（含 finalizer 保护的关键设计）

```
订单 release 事件 → 主任务删除 → res DELETE /cluster/delete → 删 Cluster CR
 → ctl 捕获 DeletionTimestamp → 先回调 portal /noauth/.../cluster/delete
   （**回调成功才移除 finalizer "storage.mck.manager"，回调失败则保留重试**）
 → portal 释放 IaaS：删 ECS/VPC/安全组/DNAT → 推送监控计费停止
 → CR 真正消失，hosts/vars/kubeconf CM、ClusterOperation、Job 全部经 ownerReference 级联 GC
 节点侧清理：reset.yml playbook（自动注入 reset_confirmation=yes）
```
出处：`cluster_controller.go:86-102`；`TaskForReleaseServiceImpl.java:228-521`；`entrypoint.go:101-103`。

**面试话术**：删除顺序是刻意设计的——**平台侧先确认资源释放逻辑被触发，才放行 CR 删除**，防止"CR 没了但虚机还在计费"的孤儿资源问题；这就是 finalizer 的经典用法。

### 3.5 停止/重启（欠费场景）

欠费/退订/过期 → `OrderArrearsStopHandlerService` → ECS power 停机（`stopping→Stopped`）；充值 → `TaskForRestartServiceImpl` 重启并走回调策略恢复状态。portal 有 7 个 ctl 回调策略 Handler（Creating/ScalingIn/ScalingOut/ScalingInToScalingOut/Restarting/Stopped/Stopping，`CtlRollbackFactory.java:14-25`）。

### 3.6 升级（如实说明现状）

kubespray 侧 playbook 已具备（`upgrade-cluster.yml`、`renew-certs.yml` 证书续期），portal 状态枚举有 `upgrading`，但 **portal 当前无升级编排实现**——面试时如实说"升级链路已预留（playbook + 状态位），编排面尚未开放"，反而显真实。

### 3.7 日常运维与巡检

| 能力 | 实现 |
|---|---|
| 集群健康探活 | `TaskClusterStatusCheck`（@CloudTask）→ Kafka → `ClusterStatusCheckConsumer` 查云监控 kube-apiserver 指标 → running/unavailable 互转；ECS 侧直连节点 `/readyz`（`ResRemoteService.checkEcsHealth`） |
| 子账号 kubeconfig | res 取 SA token Secret → 拼 WorkKubeConfigData → 存 `{subjectId}-secret`，支持签发/吊销（`kubeConfig_service.go:34-165`） |
| 组件(FEATURE)下发 | portal `/noauth/.../feature/upsert` → res `/feature/upsertDaemonset`（dryRun 校验 + checksum 强制滚动）→ Kafka 回报安装状态 |
| CR YAML 查看（后管） | plt → res `/cluster/get|describe`（kubectl 风格 describe，用 kubectl describe 包实现） |

---

## 4. 可靠性设计（面试加分点，逐条都有代码依据）

### 4.1 幂等与防重（四道防线）
1. **Operator 各步骤判空跳过**：JobRef/EntrypointSHRef/digest/ownerRef 已存在即跳过 → reconcile 任意重入安全；
2. **actor-pod 单飞**：发现 Job 出现第二个 running Pod 时 `Suspend=true` 挂起，防止两份 Ansible 同时改集群（`TrySuspendPod`, `clusteroperation_controller.go:670-727`）；
3. **spec 防篡改**：首次 reconcile 写入 `CalSalt()` MD5 摘要，之后比对不一致置 `hasModified=true`（`clusteroperation_types.go:190-208`）；
4. **portal 侧**：Redisson 锁 `order:{orderId}`/`cluster:{clusterId}` + 订单防重复入 task 表 + `hasCompleted=done` 标记"完成后只处理一次"。

### 4.2 级联清理
- Cluster finalizer `storage.mck.manager`：删除前必须成功回调 portal；
- 全链 ownerReference：ClusterOperation→Cluster、Job→ClusterOperation、entrypoint/备份 CM→ClusterOperation、hosts/vars/kubeconf CM→Cluster → 删 Cluster 即全链 GC。

### 4.3 重试与超时体系
- Operator：周期 requeue（15s/3s/5s）+ 错误重试 + `utils.Retry`（3次×300ms）；
- portal：Spring `@Retryable/@Recover`、guava-retrying（3次×300ms）；
- 超时：Job `ActiveDeadlineSeconds`（timeoutMins 默认 60min），Job Suspended 视为 Failed；
- 兜底：22 个 @CloudTask 定时任务扫"卡住的"主任务推进/回滚——**事件驱动的失效保护**。

### 4.4 状态一致性
"**先外后内**"：Cluster phase 只有在成功回调 portal 后才落盘（`cluster_controller.go:198-212`），保证外部可见状态与内部状态最终一致；集群级健康由云监控指标反向校验（running↔unavailable）。

### 4.5 高可用
- ctl `--leader-elect`（DaemonSet 单活实例）；
- 执行层无状态（状态全在 CR/etcd），portal 无状态（状态在 MySQL/Kafka），均可水平扩展；
- res 多集群客户端带 QPS 限流（50/100 burst）+ gcache 缓存，防止打爆 apiserver。

### 4.6 已知短板（面试被追问时的诚实答法）
- controller 未设 `MaxConcurrentReconciles`（默认 1），也未 `Owns()` 监听 Job——靠轮询驱动，收敛较慢但简单可控；
- inventory 中 `ansible_password` 明文存 ConfigMap（有改进空间：Secret + Vault）；
- Operator RBAC 含 `*/*` 通配（为 playbook Job 所需，可收敛为按 namespace 授权——res 实际已做 namespace 级 RoleBinding）；
- portal `upgrading` 未实现；本地缓存（地域/可用区）无过期刷新。

---

## 5. 多租户与安全

| 维度 | 实现 |
|---|---|
| 租户隔离 | 每集群独立 namespace `mck-{tenantId}`；cluster 表 tenant_id + 复合索引；查询双条件（clusterId+tenantId）防平行越权 |
| 授权 | RAM 权限中心：`@CloudResourceAction` 资源操作审计 + `RamAuthClient` 校验（如 CheckYaml 查看 YAML 需授权）；plt 有独立授权 API（operatelist/operate/validator） |
| 凭据 | 密码 KMS 加密落库；Nacos 凭据走 Vault；子账号 kubeconfig（SA token）可签发/吊销 |
| 服务间 | cloud-gateway 注册中心 + 心跳（180s）+ 调用方签名鉴权（`ServiceCallSignValidator`）；noauth 接口仅限内网（ctl/res/云监控回调） |
| 限流熔断 | Sentinel：portal/plt/res 全线接入，`@SentinelResource` 资源点 + 兜底 Handler |

---

## 6. 可观测体系

- **Metrics**：Categraf（监控 agent，post-hook DaemonSet 下发）+ kube-state-metrics → 云监控；portal/plt 业务埋点切面（失败计数）+ Prometheus 端点；
- **健康**：云监控 kube-apiserver 指标探活（集群级）+ 节点 `/readyz`（虚机级）+ Kubernetes healthz/metrics 端点；
- **追踪**：Pinpoint APM（res 内置）、xlog 结构化日志（带请求 UUID）；
- **事件**：K8s Event（Operator 关键失败点 Warning Event）+ 公司事件中心（gcloudevent，落 Kafka 供订阅）。

---

## 7. 与开源/竞品对标（结合 JD 加分）

| 本项目 | 开源对标 | 差异点 |
|---|---|---|
| gc-mck-ctl（双 CR + Job 跑 Kubespray） | **kubean**（Kubespray 生命周期 Operator） | 私有化改造：自研 precheck/kubeconfig/node-label playbook、skip-gather-ips（NAT 环境）、mck-kubeconfig-rbac |
| 部署引擎 kubespray v2.25.0 fork | kubespray | 定制 roles：`mck_csi`（自研 CSI）、`gpu`（GPU Operator + **Volcano** 批调度 CRD）、`network_plugin/lira` |
| 自研 CNI **lira**（分支 f-mck-cni） | 阿里 Terway / AWS VPC CNI | **PodENI 模式**：CRD `podenis.network.msxf.com`（ENI ID/IP/MAC/网关/安全组），Pod 独享弹性网卡，适配金融云 VPC 网络 |
| gc-mck-res 多集群客户端 | Cluster API / OCM 思路 | 轻量实现：按 poolCode 管理 kubeconfig，管理集群-工作集群两级模型 |
| portal 订单驱动编排 | 云厂商控制面 | 订单/计费/履约与 K8s 生命周期打通（金融私有云特色） |
| 监控下发 | Prometheus 生态 | Categraf + kube-state-metrics + 云监控按量计费联动 |

**JD 映射**：K8s 任务编排/operator（ctl）、存储（mck_csi）、网络（lira PodENI/CNI）、调度与弹性（Volcano+GPU 分支）、多集群（pool 模型）、租户/权限/配额（namespace+RAM+订单配额）、可观测（Categraf/KSM/巡检）、灰度与熔断限流（Sentinel、回调策略工厂）。

---

## 8. 面试高频追问（15 问 + 回答要点）

1. **为什么用 CR + Operator，而不是 API 直接调 Ansible？**
   声明式持久化（状态在 etcd，服务重启不丢）、天然重试/审计/级联 GC、与 K8s RBAC 生态兼容；Job 与编排服务解耦，执行失败可观测（Job conditions）可重放（CR 即记录）。

2. **为什么一次操作一个 ClusterOperation CR？**
   并发操作隔离（各自快照 hosts/vars）、完整操作审计（谁在何时对集群做了什么）、幂等边界清晰（一个 CR 一个 Job）、防篡改（digest）。

3. **如何保证 Ansible 任务不双跑？**
   BackoffLimit=0（Job 不自动重试）+ actor-pod 注解单飞（发现第二个 Pod 就 Suspend Job）+ hasCompleted 完成标记 + portal 分布式锁。

4. **删除集群如何防止资源泄漏？**
   finalizer：先回调 portal 触发 IaaS 释放，成功才移除 finalizer 放行删除；CR 内所有派生资源用 ownerReference 级联 GC。

5. **创建链路跨了 6 个阶段，中途宕机怎么办？**
   状态全落库（cluster/task 表），22 个 @CloudTask 兜底任务扫描非终态任务续推或回滚；Kafka 消费幂等（订单防重 + 锁）。

6. **扩缩容失败怎么回滚？**
   portal 任务态机显式回滚态（scale_out_rolling_back），删 DNAT/ECS/安全组；集群 phase 不因扩缩容失败降级（ScaleFailed 不影响 running）。

7. **多集群（数百规模）下 client-go 怎么管理？**
   按 poolCode 建 Clientset/DynamicClient，gcache 5min TTL；工作集群 kubeconfig 从管理集群 CM 惰性加载并缓存；QPS/burst 限流；master IP 漂移兼容（inventory 替换 server 地址）。

8. **为什么状态回写要"先回调成功再落盘"？**
   保证平台侧（DB）与 CR 侧状态最终一致，避免"CR 已 running 但平台还在 creating"导致的重复下发或计费错乱。

9. **PodENI 型 CNI 相比 Calico 的取舍？**
   优点：Pod IP 直属 VPC、与安全组/LB 原生集成、跨节点 flat 网络、性能好；代价：ENI 配额限制节点密度、IP 水位管理复杂。金融私有云要求 Pod IP 可审计可追溯，ENI 模式合适。

10. **GPU 支持怎么做的？**
    kubespray fork 中 `gpu` role：GPU Operator（驱动/device-plugin）+ Volcano（批调度/Gang Scheduling），随集群部署可选开启。

11. **为什么 ctl 用轮询不用 informer 监听 Job？**
    实现简单、语义明确（条件轮询 vs 事件补偿），15s/3s/5s 三档 requeue；代价是收敛延迟，可接受（集群操作本身分钟级）。承认 Owns() + Watch 是更优解。

12. **portal 与 res 为什么拆两层？**
    portal 面向订单/业务（Java 生态、事务、MQ），res 面向 K8s API（Go、client-go 性能与类型）；读写分离（plt 只读走 res）；res 无状态可独立扩缩。

13. **安全上做了什么？**
    签名鉴权、KMS/Vault、namespace 级 RBAC、子账号 kubeconfig 吊销、RAM 资源级授权、Sentinel 限流。坦诚：inventory 明文密码待改进。

14. **和 Rancher/OpenShift 比你们差异？**
    Rancher 偏多集群纳管（接现成集群），我们是**从订单到交付的全托管**（IaaS 联动 + 计费履约闭环）；OpenShift 全家桶重，我们按金融私有云合规裁剪（自研 CNI/CSI 对接自家 VPC/存储）。

15. **这套架构你最想改进的三点？**
    ① ctl 引入 Owns()/Index + 提升并发，轮询改事件驱动；② 凭据全链 Secret/Vault 化（inventory 密码、kubeconfig 轮换）；③ 升级链路落地（playbook 已具备，补编排 + 灰度批次 + cordon/drain 策略）。

---

## 9. 关键代码索引（快速复习）

| 主题 | 位置 |
|---|---|
| Cluster CRD | `gc-mck-ctl/api/v1/cluster_types.go:28-130` |
| ClusterOperation CRD + CalSalt | `gc-mck-ctl/api/v1/clusteroperation_types.go:33-208` |
| Ops 调谐流水线 | `gc-mck-ctl/controllers/clusteroperation_controller.go:79-235` |
| Cluster 调谐/finalizer/回调 | `gc-mck-ctl/controllers/cluster_controller.go:70-215` |
| Ansible 命令组装 | `gc-mck-ctl/controllers/entrypoint/entrypoint.go:91-167` |
| Job 构建（挂载/超时/单飞） | `gc-mck-ctl/controllers/cluster_resource.go:148-264` |
| 回调 portal | `gc-mck-ctl/controllers/hook/cluster_biz.go` |
| 创建校验+预创建 | `gc-mck-portal/.../OrderCreateHandlerService.java:389-1069` |
| 事件消费/下发 R 层 | `gc-mck-portal/.../ConsumerBySelf.java:136-351` |
| ctl 回调策略工厂 | `gc-mck-portal/.../CtlRollbackFactory.java`（7 策略） |
| 状态常量全集 | `gc-mck-portal/.../common/constant/Status.java:78-450` |
| res 创建集群（落 CR） | `gc-mck-res/pkg/biz/cluster_service.go:45-191` |
| res 扩缩容（changeNode） | `gc-mck-res/pkg/biz/cluster_service.go:327-414` |
| res 多集群客户端 | `gc-mck-res/rpc/kubernetes_client.go:42-390` |
| 自研 CNI（PodENI） | `gc-mck-ctl` 分支 f-mck-cni：`kubespray/roles/network_plugin/lira/` |
| 自研 CSI / GPU+Volcano | `gc-mck-ctl`：`kubespray/roles/kubernetes-apps/csi_driver/mck_csi/`、`.../gpu/` |
