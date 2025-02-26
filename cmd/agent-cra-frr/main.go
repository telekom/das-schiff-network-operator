/*
Copyright 2022.

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

//nolint:gci
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	controllerfrr "github.com/telekom/das-schiff-network-operator/controllers/agent-cra-frr"
	reconcilerfrr "github.com/telekom/das-schiff-network-operator/pkg/reconciler/agent-cra-frr"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/managerconfig"
	"github.com/telekom/das-schiff-network-operator/pkg/monitoring"
	"github.com/telekom/das-schiff-network-operator/pkg/version"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.) //nolint:gci
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	//nolint:gci // kubebuilder import
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(networkv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func initCollectors() error {
	var err error
	collector, err := monitoring.NewDasSchiffNetworkOperatorCollector(map[string]bool{})
	if err != nil {
		return fmt.Errorf("failed to create collector: %w", err)
	}
	setupLog.Info("initialize collectors")
	collectors := []string{}
	for c := range collector.Collectors {
		collectors = append(collectors, c)
	}
	sort.Strings(collectors)
	for index := range collectors {
		setupLog.Info("registered collector", "collector", collectors[index])
	}
	if err := metrics.Registry.Register(collector); err != nil {
		return fmt.Errorf("failed to register collector: %w", err)
	}
	return nil
}

func main() {
	version.Get().Print(os.Args[0])

	var configFile string
	var nodeNetworkConfigPath string
	flag.StringVar(&configFile, "config", "",
		"The controller will load its initial configuration from this file. "+
			"Omit this flag to use the default configuration values. "+
			"Command-line flags override configuration from this file.")
	flag.StringVar(&nodeNetworkConfigPath, "nodenetworkconfig-path", reconcilerfrr.DefaultNodeNetworkConfigPath,
		"Path to store working node configuration.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	options, err := setManagerOptions(configFile)
	if err != nil {
		setupLog.Error(err, "unable to configure manager's options")
		os.Exit(1)
	}

	clientConfig := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(clientConfig, *options)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := initComponents(mgr, nodeNetworkConfigPath); err != nil {
		setupLog.Error(err, "unable to initialize components")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func setManagerOptions(configFile string) (*manager.Options, error) {
	var err error
	var options manager.Options
	if configFile != "" {
		options, err = managerconfig.Load(configFile, scheme)
		if err != nil {
			return nil, fmt.Errorf("unable to load the config file: %w", err)
		}
	} else {
		options = ctrl.Options{Scheme: scheme}
	}

	if options.Metrics.BindAddress != "0" && options.Metrics.BindAddress != "" {
		err = initCollectors()
		if err != nil {
			return nil, fmt.Errorf("unable to initialize metrics collectors: %w", err)
		}
	}

	return &options, nil
}

func initComponents(mgr manager.Manager, nodeConfigPath string) error {
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	r, err := setupReconcilers(mgr, nodeConfigPath)
	if err != nil {
		return fmt.Errorf("unable to setup reconcilers: %w", err)
	}

	// Trigger initial reconciliation.
	if r != nil {
		_ = r.Reconcile(context.Background())
	}

	return nil
}

func setupReconcilers(mgr manager.Manager, nodeConfigPath string) (*reconcilerfrr.NodeNetworkConfigReconciler, error) {
	r, err := reconcilerfrr.NewNodeNetworkConfigReconciler(mgr.GetClient(), mgr.GetLogger(), nodeConfigPath)
	if err != nil {
		return nil, fmt.Errorf("unable to create debounced reconciler: %w", err)
	}

	if err = (&controllerfrr.NodeNetworkConfigReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Reconciler: r,
	}).SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("unable to create NodeConfig controller: %w", err)
	}

	return r, nil
}
