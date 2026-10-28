package k8s

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"

	networkv1beta1 "gitlab.msxf.com/mscloud/lira/pkg/api/v1beta1"
	"gitlab.msxf.com/mscloud/lira/pkg/types"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Kubernetes interface {
	GetPodENI(ctx context.Context, namespace, name string) (*networkv1beta1.PodENI, error)
	ListPodENIs(ctx context.Context) ([]*networkv1beta1.PodENI, error)
	GetPodObj(ctx context.Context, namespace, name string) (*corev1.Pod, error)
	NodeIP() net.IP
	RecordPodEvent(ctx context.Context, podName, podNamespace, eventType, reason, message string) error
	GetConfigMap(ctx context.Context, namespace, name string) (*corev1.ConfigMap, error)
}
type k8sClient struct {
	k8sClient client.Client
	K8sCS     *kubernetes.Clientset
}

var k8sClientInstance *k8sClient

type k8s struct {
	client client.Client

	broadcaster record.EventBroadcaster
	recorder    record.EventRecorder
	//mode                    string
	nodeName        string
	daemonNamespace string
	node            *corev1.Node
	//svcCIDR                 *types.IPNetSet
	//statefulWorkloadKindSet sets.Set[string]

	sync.Locker
}

func (kc *k8sClient) GetK8sClient() *k8sClient {
	return k8sClientInstance
}

func (k *k8s) RecordPodEvent(ctx context.Context, podName, podNamespace, eventType, reason, message string) error {
	pod := &corev1.Pod{}
	err := k.client.Get(ctx, k8stypes.NamespacedName{Namespace: podNamespace, Name: podName}, pod)
	if err != nil {
		return err
	}

	ref := &corev1.ObjectReference{
		Kind:      "Pod",
		Name:      pod.Name,
		UID:       pod.UID,
		Namespace: pod.Namespace,
	}

	k.recorder.Event(ref, eventType, reason, message)
	return nil
}

func (k *k8s) GetPodENI(ctx context.Context, namespace, name string) (*networkv1beta1.PodENI, error) {
	obj := &networkv1beta1.PodENI{}
	err := k.client.Get(ctx, k8stypes.NamespacedName{Namespace: namespace, Name: name}, obj, &client.GetOptions{Raw: &metav1.GetOptions{
		ResourceVersion: "0",
	}})
	return obj, err
}
func (k *k8s) GetPod(ctx context.Context, namespace, name string, cache bool) (*types.PodInfo, error) {
	return nil, nil
}

// ListPodENIs 全量列出 PodENI（网络策略 agent 周期 reconcile 按 Status.NodeName 过滤本节点）
func (k *k8s) ListPodENIs(ctx context.Context) ([]*networkv1beta1.PodENI, error) {
	list := &networkv1beta1.PodENIList{}
	if err := k.client.List(ctx, list); err != nil {
		return nil, err
	}
	out := make([]*networkv1beta1.PodENI, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, &list.Items[i])
	}
	return out, nil
}

// GetPodObj 读取 Pod 原始对象（AllocIP 需要标签计算身份）
func (k *k8s) GetPodObj(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	err := k.client.Get(ctx, k8stypes.NamespacedName{Namespace: namespace, Name: name}, pod)
	return pod, err
}

// NodeIP 返回节点内部 IP（ebpf 数据面节点旁路用），无 InternalIP 时退回 ExternalIP
func (k *k8s) NodeIP() net.IP {
	var ext net.IP
	for _, addr := range k.node.Status.Addresses {
		switch addr.Type {
		case corev1.NodeInternalIP:
			return net.ParseIP(addr.Address)
		case corev1.NodeExternalIP:
			ext = net.ParseIP(addr.Address)
		}
	}
	return ext
}
func (k *k8s) GetConfigMap(ctx context.Context, namespace, name string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	err := k.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, cm)
	return cm, err
}
func getNode(ctx context.Context, c client.Client, nodeName string) (*corev1.Node, error) {
	obj := &corev1.Node{}
	err := c.Get(ctx, k8stypes.NamespacedName{Name: nodeName}, obj)
	return obj, err
}

func NewK8S(namespace string) (Kubernetes, error) {
	restConfig := ctrl.GetConfigOrDie()

	c, err := client.New(restConfig, client.Options{
		Scheme: types.Scheme,
	})
	if err != nil {
		return nil, err
	}
	k8sClientInstance = &k8sClient{}
	k8sClientInstance.k8sClient = c
	k8sClientInstance.K8sCS = kubernetes.NewForConfigOrDie(restConfig)

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		return nil, fmt.Errorf("failed to get NODE_NAME")
	}

	node, err := getNode(context.Background(), c, nodeName)
	if err != nil {
		return nil, fmt.Errorf("error retrieving node spec for '%s': %w", nodeName, err)
	}

	broadcaster := record.NewBroadcaster()
	source := corev1.EventSource{Component: "lira-daemon"}
	recorder := broadcaster.NewRecorder(scheme.Scheme, source)

	sink := &typedv1.EventSinkImpl{
		Interface: typedv1.New(k8sClientInstance.K8sCS.CoreV1().RESTClient()).Events(""),
	}
	broadcaster.StartRecordingToSink(sink)
	k8sObj := &k8s{
		client:          c,
		node:            node,
		nodeName:        nodeName,
		daemonNamespace: namespace,
		broadcaster:     broadcaster,
		recorder:        recorder,
		Locker:          &sync.RWMutex{},
	}
	return k8sObj, nil
}
