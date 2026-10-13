# MCK 容器云平台 · 集群生命周期管理 面试准备手册

> 目标岗位：智算云 PaaS 平台架构师（岗位 2）
> 素材来源：gc-mck-portal / gc-mck-res / gc-mck-ctl / gc-mck-plt 四仓库真实代码
> 本手册所有结论均来自实际读码，标 `文件:行号` 可复查；文末第 11 节列出「未确认项」，**面试不要编造**
> 你的角色定位：**整体参与，不专一模块** —— 全链路通吃，讲的是"我怎么把一条集群生命周期管线从头到尾打通"

---

## 0. 一句话定位（30 秒电梯版，必须背熟）

> "我参与建设的是公司私有云的托管 Kubernetes 产品 **MCK**。它做的事是：**把租户订单变成一台台真实可用的 K8s 集群，并管它一辈子的扩容、缩容、升级、停启、删除**。
>
> 架构上是四层：**portal（P 层）** 做订单履约编排和主数据，**res（R 层）** 是无状态资源网关、唯一持有各集群 kubeconfig，**ctl（C 层）** 是一个 kubebuilder Operator，用 `Cluster` / `ClusterOperation` 两个 CRD 驱动，底层内嵌完整 **kubespray** 用 Ansible 真装集群，**plt** 是运维后管的只读 BFF。
>
> 最核心的设计是：**集群的每一次变更都是一条 ClusterOperation CR + 一个跑 ansible-playbook 的 K8s Job**。创建是 `cluster.yml`、扩容 `scale.yml`、缩容 `remove-node.yml`、删除 `reset.yml`、升级 `upgrade-cluster.yml` —— 生命周期动作被**playbook 白名单**统一收敛成同一条声明式管道。这样加一个新运维能力，业务侧基本零改动。"

这段话覆盖了：产品定位、四层架构、核心机制、可扩展性论证。面试官从任何一句往下钻，你都有货。

---

## 1. 架构全景与我的角色

### 1.1 五层全景图（纸上能画出来）

```
┌─────────────────────────────────────────────────────────────────────────┐
│  租户控制台                             运维后管                          │
└──────┬───────────────────────────────────────┬──────────────────────────┘
       │ /portal/**                            │ /admin/**  （只读）
       ▼                                       ▼
┌──────────────────────┐              ┌──────────────────────┐
│  gc-mck-portal (P层) │              │   gc-mck-plt         │
│  Spring Boot + MyBatis│             │   Spring Boot        │
│  ──────────────────  │              │   ──────────────────  │
│  · 租户侧 API        │              │  · 集群列表/CR YAML  │
│  · 订单履约编排      │              │  · vars CM 查看      │
│  · 主数据持有者      │              │  · 管理员授权        │
│    (gcloud_mck 库)   │              │    CheckYaml/Monitor │
│  · task 表工作流     │              └──────────┬───────────┘
│  · Kafka 事件回环    │                         │
└──────┬───────────────┘                         │
       │ POST /resource/mck-service/v1/**       │
       │ (建集群 / 扩缩容 / 删集群 / SA/RBAC…)   │
       ▼                                         │
┌──────────────────────────────────────────────┐ │
│  gc-mck-res (R层)   Go + gin                 │◄┘
│  ──────────────────────────────────────────  │
│  · 无状态资源网关，24 条路由                 │
│  · 唯一持有各集群 kubeconfig                 │
│  · 元集群 ⇄ 工作集群 两级 client 工厂        │
│  · 落 Cluster / ClusterOperation CR          │
└──────┬───────────────────────────────────────┘
       │ 写 CR                          ▲ 读 CM / get
       ▼                                │
┌──────────────────────────────────────────────────────────────────────┐
│  元集群 (Meta Cluster, 按 poolCode 划分)                              │
│   ┌────────────────────────────────────────────────────────────┐     │
│   │  gc-mck-ctl (C层)  kubebuilder Operator                    │     │
│   │  ──────────────────────────────────────────────────────    │     │
│   │  ClusterReconciler  (15s)  ─┐                              │     │
│   │  ClusterOperationReconciler (3s) ─┐                        │     │
│   │                                  ├─► entrypoint.sh CM      │     │
│   │                                  └─► spray Job             │     │
│   └────────────────────────────────────────────────────────────┘     │
│                                                                       │
│   CRD: clusters.mck.msxf.com / clusteroperations.mck.msxf.com         │
│   CM:  hosts-conf-<id>  vars-conf-<id>  <id>-kubeconf  <ops>-entrypoint│
└──────┬────────────────────────────────────────────────────────────────┘
       │ spray Job 内 ansible-playbook (SSH)
       ▼
┌──────────────────────────────────────────────────────────────────────┐
│  工作集群 (业务 K8s, namespace = mck-<tenantId>)                      │
│   内嵌 kubespray 2.25.0 安装 · 节点上跑 categraf/kube-state-metrics   │
└──────────────────────────────────────────────────────────────────────┘
```

### 1.2 各组件一句话职责

| 组件 | 语言/栈 | 一句话职责 | 关键证据 |
|---|---|---|---|
| **gc-mck-portal** | Java / Spring Boot 2.3.12 / Java 11 / MyBatis-Plus / Redisson / Kafka / fabric8 6.8.0 | 租户侧 BFF + 订单履约编排 + 主数据持有者；**不直接碰成员集群 K8s API** | `MckApplication.java:31`；`ResRemoteService.java:104-120` |
| **gc-mck-res** | Go 1.20 / gin + gogf/gf / client-go v0.24 | 无状态资源网关；**唯一持有 kubeconfig**，代表上层读写元集群/工作集群 | `tools/common/constants.go:5`；`rpc/kubernetes_client.go:89` |
| **gc-mck-ctl** | Go 1.17 / kubebuilder v3 / controller-runtime v0.11.2 / k8s v0.23.5 | 集群生命周期 Operator，**CR 驱动 + 内嵌 kubespray 执行** | `main.go:156-179`；`go.mod:1-17` |
| **gc-mck-plt** | Java / Spring Boot 2.3.12 | 运维后管**只读** BFF（9 个接口全 ReadAction） | `MckClusterController.java:40-111` |
| **kubespray 2.25.0** | Ansible | 真正装集群的引擎（默认 kube_version v1.29.1，产品用 v1.27.5） | `kubespray/galaxy.yml:5` |

### 1.3 场景区分（面试第一问大概率问这个）

| 场景 | 走什么 | 为什么 |
|---|---|---|
| **创建 / 扩容 / 缩容 / 升级** | portal → res → ctl → kubespray | 要改 ansible inventory、跑 playbook |
| **删除** | portal → res → ctl(`reset.yml`) → 回调 → portal 删 ECS/VPC | 要先清 K8s 再回收云资源 |
| **停止 / 重启**（欠费停服、充值启） | portal 直接调 ECS 停机/开机，**不经过 res/ctl** | ECS 停机即 K8s 不可用，数据面无需调谐；恢复后由 ctl 回调 `/cluster/update`（RESTARTING 策略）做健康确认 |
| **看 YAML / 改配置** | plt(只读) 或 portal(写) → res | 凭据收敛在 res |

> **话术**：「这个边界是刻意的 —— 只有"需要改 K8s 集群本身"的操作才走 Operator，纯粹的资源开关（停/启）走 ECS 链路，避免为无意义的调谐付出代价。」

---

## 2. 端到端全链路：创建一个集群（最能体现深度，必须滚瓜烂熟）

这是整个 MCK 最复杂也最能讲的一条链路，横跨 4 个仓库、3 个 Kafka topic、2 个自产自销事件回环。**建议按 5 个阶段讲**。

### 阶段 A：下单校验 + 预生成（同步，订单系统调 portal）

入口：`POST /portal/mck-service/web/v1/cluster/checkClusterCreate`（`MckClusterPortalController.java:77`）

1. **校验链**（`OrderCreateHandlerService.concurrentHandleOrderCreateCheck:389`）：
   - 拓扑：containerCidr 掩码 8–24、serviceCidr 16–24、pod/service/子网三段**重叠检测** `IPUtil.isOverlap`、userData 必须 shell 脚本；
   - 规格：CPU≥2 核、内存≥4G、**master 必须带本地盘**；
   - 镜像：ostype 白名单 `${mck.order.ostype:centos}`；
   - 网络：`vpcRemoteService.getSubnet` 校验子网。
2. **预生成**（`processPreCreateCluster:498`）：
   - 加 Redisson 锁 `order:<orderId>`；
   - **幂等闸门** `checkOrderNotInTask:514`（task 表按 orderId 查，存在即拒——同一订单不会重复预生成）；
   - **服务端预生成 clusterId**（`CommonUtil.newResourceId`）——用户不能自定义 ID；
   - 建集群内置安全组；
   - 落 task 表：1 条主任务 `cluster_create` + N 条 `master_ecs_create`/`worker_ecs_create` 子任务 + 每台 ECS 一条 check 任务；node/volume 行 batchInsert；
   - **预创建 ECS** `preCreateEcsAndCheckRes`；
   - 上报资源中心 `buildResourcesRelation`（cluster→node(ECS)→volume 引用关系）。

> **面试点**：这一步把「等支付」从长事务拆成了**两阶段提交** —— 下单阶段就把 ID、节点行、任务树、ECS 预占全部落库，支付成功后零参数续跑。像不像"资源预留 + 提交"？主动这么类比。

### 阶段 B：支付成功（Kafka 事件驱动）

`${order.topic.status-change}` → `OrderCreateHandlerService.handleOrderCreate:184-272`：

- 锁 `order:<orderId>` → 取预生成的主任务；
- **落 cluster 主记录**（`clusterStatus = initial`），关键约定在这里诞生：
  - `namespace = mck-<tenantId>`（**这是 CR 的 namespace，也是租户隔离边界**）
  - 登录密码 KMS 加密存 `cluster.authentication`
  - 默认值：`clusterVersion=v1.27.5`、`clusterType=ProprietaryKubernetes`、`proxy_mode=iptables`、`node_cidr_mask=26`、`timeout_mins=60`
- cluster_feature 初始化组件（calico / nginx-ingress）；
- 内置安全组规则下发 + gnat SNAT 绑 VPC（ECS 出网）；
- **正式创建 ECS** `createEcsAndRetry`（`@Retryable`）。

### 阶段 C：ECS 就绪 → portal 自产自销事件

`${ecs.topic.status-initInstances}`（每台 ECS 一条）→ `EcsEvent.java:123`：

- **建 DNAT** 并落 `node_network` 表（`type=TYPE_GNAT_DNAT`，`manage_ip` 是**元集群 SSH 到 ECS 的管理 IP**）——这就是后面 ansible 能连上节点的原因；
- 子任务 → DONE → `checkAllSubTaskAndHandle` → 集群锁 + **锁内外双查**（`TaskWithLockForCreateMckServiceImpl:144-176`）；
- `@Transactional`：cluster → **creating**、主任务 → EXECUTING；
- 发 `publishAllPreCloudResourceReadyEvent`。

事件经事件中心绕回 Kafka `${mckevent.topic.mckResourceOkCallRes}` → `ConsumerBySelf.handleAllPreCloudResourceReadyLocalEvent:136`：

- 锁 `config-map:<clusterId>`；
- **用 freemarker 模板生成两个 ConfigMap**（`ClusterConfigmapUtil.java:79-139`）：
  - `hosts-conf-<clusterId>` = kubespray 的 **inventory `hosts.yml`**（ansible_host 取 `node_network.manage_ip`，密码从 KMS 解密）；
  - `vars-conf-<clusterId>` = **`group_vars.yml`**（镜像源、网络、k8s 版本、下载 URL）；
- `mckResService.createClusterAndCheck` → **POST res `/resource/mck-service/v1/cluster/create`**（容忍 "already exists" 幂等）。

> ★★ **这是整个架构最漂亮的一个收口，一定要讲**：
> **portal 和 kubespray 之间的接口，就是两个 ConfigMap。** portal 负责"把订单翻译成 ansible inventory"，ctl 负责"把 inventory 塞进容器跑 playbook"。两个团队用一个 K8s 原生对象解耦，谁都不用懂对方的内部实现。

### 阶段 D：res 落 CR（网关引导）

res 侧 `ClusterBiz.Create`（`cluster_service.go:45-191`）做四件事，**顺序很重要**：

1. 建业务 namespace `mck-<tenantId>`（不存在才建）；
2. 建 ServiceAccount `gc-mck-ctl`，并把它**追加到 ctl 的 ClusterRoleBinding `gc-mck-ctl-manager-rolebinding`** —— 这是给 Operator 在业务 namespace 里准备执行身份；
3. 创建 portal 传来的 hosts/vars 两个 ConfigMap；
4. 用 dynamic client 写 CR：
   - `Cluster` CR（`clusters.mck.msxf.com`）
   - `ClusterOperation` CR `{clusterId}-ops`（镜像按 poolCode 前缀匹配，匹配不到报"poolCode没有匹配的任务镜像"）

> **面试点**：res 的 SA/RBAC 引导逻辑存在，正是因为它要**先给 Operator 铺好路**，CR 才有意义。这是"控制面代理数据面"的典型写法。

### 阶段 E：ctl 调谐 → kubespray 装集群 → 状态回流

`ClusterOperationReconciler.Reconcile`（`clusteroperation_controller.go:79-235`）15 步，核心是：

1. 终态判定短路（`IsFinalState() && IsCompleted()`）；
2. `TrySuspendPod` **防双跑**（见 4.5）；
3. `CheckClusterDataRef` 校验 Cluster 的 hosts/vars CM 存在；
4. **`BackUpDataRef`：把 Cluster 的 hosts/vars 复制成带毫秒时间戳的快照 CM** —— 每次操作一份独立基线，扩缩容失败不污染母本；
5. `CreateEntryPointShellConfigMap`：用 `go:embed` 模板渲染 `entrypoint.sh`；
6. `CreateKubeSprayJob`：建 Job `<ops>-job`；
7. `UpdateStatusLoop`：每 5s 轮询 Job conditions 回写 status。

Job 内实际执行的就是：
```bash
ansible-playbook -i /conf/hosts.yml -b --become-user root \
  -e "@/conf/group_vars.yml" \
  /kubespray/cluster.yml -vvv
# preHook: ping.yml → disable-firewalld.yml
# postHook: kubeconfig.yml → cluster-info.yml
```

装完 → ctl 回写 ops.status=`Succeeded` → Cluster.phase=`running` → **POST portal `/noauth/mck-service/v1/cluster/update`**（载荷就是 Cluster CR 的 status 形状）→ portal `ClusterUpdateHandler` 按 **DB 里的 cluster_status** 选策略（`CtlRollbackFactory`）→ `ClusterCreatingHandlerService.handlerCtlRollback:46-88`：主任务 DONE → **`postHookCategraf.perform()` 部署监控插件**。

主任务 DONE → cluster → **running** → 发 `publishOrderCreateSuccessEvent`（更新资源中心 + 推云监控**计费**）。

**post-hook**：部署 `mck-categraf` DaemonSet + `kube-state-metrics` 到成员集群 kube-system，每 10s 轮询 DaemonSet ready 数直到等于 master 数（超时 300s），回写 `cluster_feature.install_status`。这是**列表页能看到 CPU/内存使用率**的数据源。

**兜底**：`@CloudTask("taskCreateCluster")` 扫 `cluster_status in (initial, creating)`，按 task_status 分派修复（子任务超时 10min 判失败）。

### 2.1 创建链路一句话版（背这个）

> **下单预生成 → 支付落库建 ECS → ECS 就绪建 DNAT → 生成两个 ConfigMap → res 落 Cluster+ClusterOperation CR → ctl 起 Job 跑 cluster.yml → playbook 用 kubectl patch 回填 kubeconfig → ctl 回调 portal → portal 落 running + 部署监控 → 推计费。**

---

## 3. 其余生命周期动作

### 3.1 删除（退订 RELEASE）

1. 校验：`deletionProtection == 0`；
2. Kafka RELEASE → `TaskForReleaseServiceImpl.createDeleteTaskAndUpdateCluster`：主任务 `cluster_delete` + 每节点 `ecs_delete` 子任务，cluster → **deleting**；
3. `deleteClusterByRes` → res `DELETE /cluster/delete`（**res 只删 Cluster CR**，靠 OwnerReference 级联清 ops/Job/CM；容忍 "not found"）；
4. ctl 的 finalizer `storage.mck.manager` 保证**先回调 portal 成功才放行删除**；
5. res 回调 `/noauth/.../cluster/delete` → portal `deleteVpcAndEcs`：
   - 按 `node_network.type` 分流删（`vpc_manage_card` 删管理网卡 / 否则删 DNAT）；
   - 删 ECS（容忍 `-11005000`）；
   - **SNAT VPC 引用计数**：查同 VPC 还有没有别的集群，没有才 `deleteVpc` —— 多集群共享 VPC 的正确写法；
6. 推送云监控 RELEASE → cluster → **deleted**、软删节点；
7. 安全组最后删：ECS 删除事件进来后统计 `已删节点数`，`nodes - 已删 <= 1` 时删内置安全组。

> **兜底任务**：`taskReleaseCluster`（K8S_DELETED）、`taskResReleaseCluster`（K8S_DELETE_FAIL）。

### 3.2 扩容（batchChangeScaleOut）

1. 校验：`checkOrderNotInTask` 幂等 + `workerNodeCount + scaleOutCount ≤ workNodeLimit=50`（**硬上限**）+ 规格一致性；
2. 预生成主任务 `cluster_node_scale_out`；
3. 支付/变更生效 → cluster → **scale_out_initial**，建 ECS；
4. ECS 就绪 → cluster → **scaling_out** → 生成 **clusterOpId = `<clusterId>-addnode<N>`**（N 从历史任务 taskDetail 解析递增并回写）；
5. 调 res `POST /cluster/changeNode`（`requestType=scaleOut`，**代码注释明确："非幂等性接口，不重试"**）；
6. ctl 跑 `scale.yml` + `--limit=node1,node2`；
7. 回调 → `ClusterScalingOutHandlerService`：**从 conditions 里找 `clusterOps == clusterOpId`** 精确匹配本次操作 → `ScaleSucceeded` → 主任务 DONE → cluster → running + 回写 `workerNodeConfig`。

**失败回滚**（`TaskScaleOutClusterNode` 兜底任务）：
生成本地回滚任务 → 先删 DNAT（`scale_out_failed_rollback_del_gnat`）→ 查 ECS 是否存在（不存在就直接删 node/volume 数据）→ 删 ECS → 重算工作节点数 → cluster → running。

### 3.3 缩容（batchChangeScaleIn）—— 最精巧的一条

1. 校验最严：cluster 必须 running + **所有节点网络必须已是 gnat**（早期"管理网卡"方案要缩容必须先迁移）+ 缩容后 ≥1 节点；
2. `checkScaleInTaskExist`：**必须查不到进行中的缩容任务**；
3. cluster → scaling_in，clusterOpId = `<clusterId>-delnode<N>`；
4. ctl 跑 `remove-node.yml` + `-e node=...`（entrypoint 自动补 `-e skip_confirmation=true`）；
5. 成功后 → **重排 hosts.yml 剔除被缩节点**（调 res `changeHostConfigMap`）→ 删管理网卡/DNAT → 删 ECS；
6. **失败时最精彩**：先调 res `getNodeStatus` 探测剩余节点 ——
   - 若有节点不 Ready → cluster → `scale_in_rolling_back`，**反向调 `scaleClusterNode(scaleOut)` 扩回来**！任务置 `k8s_scale_in_failed_to_scale_out`；
   - 扩回的结果再由 `ClusterScalingInToScalingOutHandlerService` 承接（成功则履约失败收尾，失败则再试）。

> ★ **面试金句**：「缩容是唯一"删错了就回不来"的操作，所以我们做了**反向补偿**：一旦发现集群健康度下降，立刻用扩容把节点加回去，宁可这次缩容失败，也不让客户集群残废。」

### 3.4 停止 / 重启

- **停止**：`cluster_stop` + 每节点 `ecs_stop` → cluster → stopping → ECS 关机事件 → cluster → **stopped** + 云监控 STOP 计费。
- **重启**：校验更严（ECS 全 SHUTDOWN + 每节点业务网卡存在且 ACTIVE）→ `cluster_restart` + `ecs_restart` → cluster → running + 云监控 START 计费。
- 两者**都不经过 res/ctl**；恢复后由 ctl 回调（RESTARTING 策略）做健康确认：节点数核对 → categraf 安装状态 → **云监控里查 apiserver 存活**（VM SQL `kube-apiserver up{...}`）→ 推计费。

### 3.5 升级 / 证书续期（独立运维操作）

- **升级**：`upgrade-cluster.yml`（在白名单 `entrypoint.go:19`）；
- **证书续期** `renew-certs.yml`：备份 → `kubeadm certs generate-csr` → openssl 用 CA 签 **10 年**（`valid_days 3650`）→ 更新 kubeconfig → 重启 kubelet → 等 `kubectl cluster-info` 恢复；**rescue 分支回滚备份**。

### 3.6 playbook 白名单（这是"操作类型"的真实定义）

`controllers/entrypoint/entrypoint.go:57-62` 是唯一真相源 —— **没有 enum，action 指向哪个 playbook 就是哪种操作**：

| 动作 | playbook | 特殊参数 |
|---|---|---|
| 创建 | `cluster.yml` | preHook `ping.yml`+`disable-firewalld.yml`；postHook `kubeconfig.yml`+`cluster-info.yml` |
| 扩容 | `scale.yml` | `--limit=node001,node002` |
| 缩容 | `remove-node.yml` | `-e node=...` + 自动 `skip_confirmation=true` |
| 删除 | `reset.yml` | 自动 `reset_confirmation=yes` |
| 升级 | `upgrade-cluster.yml` | |
| 前置检查 | `precheck.yml` | 结果回写 `spec.preCheckRef` |
| 证书续期 | `renew-certs.yml` | |

辅助 playbook（可自由组合进 pre/postHook）：`enable-repo`（离线源，支持 Kylin/openEuler/Anolis 等国产 OS）、`remove-pkgs`、`update-hosts`、`mount-xfs-pquota`（docker 存储盘 xfs pquota）、`enable-nf-conntrack`、`config-for-kube-vip`、`config-insecure-registry`、`node-label`、`kubeconfig`、`cluster-info`。

> **面试点**：`mount-xfs-pquota` + `config-insecure-registry` 这两个名字一看就是**踩过坑**的产物 —— 前者是 overlay2 `size` 限制的前置条件，后者解决私有 Harbor TLS。**主动讲这类"细节 playbook"，比讲大架构更能证明真做过。**

---

## 4. gc-mck-ctl 深挖（Operator 部分是 JD 第 3 条的直接命中）

### 4.1 两个 CRD

**Cluster**（`api/v1/cluster_types.go:28-76`）—— 期望态：

| 字段 | 语义 |
|---|---|
| `clusterType` | `ProprietaryKubernetes`（专有云 K8s） |
| `ClusterAlias` | 集群别名（= MCK 的 cluster name） |
| `platformInfo` | regionCode / azoneCode / resourceGroupID / resourcePoolId / tenantId / cloudAccountId |
| `timeoutMins` | 操作超时，最终变成 Job 的 `activeDeadlineSeconds` |
| `hostsConfRef` | **kubespray inventory hosts.yml** 的 CM 引用 |
| `varsConfRef` | **group_vars.yml** 的 CM 引用 |
| `kubeconfRef` | **建好后由 playbook 用 kubectl patch 反向回填** |
| `preCheckRef` | precheck 结果回填 |

**ClusterOperation**（`clusteroperation_types.go:33-130`）—— 一次具体动作：`cluster`（目标）、`image`、`clusterAction`、`preHook[]`/`postHook[]`、`hostsConfRef`/`varsConfRef`（operator 从 Cluster 备份填充的快照）、`entrypointSHRef`、`activeDeadlineSeconds`。

`ActionInfo` = `{actionType: playbook|shell, action, actionSource: builtin|configmap, actionSourceRef, extraArgs}`。

### 4.2 状态机

```
Cluster.phase:   "" ──► creating ──► running | failed

最新一条 ops 的 status 映射（cluster_controller.go:179-188）：
  Succeeded       → running
  ScaleSucceeded  → running
  ScaleFailed     → running   ← 扩缩容失败不算集群失败
  Failed          → failed
  Running/Scaling → 不变

ClusterOperation.status: "" → Running|Scaling → Succeeded|Failed|ScaleSucceeded|ScaleFailed
```

> **面试主动讲**：`ScaleFailed → running` 是**有意为之** —— 集群本身还是健康的，只是这次变更没成功，失败信息保留在 `conditions[]` 里。如果被追问"那失败去哪了"，答：condition 里 + portal 侧落 `scale_out_failed` 状态 + 触发回滚任务。

Cluster 的 `conditions[]` 是**所有 ClusterOperation 的投影**，每 15s 重建，用 `cluster_condition.go:8-40` 逐字段比较避免无效 status 写。

### 4.3 reconcile 的幂等设计（★★ 高频追问）

**每一步都是"字段为空才做"的声明式守卫**：

| 资源 | 幂等守卫 |
|---|---|
| Job | `status.jobRef` 非空直接跳过；jobRef 空但**同名 Job 已存在则"采纳"**（确定性命名 `<ops>-job` 让采纳成为可能） |
| entrypoint CM | `spec.entrypointSHRef` 非空跳过；Create 遇 AlreadyExists 则 Update |
| hosts/vars 快照 | `ops.spec.hostsConfRef` 为空才复制，且一次 reconcile 只做一件、做完 requeue |
| digest / OwnerRef / actor | 各自"已存在则跳过" |

> **面试金句**：「幂等的关键不是重试，是**每一步都能回答"这步做过了吗"**。我们把答案放在 CR 的 spec/status 字段里，而不是靠内存状态 —— 所以 controller 重启、Pod 被驱逐，都能从断点续走。」

### 4.4 digest 防篡改

`status.digest = CalSalt()` = 对 `cluster + actionType + action + image + 全部 hook` 串接取 MD5。之后每轮比对，不等则置 `hasModified=true`。

> **诚实讲不足**：`hasModified` **只标记不阻断**，被篡改的 spec 仍会执行。面试主动认领这个设计缺陷比被问出来强。

### 4.5 防双跑：actor Pod 锚定 + Suspend（★★★ 最亮的点）

`TrySuspendPod`（`clusteroperation_controller.go:670-727`）：

```
第一次发现 Job 下有 running Pod → 把 Pod 名写进 ops 注解 mck.msxf.com/actor
之后任何一轮 reconcile：
  若发现 Job 下有【另一个】running Pod（Job 被删重建 / controller 重启导致的双跑）
    → 把 Job 的 spec.suspend 置 true 挂起
    → 挂起态被 FetchJobStatusAndCompletionTime 判定为 Failed
```

**语义：同一个 ClusterOperation，任意时刻只允许一个执行 Pod。宁可失败，也不并行双跑。**

> **为什么需要它**：ansible-playbook 重复执行会造成灾难（重复 kubeadm init、节点状态错乱）。K8s 的 Job 本身只保证"至少跑完成"，不保证"controller 重启后不会又起一个"。用注解做锚定，是**用最轻的机制解决最重的问题**。
>
> 追问预案：「为什么不用 Job 的 `spec.suspend` 加乐观锁 / 不用 lease？」→ 因为锚定对象是 Pod 不是 Job；Pod 名是 K8s 分配的唯一标识，写进注解天然持久化在 etcd，比内存锁可靠，且不引入额外组件。

### 4.6 Ansible 执行机制

**entrypoint.sh 渲染**（`controllers/entrypoint/`）：`go:embed` 把模板编进二进制，`text/template` 渲染三段：

```bash
#!/bin/bash
set -o errexit; set -o nounset; set -o pipefail
# preinstall   ← PreHookCMDs 逐条
# run kubespray ← 见下
# post install ← PostHookCMDs 逐条
```

命令拼装（`entrypoint.go:91-112`）：
```
ansible-playbook -i /conf/hosts.yml -b --become-user root -e "@/conf/group_vars.yml"
  [--private-key /auth/ssh-privatekey]   ← 仅 isPrivateKey=true（当前恒 false）
  [-e "reset_confirmation=yes"]          ← reset.yml
  [-e "skip_confirmation=true"]          ← remove-node.yml
  /kubespray/<action> -vvv [<extraArgs>]
```

**Job 定义**（`cluster_resource.go:148-264`）：
- 名字 `<ops>-job`，`BackoffLimit=0`、`RestartPolicy=Never`（**Pod 失败即 Job Failed，不重试**）、`ImagePullPolicy=Always`；
- CM 挂载：hosts → `/conf/hosts.yml`，vars → `/conf/group_vars.yml`，entrypoint → `/bin/entrypoint.sh`（`DefaultMode 0700`）；
- 环境变量 `CLUSTER_NAME` / `CLUSTER_OPERATION_NAME`（playbook 里用 `lookup('env', ...)` 读）；
- **日志卷 HostPath `/home/finance/Logs/<cluster名>` → `/var/log`**，配合 `kubespray/ansible.cfg:13` 的 `log_path=/var/log/cluster.log`，把 ansible 日志落到宿主机；
- `activeDeadlineSeconds`：ops.spec 优先，否则 `timeoutMins*60`（默认 60min）；
- **exec-mode**：`0`=等 `/bin/entrypoint.sh` 出现（最多 60s，规避 CM 卷挂载延迟）后 exec；`1`=`sleep 3600`（调试）；`2`=自定义命令。

**自定义 playbook 无侵入替换**：`actionSource=configmap` 时，把用户的 CM 挂到 `/kubespray/<action>`（shell 型挂 `/bin/<action>`）—— **用挂载覆盖内置文件**。

### 4.7 失败上下文全自动回传（★★ 很漂亮的一条链路）

```
自定义 ansible callback 插件 kubespray/plugins/callbacks/log.py
  → v2_runner_on_failed / v2_runner_on_unreachable 按 host 收集 {task, error_message}
  → v2_playbook_on_stats 时发现失败 → 子进程执行 ansible-playbook /kubespray/cp_error.yml
    → cp_error.yml 用 kubectl patch 把错误 JSON 写进 CR 注解 mck.msxf.com/error（重试 5 次）
      → operator 在 UpdateStatusForLabel 里把注解搬进 status.message/error
        → 进 Cluster conditions[] → 回调 portal → 界面可见
```

> **面试点**：这条路让**任何一个 ansible task 的失败原因，都能从集群详情页看到**，而不是让运维去翻宿主机日志。是"可观测性要端到端"的范例。

### 4.8 结果反向回写（playbook 直接 patch CR）

`kubeconfig.yml`（`playbooks/kubeconfig.yml`）：
1. 从 `kube_control_plane[0]` fetch `/root/.kube/config`；
2. `sed` 把 server 从 `127.0.0.1:6443` 改成 `first_master_host:6443`（取第一台 master 的**业务网卡地址** `access_ip`，有 kube-vip 时用 VIP）；
3. 用 pod 内 SA 凭据 `kubectl` 在 spray Job 所在 namespace 建 CM `<cluster>-kubeconf`；
4. **`kubectl patch clusters.mck.msxf.com <cluster> -p '{"spec":{"kubeconfRef":...}}'`** —— playbook 反过来写 CR。

`precheck.yml`、`node-label.yml` 同款套路。

> **面试点**：「我们没有让 Operator 去实现一套"怎么采集 kubeconfig"的逻辑 —— 谁最清楚结果长什么样，就让谁写回去。Operator 只负责把结果搬运给业务层。」

### 4.9 ctl 的对外依赖

**只有一个 HTTP 出口：portal**。
- `POST /noauth/mck-service/v1/cluster/update`（整个 Cluster CR 作为 body，60s 超时）
- `POST /noauth/mck-service/v1/cluster/delete`（`{clusterId}`）

其余全是 K8s API。**不碰任何云 API** —— 云资源的创建/回收由 portal 完成。

### 4.10 ctl 真实弱点（主动讲 = 加分）

1. **纯轮询，无 Watch、无 predicate**（`cluster_controller.go:218-222`、`clusteroperation_controller.go:753-757`）：Cluster 15s / Ops 3s / Job 5s 无条件 requeue。本该用 `Watches()` 建立 Ops→Cluster 的映射。
2. **Cluster 终态后仍每 15s 回调一次 portal**（`:197-212` 无 phase 变化短路），靠 portal 幂等兜底。
3. **Condition 常量名与值错位**：`ClusterConditionCreating = "Running"`、`ClusterConditionRunning = "Succeeded"`（`cluster_types.go:114-128`）—— 读代码极易踩。
4. **`isPrivateKey` 恒为 false**（`clusteroperation_controller.go:446,453,459`）：SSH 密钥通道代码在但没启用，实际走 hosts.yml 里的**明文 ansible_password**（样例 CM 里就是明文）。
5. **RBAC 用 `*/*` 通配 ClusterRole**（`config/rbac/service_account.yaml`）—— 理由是管理集群是内部 meta 集群，但仍是收敛点。
6. **死代码**：`clusteroperation_controller.go:110-113`（与 `:95` 条件重复）。
7. Job 状态靠 5s 轮询而非 watch Job 事件，大集群创建（60min 级）期间 ops 持续高频 requeue。

---

## 5. gc-mck-portal 深挖（编排能力，对应"大规模生产经验"）

### 5.1 task 表 = 轻量工作流引擎（★★★）

`task` 表（`ddl_0831_ppr.sql:53-83`）：

| 字段 | 语义 |
|---|---|
| `task_id` | 唯一 |
| `task_type` | `main` / `sub` |
| `task_status` | 状态机 token |
| `task_detail` | 载荷 JSON（主任务存 OrderFullBizMetas，子任务存 Node JSON） |
| `context` | **CloudPortalRequestContext JSON** —— 调用链上下文/租户/trace，后续 RPC 从任务表还原 |
| `gr_id` | 关联的全局资源 id（子任务=节点 id），ECS 事件靠它回查任务 |
| `main_task_id` | 子 → 主指针 |
| `cluster_id` | 一切按集群收敛的锚 |
| `fail_reason` | varchar(2048) |

**每类操作的任务树**：

| 操作 | 主任务 | 子任务 |
|---|---|---|
| 创建 | `cluster_create` | `master_ecs_create` / `worker_ecs_create` + `create_*_ecs_check` |
| 删除 | `cluster_delete` | `ecs_delete` |
| 扩容 | `cluster_node_scale_out` | `scale_out_worker_ecs_create` + `scale_out_ecs_check` |
| 缩容 | `cluster_scaling_in` | `ecs_scaling_in` |
| 停止/重启 | `cluster_stop` / `cluster_restart` | `ecs_stop` / `ecs_restart` |
| 后置钩子 | `post_hook_main` | `serviceaccount` / `kube-state-metrics` / `mck-categraf` / `categraf_config` |
| 回滚 | `scale_out_failed_rollback`(+`_del_gnat`) | — |
| 预资源清理 | `pre_create_release` / `pre_scale_out_release` | — |

> **设计细节（面试可讲）**：停止/删除/缩容创建任务时，`task_status` **直接复用 ClusterStatus 常量**（STOPPING/DELETING/SCALING_IN），后续兜底任务也按 cluster_status 查任务 —— **状态机 token 跨枚举有意混用**，少了一层映射但也牺牲了清晰度。

### 5.2 三层可靠性：@Retryable → @Recover → @CloudTask

```
① 方法级重试   @Retryable(maxAttempts, backoff)
② 降级落库     @Recover → 把任务置成 *_FAIL / *_UNRELEASED_MONITORING 等"可兜底状态"
③ 定时补偿     @CloudTask(22 个) 扫描这些状态捞起重做
```

22 个 `@CloudTask` 覆盖：`taskCreateCluster`、`taskScaleOutClusterNode`（4 种扩容态）、`taskReleaseCluster`、`taskResReleaseCluster`、`taskStopCluster`、`taskShrinkageResScaleInCluster`(+4 个)、`taskPreResourceRelease`（**防资源泄漏**）、`taskClusterStatusCheck`（健康巡检）等。

> **面试金句**：「我们没有引入工作流引擎，用一张 task 表 + 三种机制就做到了可恢复 —— 这是**用最简单的东西解决确定性恢复**。任何一步崩溃，都能从 DB 状态重新推导出下一步该干什么。」

### 5.3 分布式锁：三粒度 + 锁内外双查

Redisson，key 前缀 `gc-mck-portal:lock:`：

| 粒度 | key | 防什么 |
|---|---|---|
| 订单级 | `order:<orderId>` | 同订单并发进入 |
| 集群级 | `cluster:<clusterId>` | 同集群多事件/定时任务并发推进 |
| ConfigMap 级 | `config-map:<clusterId>` | 并发重建 hosts/vars CM |

**双检模式**：锁外预判（省锁开销）+ 锁内复判（保正确性）—— 所有 `TaskWithLockFor*` 类都是这个结构。

> **主动认领瑕疵**：stop/restart 用 `CLUSTER_PREFIX`（"cluster"）而非 `LOCK_CLUSTER_PREFIX`（"cluster:"）加锁（`TaskForStopServiceImpl.java:217`、`TaskForRestartServiceImpl.java:215`），锁 key 风格不一致，属历史遗留 —— 值得修。

### 5.4 Kafka 事件回环（自产自销）

portal 既是生产者又是消费者，用事件中心绕回自己的 topic：

- `${order.topic.status-change}` / `status-notice` —— 订单 CREATE / RELEASE / ARREARS_STOP / RECHARGE_ENABLE / BATCH_CHANGE
- `${ecs.topic.status-initInstances}` / `status-change` / `release_event` —— ECS 生命周期
- **`${mckevent.topic.mckResourceOkCallRes}`** —— **portal 自己发、自己收**：前置云资源就绪 → 调 res 部署
- `mckCreatedFailedRollback` / `mckScaleOutFailedRollback` 等失败回滚事件

> **为什么要绕一圈？** 把"前置资源就绪"和"调 res 部署"解耦成一个**事务性事件**（`event_link_id` 关联任务），避免在 ECS 事件处理线程里做重活，也让重放成为可能。

### 5.5 一致性模型：Saga + 事件溯源

- 本地多表跃迁用 `@Transactional`；
- **跨服务无分布式事务**（没有 Seata/TC），一致性靠五件套：
  1. **任务表记录意图**（先落 task 再调外部）
  2. **事件补偿**（失败发回滚事件）
  3. **@Recover 落特殊状态**
  4. **@CloudTask 兜底扫描**
  5. **状态机前置条件收敛**（如"cluster=INITIAL && task=CREATING"才动手）

> **面试金句**：「task 表本质上是**本地消息表的变体** —— 把"我要做什么"先持久化，再执行，失败了有回滚事件，卡住了有定时任务。这是最终一致性最朴素也最可靠的实现。」

### 5.6 幂等词表（很能体现工程成熟度）

容错清单：`createClusterAndCheck` 容忍 "already exists"、`deleteCluster` 容忍 "not found"、删 DNAT 容忍"可能资源不存在"、删 ECS 容忍 `-11005000`、`changeEcs` 容忍 `-11009000`、云监控容忍 `-11000000`/"数据状态重复"。

**反向自觉**：`scaleClusterNode` 明确注释"**非幂等性接口，不重试**"，失败走事件回滚。

> **面试点**：「我们不是无脑重试 —— 分得清哪些接口幂等可以重试、哪些一重试就出事。这份清单本身就是踩坑攒出来的。」

### 5.7 多租户

- `cluster.tenant_id` 贯穿；**CR 的 namespace = `mck-<tenantId>`** —— K8s 侧按租户 namespace 隔离；
- 查询强制 tenant 维度：`Describe`/`KubeCfg` 都 `clusterId + tenantId` 联查，代码注释明写"**防止平行越权**"；
- **主动认领**：`NodeMapper.xml:48-50` 的租户过滤被注释掉了 —— 节点列表查询当前不按租户过滤，依赖上层约束。这是个真实的越权面。

---

## 6. gc-mck-res 深挖（资源网关）

### 6.1 定位

**无状态 HTTP 网关**，24 条路由统一前缀 `/resource/mck-service/v1`，被 portal/plt 调用，代表它们对元集群和工作集群做资源 CRUD。**自身不做编排**，无 DB、无 ORM、无 informer。

### 6.2 两级集群与 client 工厂

```
元集群   = poolCode 一个资源池一个管理集群（CR/CM 都在这）
工作集群 = clusterId + namespace(mck-<tenantId>) 定位
         kubeconfig 存在【元集群】的 CM {clusterId}-kubeconf 里
```

client 获取链路（三级缓存 key）：

```
元集群强类型 client   → gcache "{poolCode}_kube_client"        (TTL 5min)
元集群 dynamic client → gcache "{poolCode}_dynamic_client"     (TTL 5min)
工作集群 client       → gcache "{poolCode}_{clusterId}_{ns}_kube_client" (TTL 5min)
                          └─ miss 时：读元集群 CM {clusterId}-kubeconf
```

`ResourcePoolHost` 默认 `http://gc-cboot-plt-...`，`GET /noauth/cboot/pool/spec?poolCode=` 取资源池元数据 + **元集群 kubeconfig**。

### 6.3 kubeconfig 修正（★ 亮点）

`ChangeMasterNodeIp`（`rpc/kubernetes_client.go:364-390`）：用 `hosts-conf-<clusterId>` 里的 **ansible inventory** 替换 kubeconfig 的 server IP，并把 IP 换成 `ansible_host` —— 解决 kubespray 部署后 **master IP 漂移**。用 `history_data_segmentation_time` 区分新旧数据（只对新建的 CM 做替换）。

### 6.4 租户凭证签发

`CreateKubeConfig`：读工作集群 SA token Secret → 从元集群取 kubeconfig → 把 users 只留 token、context/user 名替换成 `{subjectId}@cluster.local` → 生成 Secret `{subjectId}-secret` 写回工作集群。**为租户主体签发受限凭证**。

### 6.5 真实弱点

1. **kubeconfig 缓存只有 5min TTL，无主动失效**：全仓无 Remove/Clear 调用。凭证轮换后最长 5 分钟窗口用旧配置。
2. **`MountKubeConfig` 只剩最后一个**（`kubernetes_client.go:57` for 循环覆盖）：多 kubeconfig 挂载时 describe 固定用最后一份 → **多资源池部署下 describe 可能打到错误集群**。
3. **update = delete + create**（ClusterRoleBinding）：非原子，存在权限短暂消失窗口。
4. 小 bug：`strings.ReplaceAll` 结果未接收（`cluster_service.go:435`）；`configmap_service.go:150-151` 赋了 `existingConfigMap.Data` 却没用；`daemonset_service.go:94,98` 把 err 当 data 返回（`return err, nil`）。
5. **请求路径上有 `log.Fatalf`**（`kubernetes_client.go:340,346`；`kubeConfig_service.go:119,125`）—— 会把整个进程干掉。
6. 响应体全量缓冲日志（`tools/middleware/logger.go:11-30`）：kubeconfig 等大响应被完整写日志，**兼有泄密面**。

### 6.6 CRD 类型是复制不是依赖（★ 好的讨论点）

`pojo/api/v1` 是从 ctl 的 `api/v1` **手工复制的类型快照**（同 GroupVersion `mck.msxf.com/v1`），不是 module 依赖。

- **为什么**：res 不装 CRD、不跑控制器，只用 dynamic client 以 unstructured 读写，真正需要一致的只是 **json tag 序列化结构**；复制可以避免引入 controller-runtime 全套依赖。
- **代价**：**双份类型漂移** —— res 里的 `ClusterConditionUpdating = "Failed"` 已经落后于 ctl（`ClusterConditionFailed = "Failed"`），res 缺 `Scaling/ScaleSucceeded/ScaleFailed` 常量。

> **面试话术**：「这是我参与时讨论过的一个 trade-off。理想方案是抽一个 `mck-api` 共享 module，但那样 res 就要跟着 ctl 的 controller-runtime 版本走。当时选了"快照 + dynamic client"，代价是常量会漂移 —— **如果你问我怎么改进，我会做 codegen 从 CRD OpenAPI schema 生成，而不是手工复制。**」

---

## 7. gc-mck-plt（运维后管）

- 9 个接口全挂 `/admin/mck-service/web/v1/**`，**全部 `ReadAction`**（与 portal 的 `/portal/**` 读写面分野）；
- 能力：集群列表（**不按租户过滤**，后管全量视角）+ CR YAML/describe + `vars-conf-<id>` CM + 管理员授权；
- **双层授权模型**：资源 = `clusterId`，动作枚举 `CheckYaml` / `CheckMonitor`，状态 `1 待授权 → 2 生效中`；前端用 `validator` 接口做 UI 显隐，后端 4 个读接口用 `queryAuth` **服务端兜底**；
- 它自己不连 K8s —— 查 YAML 也是经 res（GET `/cluster/get` | `/clusteroperation/get`，name 改写成 `<clusterId>-ops`）。

> **面试点**：`CheckYaml` 这个授权项的设计很实在 —— 集群 YAML 里含敏感信息，所以"能看列表"和"能看 YAML"是两回事。

---

## 8. 数据模型（gcloud_mck 库）

```
cluster 1──N node 1──N volume
             │
             └─1──N node_network
cluster 1──N task (自关联 main/sub)
cluster N──N feature  经 cluster_feature（带 install_status/feature_status）
general_config        每可用区/每环境的外置配置源
```

| 表 | 关键点 |
|---|---|
| `cluster` | `cluster_id` 唯一；`namespace = mck-<tenantId>`；`cluster_status`(状态机) + `state`(控制台展示)**双轨**；`authentication` KMS 密文；`container_cidr`/`service_cidr`/`node_cidr_mask=26`；`timeout_mins=60`；`master/worker_node_config` JSON + `actual_*_node_count` |
| `node` | `node_id = ecsId`；`node_role` master/kube_node；`current_task_id/order_id/item_id` 关联履约 |
| `volume` | 随订单子项预生成，生命周期**依附节点**（无独立 CRUD API） |
| `node_network` | 节点↔网络账本；`type` = `TYPE_GNAT_DNAT` / `vpc_manage_card`；`manage_ip` = **元集群 SSH 到 ECS 的 IP**（hosts.yml 的 ansible_host 来源） |
| `task` | 见 §5.1 |
| `cluster_feature` | `feature_status`(init/pending/running/failed) + `install_status`(not_installed/installing/installed/failed) |

> **`node_network` 值得多讲两句**：它同时承载了"节点怎么被管理"（DNAT/manage_ip）和"怎么被删除"（按 type 分流删 DNAT 还是删网卡）。早期是"管理网卡"方案，后来演化到 gnat DNAT，缩容校验里强制"全 gnat"就是这次演化的兼容守卫。

---

## 9. 高频追问库（★ 是几乎必问）

### 9.1 架构与选型

- ★ **为什么要 Operator 而不是在 portal 里直接调 Ansible？**
  三点：①集群部署是**分钟级长任务**，不能在 HTTP 请求里跑；②中途失败要能**断点续做**，CR + reconcile 天然幂等可重入；③**集群状态必须持久化且可观测**，`kubectl get cluster` 就能看 phase，运维不用登业务库。
- ★ **为什么用 kubespray 而不是 kubeadm 脚本自己写？**
  kubespray 是生产验证过的成熟方案（多 OS、多运行时、HA、证书、离线源全都有）；我们做的是**在其之上包一层声明式外壳 + 定制 playbook**（国产 OS 仓库源、xfs pquota、kube-vip、insecure registry、节点打 ecsId 标签）。**重造轮子的成本远高于定制成本**。
- ★ **四层为什么这么切？**
  安全边界 + 变更隔离。kubeconfig 只在 res 一层持有，业务层（portal/plt）完全碰不到集群凭据；而"唯一能改 K8s 的东西"是 ctl —— 这样审计和爆炸半径都清晰。
- **为什么 portal 和 ctl 之间用 ConfigMap 而不是 RPC 传参？**
  ConfigMap 是 K8s 原生对象，**天然持久化、可版本化、可 diff、可被 kubectl 直接看**；而且它正好是 ansible inventory 的载体，零转换。如果用 RPC 传，就要自己造一个持久化 + 版本管理。

### 9.2 CRD / Controller（JD 第 3 条直接命中）

- ★ **reconcile 幂等怎么保证？** 见 §4.3：**每一步都能回答"这步做过了吗"，答案放在 CR 的 spec/status 字段里，不靠内存**。
- ★ **finalizer 用在哪？** `storage.mck.manager` 在 Cluster 上 —— 保证**先回调 portal 成功，才放行 CR 删除**，避免业务层丢失集群已删的通知。
- ★ **怎么防止同一个操作被执行两次？** `TrySuspendPod` actor 锚定（§4.5）。
- ★ **OwnerReference 怎么设计的？** 三级级联：`Cluster → ClusterOperation → Job/CM`；另外 Cluster 会给它引用的 4 个 CM 打 owner（`cluster_controller.go:129-141`），保证集群删了 CM 也被 GC。
- **为什么不 watch，全靠 requeue？** 见 §4.10 第 1 条 —— 这是**明确的改进点**，我会用 `Watches(&source.Kind{Type: &ClusterOperation{}}, handler.EnqueueRequestsFromMapFunc(...))` 替代轮询。
- **Admission Webhook 有没有？** ctl 当前**没有** webhook（`main.go` 只注册两个 Reconciler）。可以讲思路：**spec 不可变校验**（digest 已经在做但只标记不阻断）、**namespace 合法性校验**（必须是 `mck-<tenantId>` 形状）。诚实说"这块是我们该补没补的"。

### 9.3 状态机与一致性

- ★ **cluster_status 和 state 为什么两套？** 一套给代码分支（状态机 token），一套给控制台展示（聚合后的用户可读状态）。
- ★ **跨服务一致性怎么做的？** Saga + 本地消息表（§5.5）。
- ★ **Kafka 消息丢了怎么办？** `@CloudTask` 扫描 `cluster_status`/`task_status` 兜底；预创建资源有 `pre_*_release` 兜底任务防泄漏。
- **clusterOpId 怎么保证唯一有序？** `<clusterId>-addnode<N>` / `-delnode<N>`，N 从历史任务 taskDetail 解析递增并回写主任务（`TaskForShrinkageServiceImpl.getClusterOpId:533-583`）。回调时用 `conditions[clusterOps == clusterOpId]` **精确匹配是哪一次操作**。

### 9.4 运维 / 故障场景（架构师必考）

- ★ **用户点"创建集群"后 20 分钟没出来，怎么排查？**
  分层：①portal 侧查 cluster_status（initial/creating？）和 task 树（哪个子任务卡住、fail_reason）；②ECS 是不是建出来了（ecs-portal）；③元集群 `kubectl get cluster <id> -n mck-<tenant>` 看 phase 和 conditions；④`kubectl get clusteroperation <id>-ops -n mck-<tenant>` 看 status 和 `mck.msxf.com/error` 注解 —— **ansible 的原始失败原因就在这里**；⑤`kubectl get job <ops>-job` 看 Pod；⑥宿主机 `/home/finance/Logs/<cluster>/cluster.log` 看 ansible 全量日志。
- ★ **节点扩进来了但一直 NotReady？** 看 ops 是 ScaleSucceeded 还是 ScaleFailed；成功后节点 NotReady 通常是 playbook 的 postHook 没跑完（`cluster-info.yml` 或 CNI 未就绪），查 categraf/kube-state-metrics 安装状态。
- **缩容缩挂了会怎样？** 有**反向补偿**：探测剩余节点健康度，不 Ready 就自动扩回来（§3.3）。
- **controller 重启会有影响吗？** 无状态，从 CR 续走；正在跑的 Job 不会被重复触发（actor 锚定）。
- **怎么回收孤儿云资源？** finalizer 保证正常不泄漏；兜底是 `taskPreResourceRelease`（预创建阶段失败）和 `taskResReleaseCluster`（res 删除失败重试）。

### 9.5 规模 / 性能

- **瓶颈在哪？** ①ansible 装集群本身（分钟级）是绝对瓶颈，且**串行 nature**（一个 master 一个 master 来）；②ctl 的**纯轮询**在大集群数下会产生大量无效 reconcile（每个 ops 3s 一次）—— 这是我会优先改的；③res 的 kubeconfig 5min 缓存策略在集群数量多时内存可控（gcache 上限 100000）。
- **一个元集群管多少个工作集群？**（填真实数字/量级）架构上按 `poolCode` 分资源池，每个池一个元集群 —— **加池就是水平扩容**。
- **`activeDeadlineSeconds` 60 分钟够吗？** 大集群可能不够，这是可配的（`cluster.timeoutMins`）——主动说"我们把它做成了 per-cluster 可调"。

### 9.6 安全

- ★ **ansible 密码怎么管？** KMS 加密存 `cluster.authentication`，组装 ConfigMap 前解密（`ClusterConfigmapUtil.java:96`）。
- ★ **那 hosts.yml 里就是明文密码？** **是的，这是当前的真实风险**（`isPrivateKey` 恒 false，密钥通道没启用）。改进方向：启用 `--private-key` + 密钥走 Secret 挂载。
- **kubeconfig 会不会泄露？** 风险点：res 把 rest.Config 和整个 kubeconfig 打进日志（`kubernetes_client.go:180,248`）+ 响应体全量缓冲日志。**这是我会优先修的**。
- **租户隔离靠什么？** CR namespace `mck-<tenantId>` + 查询强制 tenant 维度 + KMS 加密凭据。**但 `NodeMapper.xml` 的租户过滤被注释了** —— 真实缺口。

---

## 10. 主动讲弱点与演进（架构师加分项，准备 5 条）

1. **ctl 从轮询改事件驱动**：给 ClusterOperation 加 `Watches` 映射到 Cluster，去掉 15s/3s/5s 的无条件 requeue；Cluster 回调 portal 加 phase 变化短路。
2. **启用 SSH 密钥通道**：`isPrivateKey` 打开，密钥走 Secret 挂载替代 hosts.yml 明文密码；顺带把 res 里打印 kubeconfig 的日志降级。
3. **CRD 类型 codegen 化**：用 controller-gen 从 CRD schema 生成 res 侧的共享类型 module，消除手工复制导致的双份漂移。
4. **补 Admission Webhook**：ClusterOperation 加不可变校验（digest 从"只标记"升级为"阻断"），Cluster 加 namespace 形状校验。
5. **补对账（reconcile 之外的自愈）**：定时对账「云资源 ↔ CR ↔ DB 三方」，回收孤儿 ECS/DNAT/安全组 —— 现在靠 finalizer + 兜底任务，缺一层全局对账。

---

## 11. 未确认项（面试不要编造，被追问就说"这块我没深入"）

1. **公司公共 Java 框架源码不可见**：`AbstractService.dispatchRequest` 的注册表实现、`@CloudTask` 调度平台（触发频率/cron）、事件中心 Sender 端点 —— 都是外部 jar。「按 `(service, method)` 注册表分发」是**强证据推断**（9/9 method 名精确匹配 + plt/portal 同框架佐证），非源码级确认。
2. **`/noauth/mck-service/v1/cluster/update` 的确切调用方**：portal 代码自称"ctl 回调"，载荷是 Cluster CR status 形状，方向确定；但**是 ctl 直调还是经 res 转发**本仓库不可见。
3. **`rpc.address.*` 和 Kafka topic 的实际值**都在 Nacos/Vault，仓库里只有 key。
4. **spray-job 基础镜像** `mck/spray-job:master` 的内部构成（基础镜像构建脚本不在任何仓库）。
5. **`mck.msxf.com/message` 注解的写入方** —— ctl 侧只读不写，写入方未确认（推断为 portal）。
6. **`mck-kubeconfig.yaml`（生成受限用户 kubeconfig）的实际触发方式** —— 不在 entrypoint 白名单里，推测由 portal 通过 configmap-source 接入。
7. **`Status.EcsProcessStatus` 里 `7=INSUFFICIENT` 与 `EcsEvent` 以 `taskState==7` 判成功的语义张力** —— 可能枚举注释滞后于实际协议。
8. **`@MapperScan` 包名失配**（plt：`com.msxf.cloud.dao.mapper` vs 实际 `com.msxf.cloud.mapper`）的真实运行表现未验证。

---

## 12. JD 逐条映射

| JD 要求 | 你的应答素材 |
|---|---|
| 1. Kubernetes 集群生命周期（规划/部署/升级/扩容/多集群/HA） | §2 创建全链路 + §3 各动作 + §4.2 状态机 + 多池多集群模型 |
| 2. 主流容器网络原理 + 自研 CNI 经验 | **lira 手册（见 `interview-prep-cni.md`）** + MCK 侧 calico/nginx-ingress 组件、kube-vip、insecure-registry、nf_conntrack 这些 playbook |
| 3. Operator / CRD / Controller / Admission Webhook | §4 整节 + §9.2 追问库；Webhook 主动讲"该补没补"及设计思路 |
| 4. 数百到数千节点生产经验 / 性能 / 高可用 / 升级 | §9.5 瓶颈模型 + §3.5 升级/证书续期 + §10 演进路线；填你的真实集群规模数字 |
| 5. 参与/研究开源项目 | kubespray 2.25.0 的定制实践（自定义角色 `skip-gather-ips`、`node-label-msxf`、自定义 callback 插件）+ 上游 kubean 用例出现在 ctl 单测里 |
| 6. GPU Kubernetes / AI Infra | **MCK 现在是 CPU 容器云**，不要硬扯 GPU。桥接：集群生命周期管理能力（多集群/节点池/扩缩容/节点标签 `mck.msxf.com/ecsId`）是 GPU 集群调度的同一套底座；GPU 部分走 lira 手册第 5 节框架 |
| 7. 竞品分析 | §13 对比题 |

---

## 13. 对比题（竞品认知）

- **和阿里 ACK / 腾讯 TKE 比？** 它们绑自家云 API + 自家的托管控制面；**MCK 的价值恰恰是"我们云平台自研"** —— 从 VPC/ECS/安全组/SNAT/DNAT 到 K8s 装机的全链路都在自己手里，可以做深度定制（比如缩容失败自动扩回的补偿逻辑，公有云产品未必敢做）。
- **和 Rancher / OpenShift 比？** Rancher 的 RKE2 强在"任意基础设施上装 K8s"，但**不解决云资源编排**（ECS/网络/计费）；MCK 是"云资源 + K8s 生命周期"一体化的订单驱动产品。OpenShift 是完整 PaaS 栈（含 S2I、OperatorHub），MCK 定位更薄、更贴自有云。
- **和 Cluster API（CAPI）比？** ★ 这是个好问题，要准备。CAPI 是社区标准的"集群即 CR"抽象（Cluster/MachineDeployment/Machine），**MCK 的 Cluster/ClusterOperation 在思想上和 CAPI 同源**，差异在于：①我们多了一层"订单履约"（业务语义），CAPI 只到基础设施抽象；②CAPI 用 provider 插件化（AWS/Azure/vSphere），我们用内嵌 kubespray（面向裸机+EC2 混合的自有云）；③CAPI 的 Machine 生命周期更细粒度（比我们的 ClusterOperation 更"资源化"）。**如果重做，我会参考 CAPI 的 provider 分层。**

---

## 14. 面试前 Checklist

- [ ] 默画 §1.1 五层架构图，纸上能画出来
- [ ] **§2 创建全链路五阶段口述 3 分钟内完成**（这是主菜）
- [ ] 背熟 §0 电梯版和 §2.1 一句话版
- [ ] §3.6 playbook 白名单表能脱稿讲 6 个核心动作
- [ ] §4.5 actor 防双跑 + §4.7 失败回传 + §4.8 反向回写 —— **三个"亮点细节"讲熟**
- [ ] §5.1 task 表工作流 + §5.2 三层可靠性 + §5.5 Saga
- [ ] §9.4 故障排查能分层说清（尤其是"20 分钟没出来怎么查"）
- [ ] §10 演进路线 5 条能脱稿讲
- [ ] §13 CAPI 对比准备一段
- [ ] **§11 未确认项记牢，不编造**
- [ ] 准备 2 个反问：① 平台当前多集群规模与元集群瓶颈？② 是否有向 CAPI/DRA 演进的规划？

---

> **最后一句话给自己**：你这套东西的**独特性**不在"用了 K8s"，而在 **"把一笔云订单，通过 CR + ConfigMap + playbook 三层抽象，端到端变成一台生产可用的 K8s 集群，并且管它一辈子"**。讲的时候始终围绕这条主线，所有细节都是它的注解。
