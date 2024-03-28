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

//nolint:gci
package main

import (
	"flag"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/controllers"
	"github.com/telekom/das-schiff-network-operator/pkg/macvlan"
	"github.com/telekom/das-schiff-network-operator/pkg/managerconfig"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.) //nolint:gci
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
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

func main() {
	var onlyBPFMode bool
	var configFile string
	var interfacePrefix string
	flag.StringVar(&configFile, "config", "",
		"The controller will load its initial configuration from this file. "+
			"Omit this flag to use the default configuration values. "+
			"Command-line flags override configuration from this file.")
	flag.BoolVar(&onlyBPFMode, "only-attach-bpf", false,
		"Only attach BPF to specified interfaces in config. This will not start any reconciliation. Perfect for masters.")
	flag.StringVar(&interfacePrefix, "macvlan-interface-prefix", "",
		"Interface prefix for bridge devices for MACVlan sync")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	var err error
	var options manager.Options
	if configFile != "" {
		options, err = managerconfig.Load(configFile, scheme)
		if err != nil {
			setupLog.Error(err, "unable to load the config file")
			os.Exit(1)
		}
	} else {
		options = ctrl.Options{Scheme: scheme}
	}

	options.LeaderElection = true
	options.LeaderElectionID = "configurator"

	// turn off metrics server
	options.MetricsBindAddress = "0"

	clientConfig := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(clientConfig, options)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := initComponents(mgr); err != nil {
		setupLog.Error(err, "unable to initialize components")
		os.Exit(1)
	}

	if interfacePrefix != "" {
		setupLog.Info("start macvlan sync")
		macvlan.RunMACSync(interfacePrefix)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func initComponents(mgr manager.Manager) error {
	// Start VRFRouteConfigurationReconciler when we are not running in only BPF mode.
	if err := setupReconcilers(mgr); err != nil {
		return fmt.Errorf("unable to setup reconcilers: %w", err)
	}
	//+kubebuilder:scaffold:builder

	return nil
}

func setupReconcilers(mgr manager.Manager) error {
	r, err := reconciler.NewConfigReconciler(mgr.GetClient(), mgr.GetLogger())
	if err != nil {
		return fmt.Errorf("unable to create debounced reconciler: %w", err)
	}

	if err = (&controllers.VRFRouteConfigurationReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Reconciler: r,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create VRFRouteConfiguration controller: %w", err)
	}

	if err = (&controllers.Layer2NetworkConfigurationReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Reconciler: r,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create Layer2NetworkConfiguration controller: %w", err)
	}

	if err = (&controllers.RoutingTableReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Reconciler: r,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create RoutingTable controller: %w", err)
	}

	return nil
}
