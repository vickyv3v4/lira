package main

import (
	"context"
	"flag"
	"os"

	corev1 "k8s.io/api/core/v1"
	k8sErr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	networkv1beta1 "gitlab.msxf.com/mscloud/lira/pkg/api/v1beta1"
	"gitlab.msxf.com/mscloud/lira/pkg/controllers"
	"gitlab.msxf.com/mscloud/lira/pkg/msxf"
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
	var runMode string
	flag.StringVar(&runMode, "run-mode", string(utils.MultiENIIP), "lira ip allocate mode")
	utils.GConfig.RunMode = utils.LiraIPMode(runMode)
	flag.StringVar(&utils.GConfig.OpenAPIUrl, "open-api-url", "", "The url for msxf openapi.")

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
		LeaderElectionID:       "913c3773x.msxf.com",
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
		Context:    ctx,
		MsConfig:   msconfig,
		MsxfClient: msxf.NewMsxfClient(utils.GConfig.OpenAPIUrl),
	}

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
