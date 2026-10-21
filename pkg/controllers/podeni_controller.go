/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cast"
	corev1 "k8s.io/api/core/v1"
	k8sErr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"gitlab.msxf.com/mscloud/cloud-infrastructure-sdk/v2/lib/xcontext"
	"gitlab.msxf.com/mscloud/lira/pkg/api/v1beta1"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"
	"gitlab.msxf.com/mscloud/lira/pkg/msxf"
	"gitlab.msxf.com/mscloud/lira/pkg/types"
	"gitlab.msxf.com/mscloud/lira/pkg/utils"
)

const podENIControllerName = "podENI"

type PodENIWrapper struct {
	ctrl controller.Controller
}

// Start the controller
func (w *PodENIWrapper) Start(ctx context.Context) error {
	err := w.ctrl.Start(ctx)
	if err != nil {
		return err
	}

	<-ctx.Done()
	return nil
}

func init() {
	Add(podENIControllerName, func(mgr manager.Manager, ctrlCtx *ControllerCtx) error {
		ctrlCtx.RegisterResource = append(ctrlCtx.RegisterResource, &v1beta1.PodENI{})
		c, err := controller.NewUnmanaged(podENIControllerName, mgr, controller.Options{
			Reconciler:              NewReconcilePodENI(mgr, llog.Logger, ctrlCtx.MsxfClient, ctrlCtx.MsConfig),
			MaxConcurrentReconciles: ctrlCtx.PodENICurrent,
			RateLimiter: workqueue.NewItemExponentialFailureRateLimiter(
				2*time.Second,
				5*time.Minute,
			),
		})
		if err != nil {
			return err
		}

		w := &PodENIWrapper{
			ctrl: c,
		}

		err = mgr.Add(w)
		if err != nil {
			return err
		}
		return c.Watch(
			source.Kind(mgr.GetCache(), &v1beta1.PodENI{}),
			&handler.EnqueueRequestForObject{},
			&predicate.ResourceVersionChangedPredicate{},
			&predicateForPodENIEvent{},
		)
	}, true)
}

type predicateForPodENIEvent struct {
	predicate.Funcs
}

func (p *predicateForPodENIEvent) Create(e event.CreateEvent) bool {
	return processPodENI(e.Object)
}

func (p *predicateForPodENIEvent) Update(e event.UpdateEvent) bool {
	return processPodENI(e.ObjectNew)
}

func (p *predicateForPodENIEvent) Delete(e event.DeleteEvent) bool {
	return processPodENI(e.Object)
}

func (p *predicateForPodENIEvent) Generic(e event.GenericEvent) bool {
	return processPodENI(e.Object)
}

func processPodENI(o client.Object) bool {
	podEni, ok := o.(*v1beta1.PodENI)
	if !ok {
		return false
	}

	if podEni.Status.NodeName == "" {
		return false
	}
	if podEni.Status.ENIID == "" {
		return false
	}
	return true
}

func NewReconcilePodENI(mgr manager.Manager, log *logrus.Logger, msxfClient msxf.MsxfClientAPI, msconf *MSConfig) *PodENIReconciler {
	return &PodENIReconciler{
		client:     mgr.GetClient(),
		scheme:     mgr.GetScheme(),
		log:        log,
		record:     mgr.GetEventRecorderFor("liraPodENIController"),
		msxfClient: msxfClient,
		MSconfig:   msconf,
	}
}

var DefaultRetry = wait.Backoff{
	Steps:    50,
	Duration: 10 * time.Millisecond,
	Factor:   1.0,
	Jitter:   0.1,
}

// PodENIReconciler reconciles a PodENI object
type PodENIReconciler struct {
	client     client.Client
	scheme     *runtime.Scheme
	log        *logrus.Logger
	record     record.EventRecorder
	msxfClient msxf.MsxfClientAPI
	MSconfig   *MSConfig
	nodeMutex  sync.Mutex
}

//+kubebuilder:rbac:groups=network.msxf.com,resources=podenis,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=network.msxf.com,resources=podenis/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network.msxf.com,resources=podenis/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the PodENI object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.2/pkg/reconcile
func (r *PodENIReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.log.Infof("podEni %s reconcile...", req.NamespacedName)
	start := time.Now()
	podENI := &v1beta1.PodENI{}
	err := r.client.Get(ctx, req.NamespacedName, podENI)
	if err != nil {
		if k8sErr.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if !podENI.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(podENI, types.FinalizerPodENI) {
			return reconcile.Result{}, nil
		}
		result, err := r.podENIDelete(ctx, podENI)
		r.recordPodENIDeleteErr(podENI, start, err)
		return result, err
	}
	result, err := r.podENICreate(ctx, req.NamespacedName, podENI, start)
	if err != nil {
		r.recordPodENICreateErr(podENI, start, err)
		return result, err
	}

	return result, nil
}

func (r *PodENIReconciler) getNode(ctx context.Context, name string) (*corev1.Node, error) {
	node := &corev1.Node{}
	err := r.client.Get(ctx, k8stypes.NamespacedName{
		Name: name,
	}, node)
	return node, err
}

func (r *PodENIReconciler) patchNodeCr(ctx context.Context, nodeCopy *corev1.Node, node *corev1.Node) error {
	var err error
	patchObj := client.MergeFrom(node)
	err = r.client.Patch(ctx, nodeCopy, patchObj)
	if err != nil {
		return err
	}
	return nil
}

// 校验节点ENI配额是否满足，每个节点规格限制了可绑定的ENI个数
func (r *PodENIReconciler) checkNodeENIConfig(ctx context.Context, node *corev1.Node) error {
	r.nodeMutex.Lock()
	defer func() {
		r.nodeMutex.Unlock()
	}()

	nodeCopy := node.DeepCopy()

	var eniCnt int
	podEniList := &v1beta1.PodENIList{}
	err := r.client.List(ctx, podEniList)
	if err != nil {
		r.log.Errorf("list podeni failed. %s", err)
		return err
	}
	eniCnt = 0
	for _, podEni := range podEniList.Items {
		if podEni.Spec.ENI.ID != "" && podEni.Status.NodeName == node.Name {
			eniCnt++
		}
	}

	c, s := nodeCopy.Status.Capacity[corev1.ResourceName(types.ENIResourceKey)]
	if !s {
		r.log.Errorf("node %s do not have capacity resource %s", node.Name, types.ENIResourceKey)
		return errors.New("node do not have annotation")
	}
	/*
		nums, err := strconv.Atoi(v)
		if err != nil {
			r.log.Errorf("Error convert eniNUMS to Integer.")
			return errors.New("error convert eniNUMS to Integer")
		}
	*/
	totalNum := int(c.Value())

	if totalNum <= 0 {
		r.log.Errorf("Node %s %s num is  %d", node.Name, corev1.ResourceName(types.ENIResourceKey), totalNum)
		return fmt.Errorf("node %s %s num is  %d", node.Name, corev1.ResourceName(types.ENIResourceKey), totalNum)
	}
	r.log.Infof("node %s eni allocat %d, eni total %d", node.Name, eniCnt, totalNum)
	if eniCnt >= totalNum {
		r.log.Errorf("Node %s has bind enough ENIs,can not more than %d", node.Name, totalNum)
		return errors.New(ErrorInsufficientENINUM)
	}
	return nil
}
func (r *PodENIReconciler) IsMasterNode(node *corev1.Node) bool {
	labels := node.Labels
	_, ec := labels[types.LabelRoleControl]
	_, em := labels[types.LabelRoleMaster]
	if ec || em {
		return true
	}
	return false
}
func IsResourceVersionConflict(err error) bool {
	return err != nil && err.Error() == "resource version conflict"
}
func (r *PodENIReconciler) retryOnError(backoff wait.Backoff, retriable func(error) bool, fn func() error) error {
	var lastErr error
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		err := fn()
		switch {
		case err == nil || err.Error() == ErrorInsufficientENINUM:
			return true, nil
		case retriable(err):
			lastErr = err
			return false, nil
		default:
			return false, err
		}
	})
	if err == wait.ErrWaitTimeout {
		err = lastErr
	}
	return err
}
func (r *PodENIReconciler) IncreaseNodeENIConfig(ctx context.Context, nodeName string, podENIName string) error {
	r.nodeMutex.Lock()
	defer func() {
		r.nodeMutex.Unlock()
	}()

	err := r.retryOnError(DefaultRetry, IsResourceVersionConflict, func() error {
		node, err := r.getNode(ctx, nodeName)
		if err != nil {
			r.log.Errorf("get node failed. %s", nodeName)
			return fmt.Errorf("error get node %s, %w", nodeName, err)
		}
		nodeCopy := node.DeepCopy()
		v, s := nodeCopy.Annotations[types.AnnotationsENINUMS]
		if !s {
			r.log.Errorf("node %s do not have annotation %s", nodeCopy.Name, types.AnnotationsENINUMS)
			return errors.New("node do not have annotation")
		}
		nums, err := strconv.Atoi(v)
		if err != nil {
			r.log.Errorf("Error convert eniNUMS to Integer.")
			return errors.New("error convert eniNUMS to Integer")
		}
		var nodeENIConf string
		if r.IsMasterNode(nodeCopy) {
			nodeENIConf = r.MSconfig.MasterENINum
		} else {
			eniNums := nodeCopy.Labels[types.ENIResourceKey]
			nodeENIConf = eniNums
		}
		confNum, err := strconv.Atoi(nodeENIConf)
		if err != nil {
			r.log.Errorf("Error convert configEniNUMS to Integer.")
			return errors.New("error convert configEniNUMS to Integer")
		}
		if nums >= confNum {
			r.log.Errorf("Node %s has bind enough ENIs,can not more than %d", nodeCopy.Name, confNum)
			return errors.New(ErrorInsufficientENINUM)
		}
		nums++
		r.log.Infof("increaseNodeENIConfig: podeni %s, node %s, originENIs %s, newENIs %d", podENIName, nodeCopy.Name, v, nums)
		numStr := strconv.Itoa(nums)
		nodeCopy.Annotations[types.AnnotationsENINUMS] = numStr
		err = r.patchNodeCr(ctx, nodeCopy, node)
		if err != nil {
			r.log.Errorf("Error patch node cr.")
			return err
		}
		return nil
	})
	return err
}

func (r *PodENIReconciler) ReduceNodeENIConfig(ctx context.Context, nodeName string, podEniName string) error {
	r.nodeMutex.Lock()
	defer func() {
		r.nodeMutex.Unlock()
	}()
	err := r.retryOnError(DefaultRetry, IsResourceVersionConflict, func() error {
		node, err := r.getNode(ctx, nodeName)
		if err != nil {
			r.log.Errorf("get node failed. %s", nodeName)
			return fmt.Errorf("error get node %s, %w", nodeName, err)
		}
		nodeCopy := node.DeepCopy()
		v, s := nodeCopy.Annotations[types.AnnotationsENINUMS]
		if !s {
			r.log.Errorf("node %s do not have annotation %s", nodeCopy.Name, types.AnnotationsENINUMS)
			return errors.New("node do not have annotation")
		}
		nums, err := strconv.Atoi(v)
		if err != nil {
			r.log.Errorf("Error convert eniNUMS to Integer.")
			return errors.New("error convert eniNUMS to Integer")
		}
		nums--

		if nums < 0 {
			r.log.Errorf("Node %s ENIs %d less than 0", nodeCopy.Name, nums)
			return nil
		}

		r.log.Infof("reduceNodeENIConfig: podeni %s, node %s, originENIs %s, newENIs %d", podEniName, nodeCopy.Name, v, nums)
		numStr := strconv.Itoa(nums)
		nodeCopy.Annotations[types.AnnotationsENINUMS] = numStr
		err = r.patchNodeCr(ctx, nodeCopy, node)
		if err != nil {
			r.log.Errorf("Error patch node cr.")
			return err
		}
		return nil
	})
	return err
}

func (r *PodENIReconciler) deleteENI(ctx context.Context, podEni *v1beta1.PodENI) error {
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

func (r *PodENIReconciler) checkPodIsExist(ctx context.Context, n k8stypes.NamespacedName) (bool, error) {
	pod := &corev1.Pod{}
	err := r.client.Get(ctx, n, pod)
	if err != nil {
		if k8sErr.IsNotFound(err) {
			r.log.Infof("pod %s is not exist", n.Name)
			return false, nil
		}
		return false, err
	}
	if !pod.DeletionTimestamp.IsZero() {
		r.log.Infof("pod %s is terminating", n.Name)
		return false, nil
	}
	switch pod.Status.Phase {
	case corev1.PodSucceeded, corev1.PodFailed:
		return false, nil
	default:
		return true, nil
	}
}

func (r *PodENIReconciler) podENIattachENI(ctx context.Context, podENICopy *v1beta1.PodENI) error {

	reuqestId := utils.GenerateRequestId()
	data := &xcontext.CloudPlatform{
		Type: xcontext.PortalPlatform,
		Portal: &xcontext.Portal{
			RequestID:      reuqestId,
			CloudAccountID: r.MSconfig.CloudAccountID,
			TenantID:       r.MSconfig.TenantID,
		},
	}
	nctx := xcontext.MockContext(ctx, data)
	nctx = context.WithValue(nctx, PlatformPropagationKey, xcontext.MockPortalHeader(r.MSconfig.CloudAccountID, "", r.MSconfig.TenantID, reuqestId))
	nctx = context.WithValue(nctx, CtxPropagationType, PPortalPlatform)
	ecsId, ok := podENICopy.Labels[types.AnnotationsENIECSID]
	if !ok {
		r.log.Error(fmt.Sprintf("podeni %s ecsId label is null", podENICopy.Name))
		return fmt.Errorf("podeni %s ecsId label is null", podENICopy.Name)
	}
	isNumberic, azCode := utils.IsValidNumber(r.MSconfig.AzoneCode)
	if !isNumberic {
		r.log.Errorf("%s is not a vaild number", r.MSconfig.AzoneCode)
		return fmt.Errorf("azcode %s is not a vaild number", r.MSconfig.AzoneCode)
	}
	err := r.msxfClient.AttachENI(nctx, &msxf.AttachNetworkInterfaceRequest{
		ID:             podENICopy.Spec.ENI.ID,
		AZoneCode:      azCode,
		TenantID:       r.MSconfig.TenantID,
		ServerID:       ecsId,
		RequestId:      reuqestId,
		CloudAccountID: r.MSconfig.CloudAccountID,
	})
	if err != nil {
		r.log.WithError(err).Error("attch eni failed.")
		return err
	}
	return nil
}

func (r *PodENIReconciler) podENIDetachENI(ctx context.Context, podENI *v1beta1.PodENI, startTime time.Time) error {

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
	err := r.msxfClient.DetachENI(nctx, &msxf.DetachNetworkInterfaceRequest{
		ID:             podENI.Spec.ENI.ID,
		TenantID:       r.MSconfig.TenantID,
		RequestId:      requestId,
		CloudAccountID: r.MSconfig.CloudAccountID,
	})
	if err != nil {
		r.recordPodENIDettachErr(podENI, startTime, err)
		r.log.WithError(err).Error("podENI dettach eni failed.")
	}
	return err
}

func (r *PodENIReconciler) podENIAllocateENI(ctx context.Context, podENICopy *v1beta1.PodENI, startTime time.Time) (*msxf.ENI, error) {

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

	name := utils.GenerateENIName(podENICopy.Status.ECSID)

	isNumberic, azCode := utils.IsValidNumber(r.MSconfig.AzoneCode)
	if !isNumberic {
		r.log.Errorf("%s is not a vaild number", r.MSconfig.AzoneCode)
		return nil, fmt.Errorf("azcode %s is not a vaild number", r.MSconfig.AzoneCode)
	}
	eni, err := r.msxfClient.AllocateENI(nctx, &msxf.CreateNetworkInterfaceRequest{
		Name:            name,
		Description:     fmt.Sprintf("Created by MCK %s VPC CNI", r.MSconfig.ClusterID),
		TenantID:        r.MSconfig.TenantID,
		RegionCode:      cast.ToInt(r.MSconfig.RegionCode),
		AZoneCode:       azCode,
		NetworkID:       r.MSconfig.VPCID,
		SubnetID:        r.MSconfig.SubnetID,
		SecurityGroups:  r.MSconfig.SecurityGroups,
		OwnerType:       r.MSconfig.OwnerType,
		CloudAccountID:  r.MSconfig.CloudAccountID,
		ResourceGroupID: r.MSconfig.ResourceGroupID,
		RequestId:       requestId,
		GrUniqueID:      podENICopy.Name,
		Type:            2,
	})
	if err != nil {
		r.recordPodENICreateErr(podENICopy, startTime, err)
		r.log.WithError(err).Error("create eni failed.")
		return nil, err
	}
	return eni, nil
}

func (r *PodENIReconciler) podENICreateExcusive(ctx context.Context, podENI *v1beta1.PodENI, startTime time.Time) (result reconcile.Result, err error) {
	var exist bool
	switch podENI.Status.Phase {
	case v1beta1.ENIPhaseBind:
		exist, err = r.checkPodIsExist(ctx, k8stypes.NamespacedName{Namespace: podENI.Namespace, Name: podENI.Name})
		if err != nil {
			r.log.Infof("check pod %s failed, err %s", podENI.Name, err.Error())
			return reconcile.Result{}, err
		}

		if !exist {
			r.log.Infof("pod %s is not exist", podENI.Name)

			podENICopy := podENI.DeepCopy()
			patchObj := client.MergeFrom(podENI)
			podENICopy.Status.Phase = v1beta1.ENIPhaseDeleting
			err = r.client.Status().Patch(ctx, podENICopy, patchObj)
			if err != nil {
				r.log.Errorf("update podENI %s, err %s", podENI.Name, err.Error())
				r.recordPodENIUpdateErr(podENI, startTime, err)
			} else {
				r.log.Infof("update podENI %s", podENI.Name)
			}
			return reconcile.Result{RequeueAfter: 10 * time.Millisecond}, nil
		}
		r.log.Infof("%s already bind", podENI.Name)
		return reconcile.Result{}, nil
	case v1beta1.ENIPhaseDeleting:
		r.log.Infof("delete podeni %s", podENI.Name)
		err = r.client.Delete(ctx, podENI)
		if err != nil {
			if k8sErr.IsNotFound(err) {
				r.log.Info("cr resource not found")
				return reconcile.Result{}, nil
			}
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, nil
	case v1beta1.ENIPhaseCreated:
		exist, err = r.checkPodIsExist(ctx, k8stypes.NamespacedName{Namespace: podENI.Namespace, Name: podENI.Name})
		if err != nil {
			r.log.Infof("check pod %s failed, err %s", podENI.Name, err.Error())
			return reconcile.Result{}, err
		}

		if !exist {
			r.log.Infof("pod %s is not exist", podENI.Name)

			podENICopy := podENI.DeepCopy()
			patchObj := client.MergeFrom(podENI)
			podENICopy.Status.Phase = v1beta1.ENIPhaseDeleting
			err = r.client.Status().Patch(ctx, podENICopy, patchObj)
			if err != nil {
				r.log.Errorf("update podENI %s, err %s", podENI.Name, err.Error())
				r.recordPodENIUpdateErr(podENI, startTime, err)
			} else {
				r.log.Infof("update podENI %s", podENI.Name)
			}
			return reconcile.Result{RequeueAfter: 10 * time.Millisecond}, nil
		}
		defer func() {
			if err != nil {
				r.recordPodENIAttachErr(podENI, startTime, err)
				r.podENIDetachENI(ctx, podENI, startTime)
			}
		}()

		podENICopy := podENI.DeepCopy()
		err = r.podENIattachENI(ctx, podENICopy)
		if err != nil {
			r.log.Errorf("attchENI %s, err %s", podENI.Name, err.Error())
			r.recordPodENIAttachErr(podENI, startTime, err)
			return reconcile.Result{}, err
		}
		patchObj := client.MergeFrom(podENI)
		podENICopy.Status.Phase = v1beta1.ENIPhaseBind
		err = r.client.Status().Patch(ctx, podENICopy, patchObj)
		if err != nil {
			r.log.Errorf("update podENI %s, err %s", podENI.Name, err.Error())
			r.recordPodENIUpdateErr(podENI, startTime, err)
		} else {
			r.log.Infof("update podENI %s", podENI.Name)
		}
		return reconcile.Result{}, err
	case v1beta1.ENIPhaseInitial:
		var err error
		exist, err = r.checkPodIsExist(ctx, k8stypes.NamespacedName{Namespace: podENI.Namespace, Name: podENI.Name})
		if err != nil {
			r.log.Infof("check pod %s failed, err %s", podENI.Name, err.Error())
			return reconcile.Result{}, err
		}

		if !exist {
			r.log.Infof("pod %s is not exist", podENI.Name)

			podENICopy := podENI.DeepCopy()
			patchObj := client.MergeFrom(podENI)
			podENICopy.Status.Phase = v1beta1.ENIPhaseDeleting
			err = r.client.Status().Patch(ctx, podENICopy, patchObj)
			if err != nil {
				r.log.Errorf("update podENI %s, err %s", podENI.Name, err.Error())
				r.recordPodENIUpdateErr(podENI, startTime, err)
			} else {
				r.log.Infof("update podENI %s", podENI.Name)
			}
			return reconcile.Result{RequeueAfter: 10 * time.Millisecond}, nil
		}
		podENICopy := podENI.DeepCopy()
		node, err := r.getNode(ctx, podENI.Status.NodeName)
		if err != nil {
			r.log.Errorf("get node failed. %s", podENI.Status.NodeName)
			return reconcile.Result{}, fmt.Errorf("error get node %s, %w", podENI.Status.NodeName, err)
		}
		err = r.checkNodeENIConfig(ctx, node)
		if err != nil {
			if err.Error() == ErrorInsufficientENINUM {
				r.recordPodENIUpdateErr(podENI, startTime, err)
				return reconcile.Result{}, err
			}
			return reconcile.Result{}, err
		}

		defer func() {
			if err != nil {
				err = r.deleteENI(ctx, podENICopy)
				if err != nil {
					r.recordPodENIDeleteErr(podENICopy, startTime, err)
					r.log.Infof("err %s, delete eni %s", err, podENICopy.Name)
				}
			}
		}()
		eni, err := r.podENIAllocateENI(ctx, podENICopy, startTime)
		if err != nil {
			return reconcile.Result{}, err
		}
		podENICopy.Spec.ENI.Description = eni.Description
		podENICopy.Spec.ENI.ID = eni.ID
		podENICopy.Spec.ENI.IPAddress = eni.IPAddress
		podENICopy.Spec.ENI.MACAddress = eni.MacAddress
		podENICopy.Spec.ENI.Name = eni.Name
		podENICopy.Spec.ENI.PodCidr = eni.Subnet.CIDR
		podENICopy.Spec.ENI.GatewayIP = eni.Subnet.GatewayIP
		pathObj := client.MergeFrom(podENI)
		err = r.client.Patch(ctx, podENICopy, pathObj)
		if err != nil {
			r.log.Error(err, "patch podENI %s spec", podENI.Name)
			r.recordPodENIUpdateErr(podENI, startTime, err)
		} else {
			r.log.Info("patch podENI spec")
		}
		podENICopy.Status.Phase = v1beta1.ENIPhaseCreated
		err = r.client.Status().Patch(ctx, podENICopy, pathObj)
		if err != nil {
			r.log.Error(err, "patch podENI %s status", podENI.Name)
			r.recordPodENIUpdateErr(podENI, startTime, err)
		} else {
			r.log.Infof("patch podENI %s status", podENI.Name)
		}
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func RemoveStringSlice(secondaryIPs []msxf.ENISecondaryPrivateIP, ip string) []msxf.ENISecondaryPrivateIP {
	res := secondaryIPs[:0]
	for _, v := range secondaryIPs {
		if v.IPAddress != ip {
			res = append(res, v)
		}
	}
	return res
}

func (r *PodENIReconciler) podENIReleaseEniSecondaryIP(ctx context.Context, podENICopy *v1beta1.PodENI) error {
	startTime := time.Now()
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

	if podENICopy.Status.ENIID == "" {
		r.log.Errorf("podeni %s eni is null", podENICopy.Name)
		return errors.New("podeni eni id is null")
	}

	eniDetail, err := r.msxfClient.GetENIDetail(nctx, &msxf.DescribeNetworkInterfaceRequest{
		ID:             podENICopy.Status.ENIID,
		TenantID:       r.MSconfig.TenantID,
		CloudAccountID: r.MSconfig.CloudAccountID,
		RequestId:      requestId,
	})
	if err != nil {
		r.recordPodENIReleaseSecondaryIPErr(podENICopy, startTime, err)
		r.log.WithError(err).Error("allocated eni secondary ip failed.")
		return err
	}
	secondaryIPs := eniDetail.SecondaryPrivateIPs
	newSecondaryIPs := RemoveStringSlice(secondaryIPs, podENICopy.Spec.PodIPInfo.IPAddress)
	var fixedIPs []msxf.FixedIPList

	for _, secondaryIP := range newSecondaryIPs {
		fixedIPs = append(fixedIPs, msxf.FixedIPList{
			IPAddress: secondaryIP.IPAddress,
			SubnetID:  secondaryIP.SubnetID,
		})
	}
	r.log.Infof("podENIReleaseEniSecondaryIP: old %v, newSecondaryIPs %v, ipAddress %s", secondaryIPs, newSecondaryIPs, podENICopy.Spec.PodIPInfo.IPAddress)
	_, err = r.msxfClient.HandleENISecondaryIP(nctx, &msxf.HandleENISecondaryIPRequest{
		ID:       podENICopy.Status.ENIID,
		FixedIPs: fixedIPs,
	})
	if err != nil {
		r.log.WithError(err).Error("allocate eni secondary ip failed.")
		return err
	}
	return err
}

func (r *PodENIReconciler) podENIAllocateEniSecondaryIP(ctx context.Context, podENICopy *v1beta1.PodENI) error {
	var nodeMultiEniIPs int = 0
	var eniID string
	var eniAllocatedIPs int = 0

	podNode := &v1beta1.PodNode{}
	podENIList := &v1beta1.PodENIList{}
	err := r.client.Get(ctx, k8stypes.NamespacedName{Name: podENICopy.Status.NodeName}, podNode)
	if err != nil {
		r.log.Errorf("podnode %s is not found. %s", podNode.Name, err)
		return err
	}
	err = r.client.List(ctx, podENIList)
	if err != nil {
		r.log.Errorf("list podEni failed %s.", err)
		return err
	}
	node := &corev1.Node{}
	err = r.client.Get(ctx, k8stypes.NamespacedName{Name: podENICopy.Status.NodeName}, node)
	if err != nil {
		r.log.Errorf("node %s get info failed. %s", podENICopy.Status.NodeName, err)
		return err
	}
	c := node.Status.Capacity[corev1.ResourceName(types.ENIResourceKey)]
	totalEniNum := int(c.Value())
	for _, podEni := range podENIList.Items {
		if podEni.Status.NodeName == podENICopy.Status.NodeName {
			nodeMultiEniIPs++
		}
	}
	if nodeMultiEniIPs >= MaxAllocatableEniNums*totalEniNum {
		return fmt.Errorf("podip had reach the max eni nums, %d", nodeMultiEniIPs)
	}
	podNodeCopy := podNode.DeepCopy()
	r.log.Infof("podENIAllocateEniSecondaryIP: podeni %s", podENICopy.Name)
	// eni上分配超过9个ip 需要使用新eni
	for _, instance := range podNodeCopy.Status.NetworkInterfaces {
		for _, podEni := range podENIList.Items {
			if podEni.Spec.ENI.ID == instance.ID && podEni.Status.Phase == v1beta1.ENIPhaseIPAllocated {
				r.log.Infof("podENIAllocateEniSecondaryIP: podeni %s, allocated %d, eni %s, node %s", podEni.Name, instance.Allocated, instance.ID, podNodeCopy.Name)
				eniAllocatedIPs++
			}
		}
		instance.Allocated = eniAllocatedIPs
		r.log.Infof("podENIAllocateEniSecondaryIP: allocated %d, eni %s, node %s", instance.Allocated, instance.ID, podNodeCopy.Name)
		eniAllocatedIPs = 0
	}
	eniID = podENICopy.Status.ENIID
	for _index, instance := range podNodeCopy.Status.NetworkInterfaces {
		if _index >= len(podNodeCopy.Status.NetworkInterfaces) {
			return fmt.Errorf("podip had reach the max eni nums")
		}
		if instance.Allocated >= MaxAllocatableEniNums {
			r.log.Infof("podENIAllocateEniSecondaryIP: eni index %d, eni id %s", _index, eniID)
			continue
		} else {
			eniID = instance.ID
			break
		}
	}
	r.log.Infof("podENIAllocateEniSecondaryIP: finally get eni id %s", eniID)
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

	eniDetail, err := r.msxfClient.GetENIDetail(nctx, &msxf.DescribeNetworkInterfaceRequest{
		ID:             eniID,
		TenantID:       r.MSconfig.TenantID,
		CloudAccountID: r.MSconfig.CloudAccountID,
		RequestId:      requestId,
	})
	if err != nil {
		r.log.WithError(err).Error("get eni detail failed.")
		return err
	}
	var secondaryIPTmpList []msxf.ENISecondaryPrivateIP
	var fixedIPs []msxf.FixedIPList
	var secondaryList, newSecondaryList []string
	secondaryIPTmpList = eniDetail.SecondaryPrivateIPs
	secondaryIPTmpList = append(secondaryIPTmpList, msxf.ENISecondaryPrivateIP{IPAddress: ""})

	for _, secondaryIP := range eniDetail.SecondaryPrivateIPs {
		secondaryList = append(secondaryList, secondaryIP.IPAddress)
	}

	for _, secondaryIP := range secondaryIPTmpList {
		fixedIPs = append(fixedIPs, msxf.FixedIPList{
			IPAddress: secondaryIP.IPAddress,
			SubnetID:  secondaryIP.SubnetID,
		})
	}

	ipsInfo, err := r.msxfClient.HandleENISecondaryIP(nctx, &msxf.HandleENISecondaryIPRequest{
		ID:       eniID,
		FixedIPs: fixedIPs,
	})
	if err != nil {
		r.log.WithError(err).Error("allocate eni secondary ip failed.")
		return err
	}
	newSecondaryIPs := ipsInfo.SecondaryPrivateIPs
	for _, newSecondaryIP := range newSecondaryIPs {
		newSecondaryList = append(newSecondaryList, newSecondaryIP.IPAddress)
	}
	r.log.Infof("ori second ip list %v, new second ip list %v", secondaryList, newSecondaryList)
	ipList := utils.DiffStringSlice(newSecondaryList, secondaryList)
	if len(ipList) > 1 {
		return fmt.Errorf("allocated secondary ip is more than 1")
	}
	podENICopy.Spec.PodIPInfo.IPAddress = ipList[0]
	podENICopy.Spec.PodIPInfo.SubnetID = eniDetail.Subnet.ID

	podENICopy.Spec.ENI.ID = eniDetail.ID
	podENICopy.Spec.ENI.IPAddress = eniDetail.IPAddress
	podENICopy.Spec.ENI.Name = eniDetail.Name
	podENICopy.Spec.ENI.MACAddress = eniDetail.MacAddress
	podENICopy.Spec.ENI.SecurityGroups = eniDetail.SecurityGroups
	podENICopy.Spec.ENI.Description = eniDetail.Description
	podENICopy.Spec.ENI.PodCidr = eniDetail.Subnet.CIDR
	podENICopy.Spec.ENI.GatewayIP = eniDetail.Subnet.GatewayIP
	return nil
}

func (r *PodENIReconciler) podENICreateMultiEniIP(ctx context.Context, podENI *v1beta1.PodENI, startTime time.Time) (reconcile.Result, error) {
	var exist bool
	switch podENI.Status.Phase {
	case v1beta1.ENIPhaseInitial:
		var err error
		exist, err = r.checkPodIsExist(ctx, k8stypes.NamespacedName{Namespace: podENI.Namespace, Name: podENI.Name})
		if err != nil {
			r.log.Infof("check pod %s failed, err %s", podENI.Name, err.Error())
			return reconcile.Result{}, err
		}

		if !exist {
			r.log.Infof("pod %s is not exist", podENI.Name)

			podENICopy := podENI.DeepCopy()
			patchObj := client.MergeFrom(podENI)
			podENICopy.Status.Phase = v1beta1.ENIPhaseDeleting
			err = r.client.Status().Patch(ctx, podENICopy, patchObj)
			if err != nil {
				r.log.Errorf("update podENI %s, err %s", podENI.Name, err.Error())
				r.recordPodENIUpdateErr(podENI, startTime, err)
			} else {
				r.log.Infof("update podENI %s", podENI.Name)
			}
			return reconcile.Result{RequeueAfter: 10 * time.Millisecond}, nil
		}

		podENICopy := podENI.DeepCopy()
		defer func() {
			if err != nil {
				errs := r.podENIReleaseEniSecondaryIP(ctx, podENICopy)
				if errs != nil {
					r.log.Errorf("release podENI secondary ip %s, err %s", podENI.Name, errs.Error())
				}
				r.recordPodENIReleaseSecondaryIPErr(podENICopy, startTime, err)
			}
		}()

		err = r.podENIAllocateEniSecondaryIP(ctx, podENICopy)
		if err != nil {
			r.log.Errorf("allocate podENI secondary ip %s, err %s", podENI.Name, err.Error())
			r.recordPodENIAllocatedSecondaryIPErr(podENICopy, startTime, err)
			return reconcile.Result{}, err
		}
		pathObj := client.MergeFrom(podENI)
		err = r.client.Patch(ctx, podENICopy, pathObj)
		if err != nil {
			r.log.Error(err, "patch podENI %s spec", podENI.Name)
			r.recordPodENIUpdateErr(podENI, startTime, err)
		} else {
			r.log.Info("patch podENI spec")
		}
		podENICopy.Status.ENIID = podENICopy.Spec.ENI.ID
		podENICopy.Status.Phase = v1beta1.ENIPhaseIPAllocated
		err = r.client.Status().Patch(ctx, podENICopy, pathObj)
		if err != nil {
			r.log.Error(err, "patch podENI %s status", podENI.Name)
			r.recordPodENIUpdateErr(podENI, startTime, err)
		} else {
			r.log.Infof("patch podENI %s status", podENI.Name)
		}

		return reconcile.Result{}, err
	case v1beta1.ENIPhaseIPAllocated:
		exist, err := r.checkPodIsExist(ctx, k8stypes.NamespacedName{Namespace: podENI.Namespace, Name: podENI.Name})
		if err != nil {
			r.log.Infof("check pod %s failed, err %s", podENI.Name, err.Error())
			return reconcile.Result{}, err
		}

		if !exist {
			r.log.Infof("pod %s is not exist", podENI.Name)

			podENICopy := podENI.DeepCopy()
			patchObj := client.MergeFrom(podENI)
			podENICopy.Status.Phase = v1beta1.ENIPhaseDeleting
			err = r.client.Status().Patch(ctx, podENICopy, patchObj)
			if err != nil {
				r.log.Errorf("update podENI %s, err %s", podENI.Name, err.Error())
				r.recordPodENIUpdateErr(podENI, startTime, err)
			} else {
				r.log.Infof("update podENI %s", podENI.Name)
			}
			return reconcile.Result{RequeueAfter: 10 * time.Millisecond}, nil
		}
		r.log.Infof("%s already allocated secondary ip", podENI.Name)
		return reconcile.Result{}, nil
	case v1beta1.ENIPhaseDeleting:
		r.log.Infof("delete podeni %s", podENI.Name)
		err := r.client.Delete(ctx, podENI)
		if err != nil {
			if k8sErr.IsNotFound(err) {
				r.log.Info("cr resource not found")
				return reconcile.Result{}, nil
			}
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, nil
	}
	return reconcile.Result{}, nil
}

func (r *PodENIReconciler) podENICreate(ctx context.Context, _ client.ObjectKey, podENI *v1beta1.PodENI, startTime time.Time) (result reconcile.Result, err error) {
	r.log.Infof("podENI %s creating, phase %s", podENI.Name, podENI.Status.Phase)
	mode := utils.GConfig.RunMode
	r.log.Infof(" lira mode is %s", mode)

	switch mode {
	case utils.ExclusiveENI:
		res, err := r.podENICreateExcusive(ctx, podENI, startTime)
		if err != nil {
			return res, err
		}
	case utils.MultiENIIP:
		res, err := r.podENICreateMultiEniIP(ctx, podENI, startTime)
		if err != nil {
			return res, err
		}
	default:
		r.log.Errorf("lira running mode is %s", mode)
		return reconcile.Result{}, errors.New("lira mode is invalid")
	}
	return reconcile.Result{}, nil
}

func (r *PodENIReconciler) podENIExclusiveDelete(ctx context.Context, podENI *v1beta1.PodENI) (result reconcile.Result, err error) {

	if podENI.Spec.ENI.ID != "" {
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

		err = r.msxfClient.DetachENI(nctx, &msxf.DetachNetworkInterfaceRequest{
			ID:             podENI.Spec.ENI.ID,
			TenantID:       r.MSconfig.TenantID,
			RequestId:      requestId,
			CloudAccountID: r.MSconfig.CloudAccountID,
		})

		if err != nil {
			if !(strings.Contains(err.Error(), "请求资源不存在") || strings.Contains(err.Error(), "此网卡未绑定设备")) {
				r.log.WithError(err).Error("podENI dettach eni failed.")
			}
		}

		err = r.msxfClient.DeleteENI(nctx, &msxf.DeleteNetworkInterfaceRequest{
			ID:             podENI.Spec.ENI.ID,
			TenantID:       r.MSconfig.TenantID,
			RequestId:      requestId,
			CloudAccountID: r.MSconfig.CloudAccountID,
		})
		if err != nil {
			if !strings.Contains(err.Error(), "-11005000") {
				r.log.WithError(err).Error("podENI delete eni failed.")
				return reconcile.Result{}, err
			}
		}
	}
	return reconcile.Result{}, err
}

func (r *PodENIReconciler) podENIDelete(ctx context.Context, podENI *v1beta1.PodENI) (reconcile.Result, error) {
	r.log.Infof("podENI delete %s, phase %s", podENI.Name, podENI.Status.Phase)
	var err error
	podENICopy := podENI.DeepCopy()
	switch utils.GConfig.RunMode {
	case utils.ExclusiveENI:
		_, err = r.podENIExclusiveDelete(ctx, podENI)
	case utils.MultiENIIP:
		err = r.podENIReleaseEniSecondaryIP(ctx, podENI)
	default:
		r.log.Errorf("runmode is invaild")
		return reconcile.Result{}, errors.New("runmode is invaild")
	}
	if err != nil {
		return reconcile.Result{}, err
	}
	controllerutil.RemoveFinalizer(podENICopy, types.FinalizerPodENI)
	err = r.client.Update(ctx, podENICopy)
	if k8sErr.IsNotFound(err) {
		return reconcile.Result{}, nil
	}
	return reconcile.Result{}, nil
}

func (r *PodENIReconciler) recordPodENIUpdateErr(podEni *v1beta1.PodENI, startTime time.Time, err error) {
	if err == nil {
		return
	}
	var pod = &corev1.Pod{}
	if getErr := r.client.Get(context.Background(), k8stypes.NamespacedName{Namespace: podEni.Namespace, Name: podEni.Name}, pod); getErr != nil {
		return
	}
	r.record.Eventf(pod, corev1.EventTypeWarning,
		"CniPodENIUpdateErr", fmt.Sprintf("CniPodENIUpdateErr: %s, elapsedTime: %s", err, time.Since(startTime)))
}

func (r *PodENIReconciler) recordPodENIAttachErr(podEni *v1beta1.PodENI, startTime time.Time, err error) {
	if err == nil {
		return
	}
	var pod = &corev1.Pod{}
	if getErr := r.client.Get(context.Background(), k8stypes.NamespacedName{Namespace: podEni.Namespace, Name: podEni.Name}, pod); getErr != nil {
		return
	}
	r.record.Eventf(pod, corev1.EventTypeWarning,
		"CniPodENIAttachErr", fmt.Sprintf("CniPodENIAttachErr: %s, elapsedTime: %s", err, time.Since(startTime)))
}

func (r *PodENIReconciler) recordPodENIDettachErr(podEni *v1beta1.PodENI, startTime time.Time, err error) {
	if err == nil {
		return
	}
	var pod = &corev1.Pod{}
	if getErr := r.client.Get(context.Background(), k8stypes.NamespacedName{Namespace: podEni.Namespace, Name: podEni.Name}, pod); getErr != nil {
		return
	}
	r.record.Eventf(pod, corev1.EventTypeWarning,
		"CniPodENIDettachErr", fmt.Sprintf("CniPodENIDettachErr: %s, elapsedTime: %s", err, time.Since(startTime)))
}

func (r *PodENIReconciler) recordPodENIDeleteErr(podEni *v1beta1.PodENI, startTime time.Time, err error) {
	if err == nil {
		return
	}
	var pod = &corev1.Pod{}
	if getErr := r.client.Get(context.Background(), k8stypes.NamespacedName{Namespace: podEni.Namespace, Name: podEni.Name}, pod); getErr != nil {
		return
	}
	r.record.Eventf(pod, corev1.EventTypeWarning,
		"CniPodENIDeleteErr", fmt.Sprintf("CniPodENIDeleteErr: %s, elapsedTime: %s", err, time.Since(startTime)))
}

func (r *PodENIReconciler) recordPodENICreateErr(podEni *v1beta1.PodENI, startTime time.Time, err error) {
	if err == nil {
		return
	}
	var pod = &corev1.Pod{}
	if getErr := r.client.Get(context.Background(), k8stypes.NamespacedName{Namespace: podEni.Namespace, Name: podEni.Name}, pod); getErr != nil {
		return
	}
	r.record.Eventf(pod, corev1.EventTypeWarning,
		"CniCreateENIError", fmt.Sprintf("CniCreateENIError: %s, elapsedTime: %s", err, time.Since(startTime)))
}

func (r *PodENIReconciler) recordPodENIAllocatedSecondaryIPErr(podEni *v1beta1.PodENI, startTime time.Time, err error) {
	if err == nil {
		return
	}
	var pod = &corev1.Pod{}
	if getErr := r.client.Get(context.Background(), k8stypes.NamespacedName{Namespace: podEni.Namespace, Name: podEni.Name}, pod); getErr != nil {
		return
	}
	r.record.Eventf(pod, corev1.EventTypeWarning,
		"CniAllocatedENISecondaryIPError", fmt.Sprintf("CniAllocatedENISecondaryIPError: %s, elapsedTime: %s", err, time.Since(startTime)))
}

func (r *PodENIReconciler) recordPodENIReleaseSecondaryIPErr(podEni *v1beta1.PodENI, startTime time.Time, err error) {
	if err == nil {
		return
	}
	var pod = &corev1.Pod{}
	if getErr := r.client.Get(context.Background(), k8stypes.NamespacedName{Namespace: podEni.Namespace, Name: podEni.Name}, pod); getErr != nil {
		return
	}
	r.record.Eventf(pod, corev1.EventTypeWarning,
		"CniReleaseSecondaryIPError", fmt.Sprintf("CniReleaseSecondaryIPError: %s, elapsedTime: %s", err, time.Since(startTime)))
}
