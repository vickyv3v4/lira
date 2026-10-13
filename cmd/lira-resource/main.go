package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8sErr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	networkv1beta1 "gitlab.msxf.com/mscloud/lira/pkg/api/v1beta1"
	"gitlab.msxf.com/mscloud/lira/pkg/controllers"
	"gitlab.msxf.com/mscloud/lira/pkg/types"
	"gitlab.msxf.com/mscloud/lira/pkg/utils"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(networkv1beta1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}
func main() {
	utils.NewGConfig()
	flag.StringVar(&utils.GConfig.ProbeAddr, "health-probe-bind-address", ":9091", "The address the probe endpoint binds to.")
	flag.StringVar(&utils.GConfig.MetricsAddr, "metrics-address", ":9191", "The address of metrics.")
	flag.BoolVar(&utils.GConfig.EnableLeaderElection, "leader-elect", true,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	probeAddr := utils.GConfig.ProbeAddr
	enableLeaderElection := utils.GConfig.EnableLeaderElection
	metricsAddr := utils.GConfig.MetricsAddr
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "913c3772.msxf.com",
		MetricsBindAddress:     metricsAddr,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	var msconfig *controllers.MSConfig
	if msconfig, err = getMsconfig(ctx); err != nil {
		setupLog.Error(err, "get mscloud config failed.")
		os.Exit(1)
	}
	ctrlCtx := &controllers.ControllerCtx{
		Context:  ctx,
		MsConfig: msconfig,
	}
	// watch node
	go func() {
		for {
			err = updateNodeENIResource(ctx, msconfig)
			if err != nil {
				setupLog.Error(err, "update node eni resource failed.")
				time.Sleep(5 * time.Second)
			}
			time.Sleep(1 * time.Second)
		}
	}()
	for name := range controllers.Resources {
		err = controllers.Resources[name].Creator(mgr, ctrlCtx)
		if err != nil {
			panic(err)
		}
		setupLog.Info("register Resources", "Resources", name)
	}
	setupLog.Info("starting resource")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running resource")
		os.Exit(1)
	}
}

func getMsconfig(ctx context.Context) (*controllers.MSConfig, error) {
	restConfig := ctrl.GetConfigOrDie()

	c, err := client.New(restConfig, client.Options{
		Scheme: types.Scheme,
	})
	if err != nil {
		return nil, err
	}
	msConfig := &controllers.MSConfig{}
	msCm := &corev1.ConfigMap{}
	err = c.Get(ctx, client.ObjectKey{Namespace: controllers.SysNamespace, Name: controllers.MSCloudCM}, msCm)
	if err != nil {
		if k8sErr.IsNotFound(err) {
			setupLog.Error(err, "get msconfig configmap failed.")
		}
		return nil, err
	}
	msConfig, err = msConfig.GetConfig([]byte(msCm.Data["mscloud_conf"]))
	if err != nil {
		setupLog.Error(err, "parse mscloud_conf failed.")
		return nil, err
	}
	setupLog.Info("get ms config")
	return msConfig, nil
}

func updateNodeENIResource(ctx context.Context, msconfig *controllers.MSConfig) error {
	setupLog.Info("start updateNodeENIResource ")
	restConfig := ctrl.GetConfigOrDie()

	c, err := client.New(restConfig, client.Options{
		Scheme: types.Scheme,
	})
	if err != nil {
		return err
	}
	nodeList := &corev1.NodeList{}
	err = c.List(ctx, nodeList)
	if err != nil {
		setupLog.Error(err, "list node failed")
		return err
	}
	for _, node := range nodeList.Items {
		setupLog.Info("get node", "name", node.Name)
		nodeCopy := node.DeepCopy()

		eniNums := "0"

		if utils.IsMasterNode(nodeCopy) {
			eniNums = msconfig.MasterENINum
		} else {
			//如果 types.ENI_COUNTS 存在于 nodeCopy.Labels 中，则 eniCountsErr 为 nil 或 false，表示操作成功
			eniCounts, eniCountsErr := nodeCopy.Labels[types.ENI_COUNTS]
			setupLog.Info("printeniCounts ", "printeniCounts", eniCounts)
			setupLog.Info("printeniCounts ", "eniCountsErr", eniCountsErr)
			if !eniCountsErr {
				msg := "node label eni_counts is missing or empty"
				labelMissingErr := errors.New(msg)
				setupLog.Error(labelMissingErr, "label is not exist")
				return labelMissingErr
			}
			eniNums = eniCounts

		}
		_, e := nodeCopy.Status.Capacity[corev1.ResourceName(types.ENIResourceKey)]
		if !e {
			setupLog.Info("start patch node", "name", node.Name)
			// master 和 work节点区分，只变更work节点
			nodeCopy.Status.Allocatable[corev1.ResourceName(types.ENIResourceKey)] = resource.MustParse(eniNums)
			nodeCopy.Status.Capacity[corev1.ResourceName(types.ENIResourceKey)] = resource.MustParse(eniNums)

			err := patchNodeCr(ctx, nodeCopy, &node, c)
			if err != nil {
				setupLog.Error(err, "update node cr failed.")
				return err
			}
		}
	}
	return nil
}

func patchNodeCr(ctx context.Context, nodeCopy *corev1.Node, node *corev1.Node, c client.Client) error {
	var err error
	retry := 5
	patchObj := client.MergeFrom(node)
	for i := 0; i < retry; i++ {
		setupLog.Info("patch node", "node name", node.Name)
		err = c.Status().Patch(ctx, nodeCopy, patchObj)
		if err != nil {
			setupLog.Error(err, "patch node failed", "node name", node.Name)
			if i < retry {
				continue
			}
			return err
		}
		return nil
	}
	setupLog.Error(err, "max retries reached, update node failed")
	return err
}
