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
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/controllers"
	configmanager "github.com/telekom/das-schiff-network-operator/pkg/config_manager"
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
	var configFile string
	var timeout string
	var limit int64
	flag.StringVar(&configFile, "config", "",
		"The controller will load its initial configuration from this file. "+
			"Omit this flag to use the default configuration values. "+
			"Command-line flags override configuration from this file.")
	flag.StringVar(&timeout, "timeout", reconciler.DefaultTimeout,
		"Timeout for Kubernetes API connections (default: 60s).")
	flag.Int64Var(&limit, "update-limit", reconciler.DefaultNodeUpdateLimit,
		"Defines how many nodes can be configured at once (default: 1).")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	options, err := setMangerOptions(configFile)
	if err != nil {
		setupLog.Error(err, "error configuring manager options")
		os.Exit(1)
	}

	clientConfig := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(clientConfig, *options)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	_, _, err = setupReconcilers(mgr, timeout, limit)
	if err != nil {
		setupLog.Error(err, "unable to setup reconcilers")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func setupReconcilers(mgr manager.Manager, timeout string, limit int64) (*reconciler.ConfigReconciler, *reconciler.NodeReconciler, error) {
	timoutVal, err := time.ParseDuration(timeout)
	if err != nil {
		return nil, nil, fmt.Errorf("error parsing timeout value %s: %w", timeout, err)
	}

	cmInfo := make(chan bool)
	nodeDelInfo := make(chan []string)

	cr, err := reconciler.NewConfigReconciler(mgr.GetClient(), mgr.GetLogger().WithName("ConfigReconciler"), timoutVal, cmInfo)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create config reconciler reconciler: %w", err)
	}

	nr, err := reconciler.NewNodeReconciler(mgr.GetClient(), mgr.GetLogger().WithName("NodeReconciler"), timoutVal, cmInfo, nodeDelInfo)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create node reconciler: %w", err)
	}

	cm := configmanager.New(mgr.GetClient(), cr, nr, mgr.GetLogger().WithName("ConfigManager"), timoutVal, limit, cmInfo, nodeDelInfo)

	if err := mgr.Add(newOnLeaderElectionEvent(cm)); err != nil {
		return nil, nil, fmt.Errorf("unable to create OnLeadeElectionEvent: %w", err)
	}

	if err = (&controllers.VRFRouteConfigurationReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Reconciler: cr,
	}).SetupWithManager(mgr); err != nil {
		return nil, nil, fmt.Errorf("unable to create VRFRouteConfiguration controller: %w", err)
	}

	if err = (&controllers.Layer2NetworkConfigurationReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Reconciler: cr,
	}).SetupWithManager(mgr); err != nil {
		return nil, nil, fmt.Errorf("unable to create Layer2NetworkConfiguration controller: %w", err)
	}

	if err = (&controllers.RoutingTableReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Reconciler: cr,
	}).SetupWithManager(mgr); err != nil {
		return nil, nil, fmt.Errorf("unable to create RoutingTable controller: %w", err)
	}

	if err = (&controllers.NodeReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Reconciler: nr,
	}).SetupWithManager(mgr); err != nil {
		return nil, nil, fmt.Errorf("unable to create RoutingTable controller: %w", err)
	}

	return cr, nr, nil
}

func setMangerOptions(configFile string) (*manager.Options, error) {
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

	// force leader election
	options.LeaderElection = true
	if options.LeaderElectionID == "" {
		options.LeaderElectionID = "network-operator-configurator"
	}

	// force turn off metrics server
	options.MetricsBindAddress = "0"

	return &options, nil
}

type onLeaderElectionEvent struct {
	cm *configmanager.ConfigManager
}

func newOnLeaderElectionEvent(cm *configmanager.ConfigManager) *onLeaderElectionEvent {
	return &onLeaderElectionEvent{
		cm: cm,
	}
}

func (*onLeaderElectionEvent) NeedLeaderElection() bool {
	return true
}

func (e *onLeaderElectionEvent) Start(ctx context.Context) error {
	setupLog.Info("onLeaderElectionEvent started")
	if err := e.cm.DirtyStartup(ctx); err != nil {
		return fmt.Errorf("error while checking previous leader work: %w", err)
	}

	watchNodesErr := make(chan error)
	watchConfigsErr := make(chan error)
	leCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go e.cm.WatchDeletedNodes(leCtx, watchNodesErr)
	go e.cm.WatchConfigs(leCtx, watchConfigsErr)

	select {
	case <-leCtx.Done():
		if err := leCtx.Err(); err != nil {
			return fmt.Errorf("onLeaderElection context error: %w", leCtx.Err())
		}
		return nil
	case err := <-watchNodesErr:
		return fmt.Errorf("node watcher error: %w", err)
	case err := <-watchConfigsErr:
		return fmt.Errorf("config watcher error: %w", err)
	}
}
