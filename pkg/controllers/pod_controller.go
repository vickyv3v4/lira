package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/lib/xcontext"
	"gitlab.msxf.com/mscloud/lira/pkg/api/v1beta1"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"
	"gitlab.msxf.com/mscloud/lira/pkg/msxf"
	"gitlab.msxf.com/mscloud/lira/pkg/types"
	"gitlab.msxf.com/mscloud/lira/pkg/utils"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	k8sErr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
)

const controllerName = "pod"

type Wrapper struct {
	ctrl controller.Controller
}

// Start the controller
func (w *Wrapper) Start(ctx context.Context) error {
	err := w.ctrl.Start(ctx)
	if err != nil {
		return err
	}

	<-ctx.Done()
	return nil
}

func init() {
	Add(controllerName, func(mgr manager.Manager, ctrlCtx *ControllerCtx) error {
		ctrlCtx.RegisterResource = append(ctrlCtx.RegisterResource, &corev1.Pod{})

		c, err := controller.NewUnmanaged(controllerName, mgr, controller.Options{
			Reconciler:              NewReconcilePod(mgr, ctrlCtx.MsxfClient, llog.Logger, ctrlCtx.MsConfig),
			MaxConcurrentReconciles: 1,
		})
		if err != nil {
			return err
		}

		w := &Wrapper{
			ctrl: c,
		}
		err = mgr.Add(w)
		if err != nil {
			return err
		}
		return c.Watch(
			source.Kind(mgr.GetCache(), &corev1.Pod{}),
			&handler.EnqueueRequestForObject{},
			&predicate.ResourceVersionChangedPredicate{},
			&predicateForPodEvent{},
		)
	}, true)
}

// ReconcilePod reconciles a AutoRepair object
type ReconcilePod struct {
	client client.Client
	scheme *runtime.Scheme

	//record event recorder
	record     record.EventRecorder
	msxfClient msxf.MsxfClientAPI
	log        *logrus.Logger
	MSconfig   *MSConfig
	nodeMutex  sync.Mutex
}

// NewReconcilePod watch pod lifecycle events and sync to podENI resource
func NewReconcilePod(mgr manager.Manager, msxfClient msxf.MsxfClientAPI, log *logrus.Logger, msconf *MSConfig) *ReconcilePod {
	r := &ReconcilePod{
		client:     mgr.GetClient(),
		scheme:     mgr.GetScheme(),
		record:     mgr.GetEventRecorderFor("LiraPodController"),
		msxfClient: msxfClient,
		log:        log,
		MSconfig:   msconf,
	}
	return r
}

func (r *ReconcilePod) recordPodDelete(pod *corev1.Pod, startTime time.Time, err error) {
	if err == nil || pod == nil {
		return
	}
	r.record.Eventf(pod, corev1.EventTypeWarning,
		"CniPodDeleteError", fmt.Sprintf("CniPodDeleteError: %s, elapsedTime: %s", err, time.Since(startTime)))
}

func (r *ReconcilePod) recordPodCreate(pod *corev1.Pod, startTime time.Time, err error) {
	if err == nil || pod == nil {
		return
	}
	r.record.Eventf(pod, corev1.EventTypeWarning,
		"CniPodCreateError", fmt.Sprintf("PodCreateError: %s, elapsedTime: %s", err, time.Since(startTime)))
}

func (r *ReconcilePod) getNode(ctx context.Context, name string) (*corev1.Node, error) {
	node := &corev1.Node{}
	err := r.client.Get(ctx, k8stypes.NamespacedName{
		Name: name,
	}, node)
	return node, err
}
func (r *ReconcilePod) patchNodeCr(ctx context.Context, nodeCopy *corev1.Node, node *corev1.Node) error {
	var err error
	retry := 5
	patchObj := client.MergeFrom(node)
	for i := 0; i < retry; i++ {
		err = r.client.Patch(ctx, nodeCopy, patchObj)
		if err != nil {
			if i < retry {
				continue
			}
			return err
		}
		return nil
	}
	r.log.Errorf("max retries reached, update node failed")
	return err
}
func (r *ReconcilePod) UpdateNodeENIResource(ctx context.Context, node *corev1.Node) error {
	r.nodeMutex.Lock()
	defer func() {
		r.nodeMutex.Unlock()
	}()
	nodeCopy := node.DeepCopy()
	eniNums := nodeCopy.Labels[types.ENIResourceKey]
	_, e := nodeCopy.Status.Capacity[corev1.ResourceName(types.ENIResourceKey)]
	if !e {
		nodeCopy.Status.Allocatable[corev1.ResourceName(types.ENIResourceKey)] = resource.MustParse(eniNums)
		nodeCopy.Status.Capacity[corev1.ResourceName(types.ENIResourceKey)] = resource.MustParse(eniNums)

		err := r.patchNodeCr(ctx, nodeCopy, node)
		if err != nil {
			r.log.WithError(err).Error("update node cr failed.")
			return err
		}
	}
	return nil
}

func (r *ReconcilePod) podCreateMultiEniIP(ctx context.Context, podCopy *corev1.Pod, podENICopy *v1beta1.PodENI) error {
	podNode := &v1beta1.PodNode{}

	err := r.client.Get(ctx, k8stypes.NamespacedName{
		Name: podCopy.Spec.NodeName,
	}, podNode)
	if err != nil {
		if k8sErr.IsNotFound(err) {
			r.log.Infof("podnode %s is not found.", podNode.Name)
			return fmt.Errorf("podnode is not found")
		}
		return err
	}

	podENICopy.Status.ENIID = podNode.Status.NetworkInterfaces[0].ID
	return nil
}

func (r *ReconcilePod) podCreate(ctx context.Context, pod *corev1.Pod) (reconcile.Result, error) {
	node, err := r.getNode(ctx, pod.Spec.NodeName)
	if err != nil {
		r.log.Errorf("get node failed. %s", pod.Spec.NodeName)
		return reconcile.Result{}, fmt.Errorf("error get node %s, %w", pod.Spec.NodeName, err)
	}
	// check podEni whether if existed
	existPodENI := &v1beta1.PodENI{}
	err = r.client.Get(ctx, k8stypes.NamespacedName{
		Namespace: pod.Namespace,
		Name:      pod.Name,
	}, existPodENI)
	if err == nil {
		r.log.Info("podENI: ", existPodENI.Name, " phase: ", existPodENI.Status.Phase)
		if !existPodENI.DeletionTimestamp.IsZero() {
			return reconcile.Result{Requeue: true}, nil
		}
		switch existPodENI.Status.Phase {
		case v1beta1.ENIPhaseBind:
			if existPodENI.Annotations[types.AnnotationsPodUID] == string(pod.UID) {
				return reconcile.Result{}, nil
			}
			return reconcile.Result{RequeueAfter: 5 * time.Second}, r.client.Delete(ctx, existPodENI)
		}
		return reconcile.Result{Requeue: true}, nil
	}
	// pod is running and podEni not exist indicates that is in upgrade scenario or multi-network scenario,do not need create eni
	if pod.Status.Phase == corev1.PodRunning {
		r.log.Info("pod: ", pod.Namespace, "/", pod.Name, " is running do not create eni")
		return reconcile.Result{}, nil
	}
	ecsID := node.Labels[types.AnnotationsENIECSID]
	if ecsID == "" {
		r.log.Infof("node %s ecsID is null", node.Name)
		return reconcile.Result{RequeueAfter: 1 * time.Second}, errors.New("ecsID is null")
	}
	var nodeIP string
	for _, addr := range node.Status.Addresses {
		if addr.Type == InternalAddrType {
			nodeIP = addr.Address
			break
		}
	}
	// need create new podEni
	podENI := &v1beta1.PodENI{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
			Finalizers: []string{
				types.FinalizerPodENI,
			},
			Annotations: map[string]string{
				types.AnnotationsPodUID: string(pod.UID),
			},
			Labels: map[string]string{
				types.AnnotationsENINodeName: pod.Spec.NodeName,
				types.AnnotationsENIECSID:    ecsID,
			},
		},
		Spec: v1beta1.PodENISpec{
			NodeIP:      nodeIP,
			ServiceCidr: r.MSconfig.ServiceCidr,
		},
	}

	// create podEni cr
	err = r.client.Create(ctx, podENI)
	if err != nil {
		r.log.WithError(err).Error("create podeni cr failed.")
		return reconcile.Result{}, err
	}
	podENICopy := podENI.DeepCopy()
	// multi-ip mode
	if utils.GConfig.RunMode == utils.MultiENIIP {
		err := r.podCreateMultiEniIP(ctx, pod.DeepCopy(), podENICopy)
		if err != nil {
			if strings.Contains(err.Error(), "podnode is not found") {
				return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
			}
			r.log.WithError(err).Error("create multi-ip podeni cr status failed.")
			return reconcile.Result{}, err
		}
	}

	podENICopy.Status.NodeName = pod.Spec.NodeName
	podENICopy.Status.ECSID = ecsID
	err = r.client.Status().Update(ctx, podENICopy)
	if err != nil {
		r.log.WithError(err).Error("patch podeni cr status failed.")
		return reconcile.Result{}, err
	}
	r.log.Info("has patched podeni cr status")

	defer func() {
		if err != nil {
			r.log.WithError(err).Error("error happend,podCreate ")
		}
	}()

	_ = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		podENI := &v1beta1.PodENI{}
		err := r.client.Get(ctx, k8stypes.NamespacedName{
			Namespace: pod.Namespace,
			Name:      pod.Name,
		}, podENI)
		if err != nil {
			return false, nil
		}
		return true, nil
	})
	return reconcile.Result{}, nil
}

func (r *ReconcilePod) podDelete(ctx context.Context, namespacedName client.ObjectKey) (reconcile.Result, error) {
	r.log.Infof("podDelete: %s", namespacedName.String())
	prePodENI := &v1beta1.PodENI{}
	err := r.client.Get(ctx, namespacedName, prePodENI)
	if err != nil {
		r.log.Errorf("get podeni %s, err %s", namespacedName, err.Error())
		if k8sErr.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	// already deleting
	if prePodENI.Status.Phase == v1beta1.ENIPhaseDeleting || !prePodENI.DeletionTimestamp.IsZero() {
		r.log.Errorf("podeni %s has been deleting", namespacedName)
		return reconcile.Result{}, nil
	}
	update := prePodENI.DeepCopy()
	update.Status.Phase = v1beta1.ENIPhaseDeleting
	err = r.client.Status().Update(ctx, update)

	return reconcile.Result{}, err
}

func (r *ReconcilePod) DeleteENI(ctx context.Context, podEni *v1beta1.PodENI) error {
	requestId := utils.GenerateRequestId()
	data := &xcontext.CloudPlatform{
		Type: xcontext.PortalPlatform,
		Portal: &xcontext.Portal{
			RequestID:      requestId,
			CloudAccountID: r.MSconfig.CloudAccountID,
			TenantID:       r.MSconfig.TenantID,
		},
	}
	nctx := xcontext.MockContext(ctx, data)
	nctx = context.WithValue(nctx, PlatformPropagationKey, xcontext.MockPortalHeader(r.MSconfig.CloudAccountID, "", r.MSconfig.TenantID, requestId))
	nctx = context.WithValue(nctx, CtxPropagationType, PPortalPlatform)
	err := r.msxfClient.DeleteENI(nctx, &msxf.DeleteNetworkInterfaceRequest{
		ID:             podEni.Spec.ENI.ID,
		TenantID:       r.MSconfig.TenantID,
		RequestId:      requestId,
		CloudAccountID: r.MSconfig.CloudAccountID,
	})
	if err != nil {
		return err
	}
	return nil
}

func (r *ReconcilePod) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	r.log.Infof("reconcile pod %s/%s", request.Namespace, request.Name)

	start := time.Now()

	pod := &corev1.Pod{}
	if err := r.client.Get(ctx, request.NamespacedName, pod); err != nil {
		if k8sErr.IsNotFound(err) {
			result, err := r.podDelete(ctx, request.NamespacedName)
			r.recordPodDelete(pod, start, err)
			return result, err
		}
		return reconcile.Result{}, err
	}

	if utils.PodSandboxExited(pod) {
		result, err := r.podDelete(ctx, request.NamespacedName)
		r.recordPodDelete(pod, start, err)
		return result, err
	}

	if !pod.DeletionTimestamp.IsZero() {
		r.log.Infof("pod %s is terminating", pod.Name)
		return reconcile.Result{RequeueAfter: 2 * time.Second}, nil
	}

	res, err := r.podCreate(ctx, pod)
	r.recordPodCreate(pod, start, err)
	return res, err
}

type predicateForPodEvent struct {
	predicate.Funcs
}

func (p *predicateForPodEvent) Create(e event.CreateEvent) bool {
	return processPod(e.Object)
}

func (p *predicateForPodEvent) Update(e event.UpdateEvent) bool {
	return processPod(e.ObjectNew)
}

func (p *predicateForPodEvent) Delete(e event.DeleteEvent) bool {
	return processPod(e.Object)
}

func (p *predicateForPodEvent) Generic(e event.GenericEvent) bool {
	return processPod(e.Object)
}

func processPod(o client.Object) bool {
	pod, ok := o.(*corev1.Pod)
	if !ok {
		return false
	}

	if pod.Spec.NodeName == "" ||
		pod.Spec.HostNetwork {
		return false
	}
	return true
}
