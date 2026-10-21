package controllers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cast"
	corev1 "k8s.io/api/core/v1"
	k8sErr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
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

const resourceDeviceManager = "resDevManager"

func init() {
	AddRes(resourceDeviceManager, func(mgr manager.Manager, ctrlCtx *ControllerCtx) error {
		llog.Logger.Info("add resource resDevManager")
		ctrlCtx.RegisterResource = append(ctrlCtx.RegisterResource, &v1beta1.PodNode{})
		c, err := controller.NewUnmanaged(resourceDeviceManager, mgr, controller.Options{
			Reconciler:              NewReconcilePodNode(mgr, ctrlCtx.MsxfClient, llog.Logger, ctrlCtx.MsConfig),
			MaxConcurrentReconciles: 1,
			RateLimiter: workqueue.NewItemExponentialFailureRateLimiter(
				2*time.Second,
				5*time.Minute,
			),
		})
		if err != nil {
			return err
		}

		err = mgr.Add(c)
		if err != nil {
			return err
		}
		err = c.Watch(
			source.Kind(mgr.GetCache(), &corev1.Node{}),
			&ResourceTypeHandler{Kind: "Node"},
			&predicate.ResourceVersionChangedPredicate{})
		if err != nil {
			return err
		}
		err = c.Watch(
			source.Kind(mgr.GetCache(), &v1beta1.PodENI{}),
			&ResourceTypeHandler{Kind: "PodENI"},
			&predicate.ResourceVersionChangedPredicate{},
			&predicateForPodNodeEvent{},
		)
		if err != nil {
			return err
		}
		err = c.Watch(
			source.Kind(mgr.GetCache(), &v1beta1.PodNode{}),
			&ResourceTypeHandler{Kind: "PodNode"},
			&predicate.ResourceVersionChangedPredicate{},
			&predicateForPodNodeEvent{},
			&predicatePodNodeForPodENIEvent{},
		)
		return err
	}, true)
}

type predicatePodNodeForPodENIEvent struct {
	predicate.Funcs
}

func (p *predicatePodNodeForPodENIEvent) Create(e event.CreateEvent) bool {
	return processPodNodePodENI(e.Object)
}

func (p *predicatePodNodeForPodENIEvent) Update(e event.UpdateEvent) bool {
	return processPodNodePodENI(e.ObjectNew)
}

func (p *predicatePodNodeForPodENIEvent) Delete(e event.DeleteEvent) bool {
	return processPodNodePodENI(e.Object)
}

func (p *predicatePodNodeForPodENIEvent) Generic(e event.GenericEvent) bool {
	return processPodNodePodENI(e.Object)
}

func processPodNodePodENI(o client.Object) bool {
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

type ResourceTypeHandler struct {
	Kind string
}

func (r *ResourceTypeHandler) Create(ctx context.Context, evt event.CreateEvent, q workqueue.RateLimitingInterface) {
	key := fmt.Sprintf("%s/%s/%s", r.Kind, evt.Object.GetNamespace(), evt.Object.GetName())
	q.Add(reconcile.Request{NamespacedName: k8stypes.NamespacedName{Name: key}})
}
func (r *ResourceTypeHandler) Delete(ctx context.Context, evt event.DeleteEvent, q workqueue.RateLimitingInterface) {
	key := fmt.Sprintf("%s/%s/%s", r.Kind, evt.Object.GetNamespace(), evt.Object.GetName())
	q.Add(reconcile.Request{NamespacedName: k8stypes.NamespacedName{Name: key}})
}
func (r *ResourceTypeHandler) Generic(ctx context.Context, evt event.GenericEvent, q workqueue.RateLimitingInterface) {
	key := fmt.Sprintf("%s/%s/%s", r.Kind, evt.Object.GetNamespace(), evt.Object.GetName())
	q.Add(reconcile.Request{NamespacedName: k8stypes.NamespacedName{Name: key}})
}
func (r *ResourceTypeHandler) Update(ctx context.Context, evt event.UpdateEvent, q workqueue.RateLimitingInterface) {
	key := fmt.Sprintf("%s/%s/%s", r.Kind, evt.ObjectNew.GetNamespace(), evt.ObjectNew.GetName())
	q.Add(reconcile.Request{NamespacedName: k8stypes.NamespacedName{Name: key}})
}

type predicateForPodNodeEvent struct {
	predicate.Funcs
}

func (p *predicateForPodNodeEvent) Update(e event.UpdateEvent) bool {
	return false
}

func (p *predicateForPodNodeEvent) Generic(e event.GenericEvent) bool {
	return false
}

type PodNodeReconciler struct {
	client        client.Client
	scheme        *runtime.Scheme
	log           *logrus.Logger
	record        record.EventRecorder
	msxfClient    msxf.MsxfClientAPI
	MSconfig      *MSConfig
	allocatedEnis map[string]*AllocatedEniInfo
}

type AllocatedEniInfo struct {
	EniInfo    *msxf.ENI
	IsAttached bool
}

func NewReconcilePodNode(mgr manager.Manager, msxfClient msxf.MsxfClientAPI, log *logrus.Logger, msconf *MSConfig) *PodNodeReconciler {
	r := &PodNodeReconciler{
		client:     mgr.GetClient(),
		scheme:     mgr.GetScheme(),
		record:     mgr.GetEventRecorderFor("LiraPodNodeController"),
		msxfClient: msxfClient,
		log:        log,
		MSconfig:   msconf,
	}
	return r
}

func (r *PodNodeReconciler) processExclusiveENIPodEni(ctx context.Context) error {
	nodeEniMap, err := r.getAllocatedENINum(ctx)
	if err != nil {
		r.log.Errorf("get node eni map failed. %s", err)
		return err
	}
	r.log.Infof("nodeEniMap %+v", nodeEniMap)
	err = r.updateNodeENIResource(ctx, &nodeEniMap)
	if err != nil {
		r.log.Errorf("update node resource failed. err %s", err)
		return err
	}

	return nil
}

func (r *PodNodeReconciler) processMultiENIIPPodEni(ctx context.Context, name, namespace string) error {
	r.log.Infof("processMultiENIIPPodEni %s", name)

	eniAllocatedIPs := make(map[string]int)

	podENI := &v1beta1.PodENI{}
	err := r.client.Get(ctx, k8stypes.NamespacedName{Name: name, Namespace: namespace}, podENI)
	if err != nil {
		if k8sErr.IsNotFound(err) {
			return nil
		}
		return err
	}
	eniId := podENI.Spec.ENI.ID
	phase := podENI.Status.Phase

	nodeName := podENI.Status.NodeName
	podNode := &v1beta1.PodNode{}
	err = r.client.Get(ctx, k8stypes.NamespacedName{Name: nodeName}, podNode)
	if err != nil {
		if k8sErr.IsNotFound(err) {
			return nil
		}
		return err
	}
	podNodeCopy := podNode.DeepCopy()

	podENIList := &v1beta1.PodENIList{}
	err = r.client.List(ctx, podENIList)
	if err != nil {
		r.log.Errorf("list podEni failed %s.", err)
		return err
	}

	for _, podEni := range podENIList.Items {
		if podEni.Spec.ENI.ID == eniId && phase == v1beta1.ENIPhaseIPAllocated {
			eniAllocatedIPs[eniId]++
		}
	}
	for _, instance := range podNodeCopy.Status.NetworkInterfaces {
		instance.Allocated = eniAllocatedIPs[instance.ID]
	}

	err = r.client.Status().Update(ctx, podNodeCopy)
	if err != nil {
		r.log.Errorf("update podnode NetworkInterfaces failed. %s", err)
		return err
	}
	return nil
}

func (r *PodNodeReconciler) processENIResource(ctx context.Context, name, namespace string) (ctrl.Result, error) {
	var err error
	mode := utils.GConfig.RunMode

	switch mode {
	case utils.ExclusiveENI:
		err = r.processExclusiveENIPodEni(ctx)
	case utils.MultiENIIP:
		err = r.processMultiENIIPPodEni(ctx, name, namespace)
	default:
	}

	return reconcile.Result{}, err
}

func (r *PodNodeReconciler) updateNodeENIResourceExcusive(ctx context.Context, node, nodeCopy *corev1.Node, eniNums string) (ctrl.Result, error) {
	r.log.Infof("updateNodeENIResourceExcusive start")

	_, e := nodeCopy.Status.Capacity[corev1.ResourceName(types.ENIResourceKey)]
	if e {
		return reconcile.Result{}, nil
	}

	// master 和 work节点区分，只变更work节点
	nodeCopy.Status.Allocatable[corev1.ResourceName(types.ENIResourceKey)] = resource.MustParse(eniNums)
	nodeCopy.Status.Capacity[corev1.ResourceName(types.ENIResourceKey)] = resource.MustParse(eniNums)

	err := r.patchNodeCr(ctx, nodeCopy, node)
	if err != nil {
		r.log.Error(err, "update node cr failed.")
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (r *PodNodeReconciler) recordPodENICreateErr(node *corev1.Node, startTime time.Time, err error) {
	if err == nil {
		return
	}
	r.record.Eventf(node, corev1.EventTypeWarning,
		"CniPodNodeCreateENIError", fmt.Sprintf("CniCreateNodeENIError: %s, elapsedTime: %s", err, time.Since(startTime)))
}

func (r *PodNodeReconciler) recordPodENIAttachErr(node *corev1.Node, startTime time.Time, err error) {
	if err == nil {
		return
	}

	r.record.Eventf(node, corev1.EventTypeWarning,
		"CniPodNodeENIAttachErr", fmt.Sprintf("CniPodNodeENIAttachErr: %s, elapsedTime: %s", err, time.Since(startTime)))
}

func (r *PodNodeReconciler) recordPodENIDettachErr(podNode *v1beta1.PodNode, startTime time.Time, err error) {
	if err == nil {
		return
	}
	r.record.Eventf(podNode, corev1.EventTypeWarning,
		"CniPodNodeENIDettachErr", fmt.Sprintf("CniPodNodeENIDettachErr: %s, elapsedTime: %s", err, time.Since(startTime)))
}

func (r *PodNodeReconciler) recordPodENIDeleteErr(podNode *v1beta1.PodNode, startTime time.Time, err error) {
	if err == nil {
		return
	}
	r.record.Eventf(podNode, corev1.EventTypeWarning,
		"CniPodNodeENIDeleteErr", fmt.Sprintf("CniPodNodeENIDeleteErr: %s, elapsedTime: %s", err, time.Since(startTime)))
}

func (r *PodNodeReconciler) createENIForNode(ctx context.Context, node *corev1.Node) (*msxf.ENI, error) {

	start := time.Now()

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

	ecsID := node.Labels[types.AnnotationsENIECSID]
	if ecsID == "" {
		r.log.Infof("node %s ecsID is null", node.Name)
		return nil, errors.New("ecsID is null")
	}

	name := utils.GeneratePodNodeENIName(ecsID)

	isNumberic, azCode := utils.IsValidNumber(r.MSconfig.AzoneCode)
	if !isNumberic {
		r.log.Errorf("%s is not a vaild number", r.MSconfig.AzoneCode)
		return nil, fmt.Errorf("azcode %s is not a vaild number", r.MSconfig.AzoneCode)
	}
	uniqueID := utils.GenerateENIGrUniqueID(ecsID)
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
		GrUniqueID:      uniqueID,
		Type:            2,
	})
	if err != nil {
		r.recordPodENICreateErr(node, start, err)
		r.log.WithError(err).Error("create eni failed.")
		return nil, err
	}
	r.log.Infof("createENIForNode: eni id %s\n", eni.ID)
	return eni, nil
}

func (r *PodNodeReconciler) attachENIForNode(ctx context.Context, node *corev1.Node, eniID string) error {

	start := time.Now()
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
	ecsId, ok := node.Labels[types.AnnotationsENIECSID]
	if !ok {
		r.log.Error(fmt.Sprintf("node %s ecsId label is null", node.Name))
		return fmt.Errorf("node %s ecsId label is null", node.Name)
	}
	isNumberic, azCode := utils.IsValidNumber(r.MSconfig.AzoneCode)
	if !isNumberic {
		r.log.Errorf("%s is not a vaild number", r.MSconfig.AzoneCode)
		return fmt.Errorf("azcode %s is not a vaild number", r.MSconfig.AzoneCode)
	}
	err := r.msxfClient.AttachENI(nctx, &msxf.AttachNetworkInterfaceRequest{
		ID:             eniID,
		AZoneCode:      azCode,
		TenantID:       r.MSconfig.TenantID,
		ServerID:       ecsId,
		RequestId:      reuqestId,
		CloudAccountID: r.MSconfig.CloudAccountID,
	})
	if err != nil {
		r.log.WithError(err).Error("attch eni failed.")
		r.recordPodENIAttachErr(node, start, err)
		return err
	}
	return nil
}
func (r *PodNodeReconciler) generateENIForPodNode(ctx context.Context, node *corev1.Node, podNode *v1beta1.PodNode) error {
	pnodePhase := podNode.Status.Phase
	switch pnodePhase {
	case v1beta1.PodNodePhaseInitial:
		nodeCopy := node.DeepCopy()
		eniNum := podNode.Spec.EniNums

		if r.allocatedEnis == nil {
			r.allocatedEnis = make(map[string]*AllocatedEniInfo, eniNum)
		}

		if len(r.allocatedEnis) == eniNum {
			r.log.Info("eni all have been created.")
			return nil
		}

		podNodeCopy := podNode.DeepCopy()
		// finish attach
		for _, allocatedEni := range podNodeCopy.Status.NetworkInterfaces {
			if !allocatedEni.IsBind {
				err := r.attachENIForNode(ctx, nodeCopy, allocatedEni.ID)
				if err != nil {
					if strings.Contains(err.Error(), "-14108000") {
						goto BindStep
					}
					r.log.WithError(err).Error("attach eni for node failed.")
					return err
				}
			BindStep:
				allocatedEni.IsBind = true
			}
		}
		needAllocate := eniNum - len(podNodeCopy.Status.NetworkInterfaces)
		for i := 0; i < needAllocate; i++ {
			eni, err := r.createENIForNode(ctx, nodeCopy)
			if err != nil {
				r.log.WithError(err).Error("create eni for node failed.")
				return err
			}

			err = r.attachENIForNode(ctx, nodeCopy, eni.ID)
			if err != nil {
				if strings.Contains(err.Error(), "-14108000") {
					goto NextBind
				}
				r.log.WithError(err).Error("attach eni for node failed.")
				return err
			}

		NextBind:
			podNodeCopy.Status.NetworkInterfaces = append(podNodeCopy.Status.NetworkInterfaces, &v1beta1.NetworkInterface{
				ID:                   eni.ID,
				Name:                 eni.Name,
				Status:               eni.Status,
				MacAddress:           eni.MacAddress,
				SecurityGroups:       eni.SecurityGroups,
				IPAddress:            eni.IPAddress,
				NetworkInterfaceType: v1beta1.ENITypeSecondary,
				Subnet: &v1beta1.SubnetBrief{
					ID:        eni.Subnet.ID,
					CIDR:      eni.Subnet.CIDR,
					OSID:      eni.OSID,
					GatewayIP: eni.Subnet.GatewayIP,
					Name:      eni.Subnet.Name,
				},
				IsBind: true,
			})
			r.log.Infof("eniid %s, ip %s", eni.ID, eni.IPAddress)
			r.log.Infof("NetworkInterfaces: %+v\n", podNodeCopy.Status.NetworkInterfaces)
		}
		podNodeCopy.Status.Phase = v1beta1.PodNodePhaseFinished

		err := r.client.Status().Update(ctx, podNodeCopy)
		if err != nil {
			r.log.WithError(err).Error("patch podeni cr status failed.")
			return err
		}
		// 默认全部分配，运行中不再做创建、删除、attach和detach操作
		eniAllocatableNums := eniNum - len(r.allocatedEnis)
		eniNumStr := strconv.FormatInt(int64(eniAllocatableNums), 10)
		eniNums := strconv.FormatInt(int64(eniNum), 10)
		nodeCopy.Status.Allocatable[corev1.ResourceName(types.ENIResourceKey)] = resource.MustParse(eniNumStr)
		nodeCopy.Status.Capacity[corev1.ResourceName(types.ENIResourceKey)] = resource.MustParse(eniNums)

		err = r.patchNodeCr(ctx, nodeCopy, node)
		if err != nil {
			r.log.Error(err, "update node cr failed.")
			return err
		}
		return nil
	case v1beta1.PodNodePhaseFinished:
		r.log.Infof("%s already finished", podNode.Name)
		return nil
	case v1beta1.PodNodePhaseDeleting:
		r.log.Infof("delete podnode %s", podNode.Name)
		err := r.client.Delete(ctx, podNode)
		if err != nil {
			if k8sErr.IsNotFound(err) {
				r.log.Info("cr resource not found")
				return nil
			}
			return err
		}
		return nil
	default:
		r.log.Errorf("podnode phase is invalid %s", pnodePhase)
		return errors.New("podnode phase is invalid")
	}
}

func (r *PodNodeReconciler) generateENIForNode(ctx context.Context, node *corev1.Node, eniNum int) error {
	podNodeE := &v1beta1.PodNode{}
	err := r.client.Get(ctx, k8stypes.NamespacedName{Name: node.Name}, podNodeE)
	if err == nil {
		r.log.WithError(err).Infof("podnode %s is exist", node.Name)
		return nil
	}
	podNode := &v1beta1.PodNode{
		ObjectMeta: metav1.ObjectMeta{
			Name: node.Name,
			Finalizers: []string{
				types.FinalizerPodNode,
			},
		},
		Spec: v1beta1.PodNodeSpec{
			NodeName: node.Name,
			EniNums:  eniNum,
		},
	}
	// create podNode cr
	err = r.client.Create(ctx, podNode)
	if err != nil {
		r.log.WithError(err).Error("create podnode cr failed.")
		return err
	}

	podNodeCopy := podNode.DeepCopy()
	podNodeCopy.Status.Phase = v1beta1.PodNodePhaseInitial

	err = r.client.Status().Update(ctx, podNodeCopy)
	if err != nil {
		r.log.WithError(err).Error("patch podeni cr status failed.")
		return err
	}
	return nil
}

func (r *PodNodeReconciler) updateNodeENIResourceMultiENIIP(ctx context.Context, node *corev1.Node, eniNums string) (ctrl.Result, error) {
	r.log.Infof("updateNodeENIResourceMultiENIIP start")

	eniN, err := strconv.Atoi(eniNums)
	if err != nil {
		r.log.Error(err)
		return reconcile.Result{}, err
	}

	if eniN == 0 {
		r.log.Error("eninum is less than 1")
		return reconcile.Result{}, errors.New("eninum is less than 1")
	}

	//multi-eni-ip 模式下，每个节点默认创建eniNums个eni设备
	err = r.generateENIForNode(ctx, node, eniN)
	if err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (r *PodNodeReconciler) generateNodeENIResource(ctx context.Context, node, nodeCopy *corev1.Node) (ctrl.Result, error) {
	eniNums := "0"

	if utils.IsMasterNode(nodeCopy) {
		eniNums = r.MSconfig.MasterENINum
	} else {
		//如果 types.ENI_COUNTS 存在于 nodeCopy.Labels 中，则 eniCountsErr 为 nil 或 false，表示操作成功
		eniCounts, eniCountsErr := nodeCopy.Labels[types.ENI_COUNTS]
		r.log.Info("printeniCounts ", "printeniCounts", eniCounts)
		r.log.Info("printeniCounts ", "eniCountsErr", eniCountsErr)
		if !eniCountsErr {
			msg := "node label eni_counts is missing or empty"
			labelMissingErr := errors.New(msg)
			r.log.Error(labelMissingErr, "label is not exist")
			return reconcile.Result{}, labelMissingErr
		}
		eniNums = eniCounts
	}
	mode := utils.GConfig.RunMode
	switch mode {
	case utils.ExclusiveENI:
		return r.updateNodeENIResourceExcusive(ctx, node, nodeCopy, eniNums)
	case utils.MultiENIIP:
		return r.updateNodeENIResourceMultiENIIP(ctx, node, eniNums)
	default:
		r.log.Errorf("lira mode %s is invalid.", mode)
		return reconcile.Result{}, errors.New("lira mode is invalid")
	}
}

func (r *PodNodeReconciler) releasePodNodeENI(ctx context.Context, podNode *v1beta1.PodNode) (ctrl.Result, error) {
	start := time.Now()
	for _, eni := range podNode.Status.NetworkInterfaces {
		if eni.ID != "" {
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
				ID:             eni.ID,
				TenantID:       r.MSconfig.TenantID,
				RequestId:      requestId,
				CloudAccountID: r.MSconfig.CloudAccountID,
			})

			if err != nil {
				if !(strings.Contains(err.Error(), "请求资源不存在") || strings.Contains(err.Error(), "此网卡未绑定设备")) {
					r.log.WithError(err).Error("podENI dettach eni failed.")
					r.recordPodENIDettachErr(podNode, start, err)
				}
			}

			err = r.msxfClient.DeleteENI(nctx, &msxf.DeleteNetworkInterfaceRequest{
				ID:             eni.ID,
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
			return reconcile.Result{}, nil
		}
	}

	controllerutil.RemoveFinalizer(podNode, types.FinalizerPodNode)
	err := r.client.Update(ctx, podNode)
	if k8sErr.IsNotFound(err) {
		return reconcile.Result{}, nil
	}
	return reconcile.Result{}, nil
}

func (r *PodNodeReconciler) processNode(ctx context.Context, name string) (ctrl.Result, error) {
	r.log.Infof("processNode %s", name)
	start := time.Now()
	mode := utils.GConfig.RunMode
	node := &corev1.Node{}
	err := r.client.Get(ctx, k8stypes.NamespacedName{Name: name}, node)
	if err != nil {
		if k8sErr.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if !node.DeletionTimestamp.IsZero() {
		// 多eniip场景才处理podnode
		if mode != utils.MultiENIIP {
			return reconcile.Result{}, nil
		}

		podNode := &v1beta1.PodNode{}
		err := r.client.Get(ctx, k8stypes.NamespacedName{Name: name}, podNode)
		if err != nil {
			if k8sErr.IsNotFound(err) {
				return reconcile.Result{}, nil
			}
			return reconcile.Result{}, err
		}

		podNodeCopy := podNode.DeepCopy()
		podNodeCopy.Status.Phase = v1beta1.PodNodePhaseDeleting

		err = r.client.Status().Update(ctx, podNodeCopy)
		if err != nil {
			r.log.WithError(err).Error("patch podeni cr status failed.")
			return reconcile.Result{}, err
		}
		r.recordPodENIDeleteErr(podNode, start, err)
		return reconcile.Result{}, err
	}
	r.log.Info("get node ", node.Name)
	nodeCopy := node.DeepCopy()

	return r.generateNodeENIResource(ctx, node, nodeCopy)
}

func (r *PodNodeReconciler) processPodNode(ctx context.Context, name string) (ctrl.Result, error) {
	r.log.Infof("processPodNode %s", name)

	start := time.Now()
	podNode := &v1beta1.PodNode{}
	err := r.client.Get(ctx, k8stypes.NamespacedName{Name: name}, podNode)
	if err != nil {
		if k8sErr.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	podNodeCopy := podNode.DeepCopy()

	if !podNode.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(podNode, types.FinalizerPodNode) {
			return reconcile.Result{}, nil
		}
		result, err := r.releasePodNodeENI(ctx, podNodeCopy)
		r.recordPodENIDeleteErr(podNodeCopy, start, err)
		return result, err
	}

	node := &corev1.Node{}
	err = r.client.Get(ctx, k8stypes.NamespacedName{Name: name}, node)
	if err != nil {
		if k8sErr.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	err = r.generateENIForPodNode(ctx, node, podNodeCopy)
	if err != nil {
		r.log.WithError(err).Errorln("generateENIForPodNode failed.")
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

// +kubebuilder:rbac:groups=network.msxf.com,resources=podnode,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=network.msxf.com,resources=podnode/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=network.msxf.com,resources=podnode/finalizers,verbs=update
func (r *PodNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.log.Infof("PodNode %s reconcile...", req.NamespacedName)
	r.log.Infof("lira mode is %s", utils.GConfig.RunMode)

	parts := strings.Split(req.Name, "/")
	kind := parts[0]
	namespace := parts[1]
	name := parts[2]
	switch kind {
	case "Node":
		return r.processNode(ctx, name)
	case "PodENI":
		return r.processENIResource(ctx, name, namespace)
	case "PodNode":
		return r.processPodNode(ctx, name)
	default:
		r.log.Errorf("invaild kind %s", kind)
		return reconcile.Result{}, errors.New("invaild kind")
	}
}

func (r *PodNodeReconciler) patchNodeCr(ctx context.Context, nodeCopy *corev1.Node, node *corev1.Node) error {
	var err error
	retry := 5
	patchObj := client.MergeFrom(node)
	for i := 0; i < retry; i++ {
		r.log.Info("patch node", "node name", node.Name)
		err = r.client.Status().Patch(ctx, nodeCopy, patchObj)
		if err != nil {
			r.log.Error(err, "patch node failed", "node name", node.Name)
			if i < retry {
				continue
			}
			return err
		}
		return nil
	}
	r.log.Error(err, "max retries reached, update node failed")
	return err
}

func (r *PodNodeReconciler) patchPodNodeCr(ctx context.Context, podNodeCopy *v1beta1.PodNode, podNode *v1beta1.PodNode) error {
	var err error
	retry := 5
	patchObj := client.MergeFrom(podNode)
	for i := 0; i < retry; i++ {
		r.log.Info("patch podnode ", "node name", podNodeCopy.Name)
		err = r.client.Status().Patch(ctx, podNodeCopy, patchObj)
		if err != nil {
			r.log.Error(err, "patch podnode failed ", "node name", podNode.Name)
			if i < retry {
				continue
			}
			return err
		}
		return nil
	}
	r.log.Error(err, "max retries reached, update podnode failed")
	return err
}

func (r *PodNodeReconciler) getNode(ctx context.Context, name string) (*corev1.Node, error) {
	node := &corev1.Node{}
	err := r.client.Get(ctx, k8stypes.NamespacedName{
		Name: name,
	}, node)
	return node, err
}

func (r *PodNodeReconciler) updateNodeENIResource(ctx context.Context, nodeEniMap *map[string]int) error {
	for nodeName, allocEniNum := range *nodeEniMap {
		node, err := r.getNode(ctx, nodeName)
		if err != nil {
			r.log.Errorf("get node cr %s failed. err %s", nodeName, err)
		}
		nodeCopy := node.DeepCopy()

		total := nodeCopy.Status.Capacity[corev1.ResourceName(types.ENIResourceKey)]
		totalNum := int(total.Value())
		if totalNum <= 0 {
			r.log.Errorf("node %s Capacity totalNum is invalid, totalNum %d", nodeName, totalNum)
			continue
		}
		allocableEni := totalNum - allocEniNum
		allocableEniRes := resource.NewQuantity(int64(allocableEni), resource.DecimalSI)
		nodeCopy.Status.Allocatable[corev1.ResourceName(types.ENIResourceKey)] = *allocableEniRes
		err = r.patchNodeCr(ctx, nodeCopy, node)
		if err != nil {
			r.log.Errorf("patch node %s resource failed. err %s", nodeName, err)
		}
	}
	return nil
}

func (r *PodNodeReconciler) getAllocatedENINum(ctx context.Context) (map[string]int, error) {
	r.log.Info("start getAllocatedENINum")
	nodeEniNum := make(map[string]int)
	var eniCnt int
	podEniList := &v1beta1.PodENIList{}
	err := r.client.List(ctx, podEniList)
	if err != nil {
		r.log.Errorf("list podeni failed. %s", err)
		return nil, err
	}
	nodeList := &corev1.NodeList{}
	err = r.client.List(ctx, nodeList)
	if err != nil {
		r.log.Errorf("list node failed. %s", err)
		return nil, err
	}
	for _, node := range nodeList.Items {
		eniCnt = 0
		for _, podEni := range podEniList.Items {
			if podEni.Spec.ENI.ID != "" && podEni.Status.NodeName == node.Name {
				eniCnt++
			}
			nodeEniNum[node.Name] = eniCnt
		}
	}
	return nodeEniNum, err
}
