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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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
		c, err := controller.NewUnmanaged(controllerName, mgr, controller.Options{
			Reconciler:              NewReconcilePodENI(mgr, llog.Logger, ctrlCtx.MsxfClient, ctrlCtx.MsConfig),
			MaxConcurrentReconciles: ctrlCtx.PodENICurrent,
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
		)
	}, true)
}

func NewReconcilePodENI(mgr manager.Manager, log *logrus.Logger, msxfClient *msxf.MsxfClient, msconf *MSConfig) *PodENIReconciler {
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
	msxfClient *msxf.MsxfClient
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
		result, err := r.podENIDelete(ctx, podENI, start)
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
		return fmt.Errorf("Node %s %s num is  %d", node.Name, corev1.ResourceName(types.ENIResourceKey), totalNum)
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
	nctx = context.WithValue(nctx, xcontext.PropagationKey, xcontext.MockPortalHeader(r.MSconfig.CloudAccountID, "", r.MSconfig.TenantID, requestId))
	nctx = context.WithValue(nctx, xcontext.PropagationType, xcontext.PortalPlatform)
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
func (r *PodENIReconciler) podENICreate(ctx context.Context, _ client.ObjectKey, podENI *v1beta1.PodENI, startTime time.Time) (result reconcile.Result, err error) {
	r.log.Infof("podENI %s creating, phase %s", podENI.Name, podENI.Status.Phase)

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
				nctx = context.WithValue(nctx, xcontext.PropagationKey, xcontext.MockPortalHeader(r.MSconfig.CloudAccountID, "", r.MSconfig.TenantID, requestId))
				nctx = context.WithValue(nctx, xcontext.PropagationType, xcontext.PortalPlatform)
				err = r.msxfClient.DetachENI(nctx, &msxf.DetachNetworkInterfaceRequest{
					ID:             podENI.Spec.ENI.ID,
					TenantID:       r.MSconfig.TenantID,
					RequestId:      requestId,
					CloudAccountID: r.MSconfig.CloudAccountID,
				})
				if err != nil {
					r.recordPodENIDettachErr(podENI, startTime, err)
					r.log.WithError(err).Error("podENI dettach eni failed.")
				}
			}
		}()

		podENICopy := podENI.DeepCopy()

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
		nctx = context.WithValue(nctx, xcontext.PropagationKey, xcontext.MockPortalHeader(r.MSconfig.CloudAccountID, "", r.MSconfig.TenantID, reuqestId))
		nctx = context.WithValue(nctx, xcontext.PropagationType, xcontext.PortalPlatform)
		ecsId, ok := podENICopy.Labels[types.AnnotationsENIECSID]
		if !ok {
			r.log.Error(fmt.Sprintf("podeni %s ecsId label is null", podENICopy.Name))
			return reconcile.Result{}, fmt.Errorf("podeni %s ecsId label is null", podENICopy.Name)
		}
		isNumberic, azCode := utils.IsValidNumber(r.MSconfig.AzoneCode)
		if !isNumberic {
			r.log.Errorf("%s is not a vaild number", r.MSconfig.AzoneCode)
			return reconcile.Result{}, fmt.Errorf("azcode %s is not a vaild number", r.MSconfig.AzoneCode)
		}
		err = r.msxfClient.AttachENI(nctx, &msxf.AttachNetworkInterfaceRequest{
			ID:             podENICopy.Spec.ENI.ID,
			AZoneCode:      azCode,
			TenantID:       r.MSconfig.TenantID,
			ServerID:       ecsId,
			RequestId:      reuqestId,
			CloudAccountID: r.MSconfig.CloudAccountID,
		})
		if err != nil {
			r.log.WithError(err).Error("attch eni failed.")
			r.recordPodENIAttachErr(podENICopy, startTime, err)
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
				//r.reduceNodeENIConfig(ctx, node.Name, podENICopy.Name)
				//r.log.Infof("err %s, reduceENINum node %s", err, node.Name)
				err = r.deleteENI(ctx, podENICopy)
				if err != nil {
					r.recordPodENIDeleteErr(podENICopy, startTime, err)
					r.log.Infof("err %s, delete eni %s", err, podENICopy.Name)
				}
			}
		}()
		/*
			err = r.increaseNodeENIConfig(ctx, node.Name, podENICopy.Name)
			if err != nil {
				r.recordPodENIUpdateErr(podENI, startTime, err)
				if err.Error() == ErrorInsufficientENINUM {
					return reconcile.Result{}, nil
				}
				r.log.WithError(err).Error("increase node eni config failed.")
				return reconcile.Result{}, err
			}
		*/
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
		nctx = context.WithValue(nctx, xcontext.PropagationKey, xcontext.MockPortalHeader(r.MSconfig.CloudAccountID, "", r.MSconfig.TenantID, requestId))
		nctx = context.WithValue(nctx, xcontext.PropagationType, xcontext.PortalPlatform)

		name := utils.GenerateENIName(podENICopy.Status.ECSID)

		isNumberic, azCode := utils.IsValidNumber(r.MSconfig.AzoneCode)
		if !isNumberic {
			r.log.Errorf("%s is not a vaild number", r.MSconfig.AzoneCode)
			return reconcile.Result{}, fmt.Errorf("azcode %s is not a vaild number", r.MSconfig.AzoneCode)
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
			GrUniqueID:      podENI.Name,
			Type:            2,
		})
		if err != nil {
			r.recordPodENICreateErr(podENICopy, startTime, err)
			r.log.WithError(err).Error("create eni failed.")
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

func (r *PodENIReconciler) podENIDelete(ctx context.Context, podENI *v1beta1.PodENI, startTime time.Time) (result reconcile.Result, err error) {
	r.log.Infof("podENI delete %s, phase %s", podENI.Name, podENI.Status.Phase)

	podENICopy := podENI.DeepCopy()
	/*
		node, err := r.getNode(ctx, podENICopy.Status.NodeName)
		if err != nil {
			r.log.Errorf("get node failed. %s", podENICopy.Status.NodeName)
			return reconcile.Result{}, fmt.Errorf("error get node %s, %w", podENICopy.Status.NodeName, err)
		}
	*/
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
		nctx = context.WithValue(nctx, xcontext.PropagationKey, xcontext.MockPortalHeader(r.MSconfig.CloudAccountID, "", r.MSconfig.TenantID, requestId))
		nctx = context.WithValue(nctx, xcontext.PropagationType, xcontext.PortalPlatform)

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
		/*
			err = r.reduceNodeENIConfig(ctx, node.Name, podENICopy.Name)
			if err != nil {
				r.log.Errorf("reduce node eniconfig failed. %s", podENICopy.Status.NodeName)
				return reconcile.Result{}, fmt.Errorf("reduce node eniconfig failed %s, %w", podENICopy.Status.NodeName, err)
			}
		*/
	}
	controllerutil.RemoveFinalizer(podENICopy, types.FinalizerPodENI)
	err = r.client.Update(ctx, podENICopy)
	if k8sErr.IsNotFound(err) {
		return reconcile.Result{}, nil
	}
	return reconcile.Result{}, err
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
