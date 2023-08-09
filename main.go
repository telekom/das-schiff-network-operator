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
	"flag"
	"fmt"
	"os"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/controllers"
	"github.com/telekom/das-schiff-network-operator/pkg/anycast"
	"github.com/telekom/das-schiff-network-operator/pkg/bpf"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	"github.com/telekom/das-schiff-network-operator/pkg/macvlan"
	"github.com/telekom/das-schiff-network-operator/pkg/notrack"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.) //nolint:gci
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
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
	options := ctrl.Options{Scheme: scheme}
	if configFile != "" {
		options, err = options.AndFrom(ctrl.ConfigFile().AtPath(configFile))
		if err != nil {
			setupLog.Error(err, "unable to load the config file")
			os.Exit(1)
		}
	}
	clientConfig := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(clientConfig, options)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		setupLog.Error(err, "unable to load config")
		os.Exit(1)
	}

	anycastTracker := &anycast.Tracker{}

	if err = (&networkv1alpha1.VRFRouteConfiguration{}).SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "VRFRouteConfiguration")
		os.Exit(1)
	}

	if err := initComponents(mgr, anycastTracker, cfg, onlyBPFMode); err != nil {
		setupLog.Error(err, "unable to initialize components")
		os.Exit(1)
	}

	if len(interfacePrefix) > 0 {
		setupLog.Info("start macvlan sync")
		macvlan.RunMACSync(interfacePrefix)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func initComponents(mgr manager.Manager, anycastTracker *anycast.Tracker, cfg *config.Config, onlyBPFMode bool) error {
	// Start VRFRouteConfigurationReconciler when we are not running in only BPF mode.
	if !onlyBPFMode {
		if err := setupReconcilers(mgr, anycastTracker); err != nil {
			return fmt.Errorf("unable to setup reconcilers: %w", err)
		}
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	setupLog.Info("load bpf program into Kernel")
	if err := bpf.InitBPFRouter(); err != nil {
		return fmt.Errorf("unable to init BPF router: %w", err)
	}
	setupLog.Info("attach bpf to interfaces specified in config")
	if err := bpf.AttachInterfaces(cfg.BPFInterfaces); err != nil {
		return fmt.Errorf("unable to attach bpf to interfaces: %w", err)
	}

	setupLog.Info("start bpf interface check")
	bpf.RunInterfaceCheck()

	setupLog.Info("start anycast sync")
	anycastTracker.RunAnycastSync()

	setupLog.Info("start notrack sync")
	if err := notrack.RunIPTablesSync(); err != nil {
		setupLog.Error(err, "error starting IPTables sync")
	}

	nc, err := healthcheck.LoadConfig(healthcheck.NetHealthcheckFile)
	if err != nil {
		setupLog.Error(err, "problem loading network healthcheck config")
		os.Exit(1)
	}

	tcpDialer := healthcheck.NewTCPDialer(nc.Timeout)
	hc, err := healthcheck.NewHealthChecker(clusterClient,
		healthcheck.NewDefaultHealthcheckToolkit(nil, tcpDialer),
		nc)
	if err != nil {
		setupLog.Error(err, "problem initializing healthchecker")
		os.Exit(1)
	}
	if err = hc.CheckInterfaces(); err != nil {
		setupLog.Error(err, "problem with network interfaces")
		os.Exit(1)
	}
	if err = hc.CheckReachability(); err != nil {
		setupLog.Error(err, "problem with network reachability")
		os.Exit(1)
	}
	if err = hc.RemoveTaint(healthcheck.TaintKey); err != nil {
		setupLog.Error(err, "problem removing taint")
		os.Exit(1)
	}

	return nil
}

func setupReconcilers(mgr manager.Manager, anycastTracker *anycast.Tracker) error {
	r, err := reconciler.NewReconciler(mgr.GetClient(), anycastTracker)
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

	return nil
}
