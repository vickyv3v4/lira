package controllers

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"gitlab.msxf.com/mscloud/lira/pkg/api/v1beta1"
	"gitlab.msxf.com/mscloud/lira/pkg/llog"
	"gitlab.msxf.com/mscloud/lira/pkg/types"
)

const resourceDeviceManager = "resDevManager"

func init() {
	AddRes(resourceDeviceManager, func(mgr manager.Manager, ctrlCtx *ControllerCtx) error {
		llog.Logger.Info("add resource resDevManager")
		rDevManger := &ResourceDeviceManager{
			client: mgr.GetClient(),
			log:    llog.Logger,
		}
		err := mgr.Add(rDevManger)
		if err != nil {
			return err
		}
		return nil
	}, true)
}

type ResourceDeviceManager struct {
	client client.Client
	log    *logrus.Logger
}

func (r *ResourceDeviceManager) Start(ctx context.Context) error {
	r.log.Infoln("ResourceDeviceManager starting...")
	for {
		select {
		case <-ctx.Done():
			r.log.Infoln("ResourceDeviceManager is existing...")
			return nil
		default:
			nodeEniMap, err := r.getAllocatedENINum(ctx)
			if err != nil {
				r.log.Errorf("get node eni map failed. %s", err)
			}
			r.log.Infof("nodeEniMap %+v", nodeEniMap)
			err = r.updateNodeENIResource(ctx, &nodeEniMap)
			if err != nil {
				r.log.Errorf("update node resource failed. err %s", err)
			}
			time.Sleep(1000 * time.Millisecond)
		}
	}
}
func (r *ResourceDeviceManager) getNode(ctx context.Context, name string) (*corev1.Node, error) {
	node := &corev1.Node{}
	err := r.client.Get(ctx, k8stypes.NamespacedName{
		Name: name,
	}, node)
	return node, err
}
func (r *ResourceDeviceManager) updateNodeENIResource(ctx context.Context, nodeEniMap *map[string]int) error {
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
func (r *ResourceDeviceManager) patchNodeCr(ctx context.Context, nodeCopy *corev1.Node, node *corev1.Node) error {
	var err error
	retry := 5
	patchObj := client.MergeFrom(node)
	for i := 0; i < retry; i++ {
		err = r.client.Status().Patch(ctx, nodeCopy, patchObj)
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

func (r *ResourceDeviceManager) getAllocatedENINum(ctx context.Context) (map[string]int, error) {
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
