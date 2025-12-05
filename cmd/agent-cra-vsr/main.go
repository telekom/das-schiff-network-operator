/*
Copyright 2025.

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
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	controllervsr "github.com/telekom/das-schiff-network-operator/controllers/agent-cra-vsr"
	reconcilervsr "github.com/telekom/das-schiff-network-operator/pkg/reconciler/agent-cra-vsr"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/cra-vsr"
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
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
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

func setupCraPrometheusRegistry(craManager *cra.Manager) (*prometheus.Registry, error) {
	registry := prometheus.NewRegistry()

	collector, err := monitoring.NewDasSchiffNetworkOperatorCollector(
		map[string]bool{
			"netlink": false,
			"frr":     false,
			"vsr":     true,
		})
	if err != nil {
		return nil, fmt.Errorf("failed to create collector %w", err)
	}

	if v, ok := collector.Collectors["vsr"].(*monitoring.VSRCollector); ok {
		v.CraManager = craManager
	}

	registry.MustRegister(collector)

	return registry, nil
}

func updateManagerOptions(options *manager.Options, craManager *cra.Manager) error {
	if options.Metrics.BindAddress != "0" && options.Metrics.BindAddress != "" {
		err := initCollectors()
		if err != nil {
			return fmt.Errorf("unable to initialize metrics collectors: %w", err)
		}

		registry, err := setupCraPrometheusRegistry(craManager)
		if err != nil {
			return fmt.Errorf("failed to setup cra prometheus registry: %w", err)
		}

		options.Metrics.ExtraHandlers = map[string]http.Handler{
			"/cra/metrics": promhttp.HandlerFor(
				registry,
				promhttp.HandlerOpts{
					// Opt into OpenMetrics to support exemplars.
					EnableOpenMetrics: true,
					Timeout:           time.Minute,
				},
			),
		}
	}

	return nil
}

func createCraManager() (*cra.Manager, error) {
	urls := strings.Split(os.Getenv("CRA_URL"), ",")
	if len(urls) == 0 {
		return nil, fmt.Errorf("no CRA URL provided")
	}

	metricsUrls := strings.Split(os.Getenv("CRA_METRICS_URL"), ",")
	if len(urls) == 0 {
		return nil, fmt.Errorf("no CRA URL provided")
	}

	timeout, err := time.ParseDuration(os.Getenv("CRA_TIMEOUT"))
	if err != nil {
		return nil, fmt.Errorf("error parsing VSR timeout: %w", err)
	}

	user := os.Getenv("CRA_USER")
	if user == "" {
		return nil, fmt.Errorf("no CRA user provided")
	}

	pwd := os.Getenv("CRA_PASSWORD")
	if pwd == "" {
		return nil, fmt.Errorf("no CRA password provided")
	}

	craManager, err := cra.NewManager(urls, metricsUrls, user, pwd, timeout)
	if err != nil {
		return nil, fmt.Errorf("error creating CRA manager: %w", err)
	}

	return craManager, nil
}

func setupReconcilers(
	mgr manager.Manager,
	nodeConfigPath string,
	craManager *cra.Manager,
) (*reconcilervsr.NodeNetworkConfigReconciler, error) {
	r, err := reconcilervsr.NewNodeNetworkConfigReconciler(
		craManager, mgr.GetClient(), mgr.GetLogger(), nodeConfigPath)
	if err != nil {
		return nil, fmt.Errorf("unable to create debounced reconciler: %w", err)
	}

	if err = (&controllervsr.NodeNetworkConfigReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Reconciler: r,
	}).SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("unable to create NodeConfig controller: %w", err)
	}

	return r, nil
}

func initComponents(mgr manager.Manager, nodeConfigPath string, craManager *cra.Manager) error {
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	r, err := setupReconcilers(mgr, nodeConfigPath, craManager)
	if err != nil {
		return fmt.Errorf("unable to setup reconcilers: %w", err)
	}

	// Trigger initial reconciliation.
	if r != nil {
		_ = r.Reconcile(context.Background())
	}

	return nil
}

func main() {
	var nodeNetworkConfigPath string
	var metricsAddr string
	var healthAddr string
	var opts zap.Options

	version.Get().Print(os.Args[0])

	opts.Development = true
	opts.BindFlags(flag.CommandLine)

	flag.StringVar(&healthAddr, "health-addr", ":7081", "bind address of health/readiness probes")
	flag.StringVar(&metricsAddr, "metrics-addr", ":7080", "bind address of metrics endpoint")
	flag.StringVar(&nodeNetworkConfigPath, "nodenetworkconfig-path",
		reconcilervsr.DefaultNodeNetworkConfigPath,
		"Path to store working node configuration.")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	craManager, err := createCraManager()
	if err != nil {
		setupLog.Error(err, "unable to create VSR manager")
		os.Exit(1)
	}

	options := ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: healthAddr,
	}
	err = updateManagerOptions(&options, craManager)
	if err != nil {
		setupLog.Error(err, "unable to update manager options")
		os.Exit(1)
	}

	clientConfig := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(clientConfig, options)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := initComponents(mgr, nodeNetworkConfigPath, craManager); err != nil {
		setupLog.Error(err, "unable to initialize components")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
