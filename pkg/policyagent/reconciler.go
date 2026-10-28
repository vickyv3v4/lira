//go:build linux
// +build linux

package policyagent

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"gitlab.msxf.com/mscloud/lira/pkg/ebpf"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"
)

// podState 参与策略计算的 pod 快照（compute 的输入）
type podState struct {
	namespace   string
	name        string
	ip          string
	labels      map[string]string
	nsLabels    map[string]string
	hostNetwork bool
	identity    uint32
}

const (
	// debounce 事件合流窗口：距最近 watch 事件不足窗口时延后重入队，合流事件风暴
	defaultDebounce = 500 * time.Millisecond
	// defaultResync 成功后周期性全量重同步，自愈数据面 map 漂移
	defaultResync = 5 * time.Minute
)

// syncKey 全部 watch 事件映射到的固定 reconcile key（全量重算模型，与对象无关）
var syncKey = k8stypes.NamespacedName{Name: "lira-policy-sync"}

// Reconciler 策略控制面（daemon 内运行，设计 §5.2），与仓库现有 controller 统一使用
// controller-runtime Manager：watch 全部 Pod / NetworkPolicy / Namespace，事件触发去抖后的
// 全量重算，将目标状态全量替换写入三张策略 map 与 pod_ip 身份表。
type Reconciler struct {
	client   client.Client
	backend  ebpf.Backend
	debounce time.Duration
	resync   time.Duration

	mu        sync.Mutex
	lastEvent time.Time // 最近一次 watch 事件时间（去抖基准）

	// syncedIPs 上一次成功全量写入的 pod_ip 集合，用于 diff 删除
	// （daemon AllocIP 也可能写 pod_ip，不在此集合内的条目不会被误删）
	syncedIPs map[string]uint32
}

// Register 将策略 reconciler 注册到 manager：三类对象的事件统一映射到固定 sync key，
// Reconcile 内部做去抖 + 全量重算（MaxConcurrentReconciles 默认 1，天然串行）。
func Register(mgr ctrl.Manager, backend ebpf.Backend) error {
	r := &Reconciler{
		client:    mgr.GetClient(),
		backend:   backend,
		debounce:  defaultDebounce,
		resync:    defaultResync,
		syncedIPs: make(map[string]uint32),
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("lira-policy").
		Watches(&corev1.Pod{}, r.eventHandler()).
		Watches(&networkingv1.NetworkPolicy{}, r.eventHandler()).
		Watches(&corev1.Namespace{}, r.eventHandler()).
		Complete(r)
}

// eventHandler 把任意对象事件标记时间戳并映射到固定 sync key；
// ResourceVersion 相同的事件为缓存 resync 回放，跳过。
func (r *Reconciler) eventHandler() handler.EventHandler {
	enqueue := func(q workqueue.RateLimitingInterface) {
		r.mu.Lock()
		r.lastEvent = time.Now()
		r.mu.Unlock()
		q.Add(reconcile.Request{NamespacedName: syncKey})
	}
	return handler.Funcs{
		CreateFunc: func(_ context.Context, _ event.CreateEvent, q workqueue.RateLimitingInterface) {
			enqueue(q)
		},
		UpdateFunc: func(_ context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
			if e.ObjectOld != nil && e.ObjectNew != nil &&
				e.ObjectOld.GetResourceVersion() == e.ObjectNew.GetResourceVersion() {
				return
			}
			enqueue(q)
		},
		DeleteFunc: func(_ context.Context, _ event.DeleteEvent, q workqueue.RateLimitingInterface) {
			enqueue(q)
		},
	}
}

// Reconcile 去抖后全量重算（设计 §5.2）：事件风暴期间反复延后直至安静一个窗口；
// 失败返回 error 由控制器限速退避重试；成功后周期重同步自愈数据面 map 漂移。
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if since := time.Since(r.lastEventTime()); since < r.debounce {
		return ctrl.Result{RequeueAfter: r.debounce - since + 10*time.Millisecond}, nil
	}
	if err := r.sync(ctx); err != nil {
		llog.Logger.WithError(err).Error("ebpf: policy sync failed")
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.resync}, nil
}

func (r *Reconciler) lastEventTime() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastEvent
}

// sync 全量重算并写入数据面（设计 §5.2）
func (r *Reconciler) sync(ctx context.Context) error {
	nsLabels, err := r.nsLabelIndex(ctx)
	if err != nil {
		return fmt.Errorf("error list namespaces, %w", err)
	}
	pods, err := r.listPods(ctx, nsLabels)
	if err != nil {
		return fmt.Errorf("error list pods, %w", err)
	}
	policies, err := r.listPolicies(ctx)
	if err != nil {
		return fmt.Errorf("error list network policies, %w", err)
	}

	res, err := compute(pods, policies)
	if err != nil {
		return fmt.Errorf("error compute policy, %w", err)
	}
	if err := r.backend.ReplaceIsolated(res.isolated); err != nil {
		return fmt.Errorf("error replace isolated map, %w", err)
	}
	if err := r.backend.ReplacePair(res.pairs); err != nil {
		return fmt.Errorf("error replace policy_id map, %w", err)
	}
	if err := r.backend.ReplaceCIDR(res.cidrs); err != nil {
		return fmt.Errorf("error replace policy_cidr map, %w", err)
	}

	// pod_ip 身份表全量 upsert（daemon 建链时已写当前 pod，此处收敛到全集群视图）
	current := make(map[string]uint32, len(pods))
	for _, p := range pods {
		current[p.ip] = p.identity
		if err := r.backend.UpsertPodIP(net.ParseIP(p.ip), p.identity); err != nil {
			return fmt.Errorf("error upsert pod ip %s, %w", p.ip, err)
		}
	}

	// 删除消失的 pod_ip：仅限本 reconciler 上次成功写入过的条目，
	// 避免 diff 窗口内误删 daemon AllocIP 刚写入、缓存尚未同步的 pod
	r.mu.Lock()
	var deleteErrs []string
	for ip := range r.syncedIPs {
		if _, ok := current[ip]; !ok {
			if err := r.backend.DeletePodIP(net.ParseIP(ip)); err != nil {
				deleteErrs = append(deleteErrs, ip)
			}
		}
	}
	r.syncedIPs = current
	r.mu.Unlock()
	if len(deleteErrs) > 0 {
		return fmt.Errorf("error delete stale pod ips %v", deleteErrs)
	}
	return nil
}

// listPods 从 manager 缓存生成 pod 快照（hostNetwork / 无 IPv4 的 pod 不参与）
func (r *Reconciler) listPods(ctx context.Context, nsLabels map[string]map[string]string) ([]*podState, error) {
	list := &corev1.PodList{}
	if err := r.client.List(ctx, list); err != nil {
		return nil, err
	}
	out := make([]*podState, 0, len(list.Items))
	for i := range list.Items {
		pod := &list.Items[i]
		if pod.Spec.HostNetwork {
			continue // 归节点身份（IdentityHost），不参与策略计算（设计 §3/§8）
		}
		ip := net.ParseIP(pod.Status.PodIP)
		if ip == nil || ip.To4() == nil {
			continue // Pending / 非 IPv4
		}
		out = append(out, &podState{
			namespace: pod.Namespace,
			name:      pod.Name,
			ip:        pod.Status.PodIP,
			labels:    pod.Labels,
			nsLabels:  nsLabels[pod.Namespace],
			identity:  IdentityFor(pod.Namespace, pod.Labels),
		})
	}
	return out, nil
}

func (r *Reconciler) listPolicies(ctx context.Context) ([]*networkingv1.NetworkPolicy, error) {
	list := &networkingv1.NetworkPolicyList{}
	if err := r.client.List(ctx, list); err != nil {
		return nil, err
	}
	out := make([]*networkingv1.NetworkPolicy, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, &list.Items[i])
	}
	return out, nil
}

// nsLabelIndex 构建命名空间 → 标签 快照（namespaceSelector 匹配用）
func (r *Reconciler) nsLabelIndex(ctx context.Context) (map[string]map[string]string, error) {
	list := &corev1.NamespaceList{}
	if err := r.client.List(ctx, list); err != nil {
		return nil, err
	}
	out := make(map[string]map[string]string, len(list.Items))
	for i := range list.Items {
		out[list.Items[i].Name] = list.Items[i].Labels
	}
	return out, nil
}
