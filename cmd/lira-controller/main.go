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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

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
	//+kubebuilder:scaffold:imports
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
	flag.StringVar(&utils.GConfig.ProbeAddr, "health-probe-bind-address", ":9081", "The address the probe endpoint binds to.")
	flag.StringVar(&utils.GConfig.MetricsAddr, "metrics-address", ":9181", "The address of metrics.")
	flag.BoolVar(&utils.GConfig.EnableLeaderElection, "leader-elect", true,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&utils.GConfig.OpenAPIUrl, "open-api-url", "", "The url for msxf openapi.")
	flag.IntVar(&utils.GConfig.PodENICurrent, "podeni-current", 1, "The podeni controller's maximum number of concurrent Reconciles")
	var runMode string
	flag.StringVar(&runMode, "run-mode", string(utils.MultiENIIP), "lira ip allocate mode")
	utils.GConfig.RunMode = utils.LiraIPMode(runMode)
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if utils.GConfig.OpenAPIUrl == "" {
		setupLog.Error(fmt.Errorf("parameter error"), "API Url is null.")
		os.Exit(1)
	}
	probeAddr := utils.GConfig.ProbeAddr
	enableLeaderElection := utils.GConfig.EnableLeaderElection
	metricsAddr := utils.GConfig.MetricsAddr
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "913c9772.msxf.com",
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
		Context:       ctx,
		MsxfClient:    msxf.NewMsxfClient(utils.GConfig.OpenAPIUrl),
		MsConfig:      msconfig,
		PodENICurrent: utils.GConfig.PodENICurrent,
	}

	for name := range controllers.Controllers {
		err = controllers.Controllers[name].Creator(mgr, ctrlCtx)
		if err != nil {
			panic(err)
		}
		setupLog.Info("register controller", "controller", name)
	}
	/*
		if err = (&controllers.DummyReconcile{
			RegisterResource: ctrlCtx.RegisterResource,
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to setup controller")
			os.Exit(1)
		}
	*/
	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
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
