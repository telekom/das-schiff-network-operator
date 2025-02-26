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
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"time"

	operator2 "github.com/telekom/das-schiff-network-operator/controllers/operator"
	"github.com/telekom/das-schiff-network-operator/pkg/reconciler/operator"
	"github.com/telekom/das-schiff-network-operator/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/managerconfig"
	"github.com/telekom/das-schiff-network-operator/pkg/version"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.) //nolint:gci
	// to ensure that exec-entrypoint and run can make use of them.
	"github.com/open-policy-agent/cert-controller/pkg/rotator"
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

const defaultWebhookPort = 7443

func main() {
	version.Get().Print(os.Args[0])

	var configFile string
	var apiTimeout string
	var configTimeout string
	var preconfigTimeout string
	var maxUpdating int
	var disableCertRotation bool
	var disableRestartOnCertRefresh bool
	flag.StringVar(&configFile, "config", "",
		"The controller will load its initial configuration from this file. "+
			"Omit this flag to use the default configuration values. "+
			"Command-line flags override configuration from this file.")
	flag.StringVar(&apiTimeout, "api-timeout", operator.DefaultTimeout,
		"Timeout for Kubernetes API connections (default: 60s).")
	flag.StringVar(&preconfigTimeout, "preconfig-timeout", operator.DefaultPreconfigTimout, "Timoeut for NodeConfig reconciliation process, when agent DID NOT picked the work yet")
	flag.StringVar(&configTimeout, "config-timeout", operator.DefaultConfigTimeout, "Timoeut for NodeConfig reconciliation process, when agent picked the work")
	flag.IntVar(&maxUpdating, "max-updating", 1, "Configures how many nodes can be updated simultaneously when rolling update is performed.")
	flag.BoolVar(&disableCertRotation, "disable-cert-rotation", false, "Disables certificate rotation if set true.")
	flag.BoolVar(&disableRestartOnCertRefresh, "disable-restart-on-cert-rotation", false, "Disables operator's restart after certificates refresh was performed.")
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

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	var setupFinished chan struct{}

	if !disableCertRotation {
		setupFinished, err = setupRotator(mgr, disableRestartOnCertRefresh)
		if err != nil {
			setupLog.Error(err, "failed to setup add Rotator")
			os.Exit(1)
		}
	}

	setupErr := make(chan error)

	go func() {
		setupErr <- setupReconcilers(mgr, apiTimeout, configTimeout, preconfigTimeout, maxUpdating, setupFinished)
		close(setupErr)
	}()

	ctx, cancel := context.WithCancel(ctrl.SetupSignalHandler())

	setupLog.Info("starting manager")
	mgrErr := make(chan error)
	go func() {
		if err := mgr.Start(ctx); err != nil {
			mgrErr <- err
		}
		close(mgrErr)
	}()

	if err := waitForExit(setupErr, mgrErr, cancel); err != nil {
		setupLog.Error(err, "error")
		os.Exit(1)
	}

	os.Exit(0)
}

func waitForExit(setupErr, mgrErr chan error, cancel context.CancelFunc) error {
	for {
		select {
		case err := <-setupErr:
			if err != nil {
				setupLog.Error(err, "unable to setup reconcilers")
				cancel()
			}
		case err := <-mgrErr:
			if err != nil {
				return fmt.Errorf("manager error: %w", err)
			}
			return nil
		}
	}
}

func setupRotator(mgr ctrl.Manager, disableRestartOnCertRefresh bool) (chan struct{}, error) {
	webhooks := []rotator.WebhookInfo{
		{
			Name: "network-operator-validating-webhook-configuration",
			Type: rotator.Validating,
		},
	}

	podNamespace := utils.GetNamespace()
	baseName := "network-operator-webhook"
	serviceName := fmt.Sprintf("%s-service", baseName)
	secretName := fmt.Sprintf("%s-server-cert", baseName)

	setupFinished := make(chan struct{})
	setupLog.Info("setting up cert rotation")
	if err := rotator.AddRotator(mgr, &rotator.CertRotator{
		SecretKey: types.NamespacedName{
			Namespace: podNamespace,
			Name:      secretName,
		},
		CertDir:                "/tmp/k8s-webhook-server/serving-certs",
		CAName:                 "network-operator-ca",
		CAOrganization:         "network-operator",
		DNSName:                fmt.Sprintf("%s.%s.svc", serviceName, podNamespace),
		ExtraDNSNames:          []string{fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, podNamespace)},
		IsReady:                setupFinished,
		RequireLeaderElection:  true,
		Webhooks:               webhooks,
		RestartOnSecretRefresh: !disableRestartOnCertRefresh,
	}); err != nil {
		close(setupFinished)
		return nil, fmt.Errorf("unable to set up cert rotation: %w", err)
	}

	return setupFinished, nil
}

func setupReconcilers(mgr manager.Manager, apiTimeout, configTimeout, preconfigTimeout string, maxUpdating int, setupFinished chan struct{}) error {
	apiTimoutVal, err := time.ParseDuration(apiTimeout)
	if err != nil {
		return fmt.Errorf("error parsing API timeout value %s: %w", apiTimeout, err)
	}

	configTimeoutVal, err := time.ParseDuration(configTimeout)
	if err != nil {
		return fmt.Errorf("error parsing config timeout value %s: %w", configTimeout, err)
	}

	preconfigTimeoutVal, err := time.ParseDuration(preconfigTimeout)
	if err != nil {
		return fmt.Errorf("error parsing preconfig timeout value %s: %w", preconfigTimeout, err)
	}

	cr, err := operator.NewConfigReconciler(mgr.GetClient(), mgr.GetLogger().WithName("ConfigReconciler"), apiTimoutVal)
	if err != nil {
		return fmt.Errorf("unable to create config reconciler reconciler: %w", err)
	}

	ncr, err := operator.NewNodeConfigReconciler(mgr.GetClient(), mgr.GetLogger().WithName("NodeConfigReconciler"), apiTimoutVal, configTimeoutVal, preconfigTimeoutVal, mgr.GetScheme(), maxUpdating)
	if err != nil {
		return fmt.Errorf("unable to create node reconciler: %w", err)
	}

	if setupFinished != nil {
		<-setupFinished
		setupLog.Info("cert setup finished")
	}

	initialSetup := newOnLeaderElectionEvent(cr)
	if err := mgr.Add(initialSetup); err != nil {
		return fmt.Errorf("error adding on leader election event to the manager: %w", err)
	}

	if err = (&operator2.ConfigReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Reconciler: cr,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create Config controller: %w", err)
	}

	if err = (&operator2.RevisionReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Reconciler: ncr,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create RoutingTable controller: %w", err)
	}

	if err = (&networkv1alpha1.VRFRouteConfiguration{}).SetupWebhookWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create webhook for VRFRouteConfiguration: %w", err)
	}

	return nil
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
		webhookOpts := webhook.Options{
			Host:    "",
			Port:    defaultWebhookPort,
			CertDir: "/tmp/k8s-webhook-server/serving-certs",
			TLSOpts: []func(c *tls.Config){func(c *tls.Config) { c.MinVersion = tls.VersionTLS13 }},
		}

		options = manager.Options{
			Scheme:        scheme,
			WebhookServer: webhook.NewServer(webhookOpts),
		}
	}

	// force leader election
	options.LeaderElection = true
	if options.LeaderElectionID == "" {
		options.LeaderElectionID = "network-operator"
	}

	return &options, nil
}

type onLeaderElectionEvent struct {
	cr *operator.ConfigReconciler
}

func newOnLeaderElectionEvent(cr *operator.ConfigReconciler) *onLeaderElectionEvent {
	return &onLeaderElectionEvent{
		cr: cr,
	}
}

func (*onLeaderElectionEvent) NeedLeaderElection() bool {
	return true
}

func (e *onLeaderElectionEvent) Start(ctx context.Context) error {
	if err := e.cr.ReconcileDebounced(ctx); err != nil {
		return fmt.Errorf("error configuring initial configuration revision: %w", err)
	}
	return nil
}
