package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/monitoring"
	"github.com/telekom/das-schiff-network-operator/pkg/version"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const (
	twenty = 20
)

var (
	setupLog = ctrl.Log.WithName("setup")
)

func main() {
	version.Get().Print(os.Args[0])
	var addr string
	flag.StringVar(&addr, "listen-address", ":7082", "The address to listen on for HTTP requests.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Setup a new registry.
	reg, err := setupPrometheusRegistry()
	if err != nil {
		log.Fatal(fmt.Errorf("prometheus registry setup error: %w", err))
	}

	setupLog.Info("configured Prometheus registry")

	endpoint, err := setupMonitoringEndpoint()
	if err != nil {
		log.Fatal(fmt.Errorf("error configuring monitoring endpoint: %w", err))
	}

	setupLog.Info("configured monitoring endpoint")

	// Expose the registered metrics and monitoring endpoint via HTTP.
	mux := setupMux(reg, endpoint)

	server := http.Server{
		Addr:              addr,
		ReadHeaderTimeout: twenty * time.Second,
		ReadTimeout:       time.Minute,
		Handler:           mux,
	}

	setupLog.Info("created server, starting...", "Addr", server.Addr,
		"ReadHeaderTimeout", server.ReadHeaderTimeout, "ReadTimeout", server.ReadTimeout)

	// Run server
	err = server.ListenAndServe()
	if err != nil {
		log.Fatal(fmt.Errorf("failed to start server: %w", err))
	}
}

func setupPrometheusRegistry() (*prometheus.Registry, error) {
	// Create a new registry.
	reg := prometheus.NewRegistry()

	// Add Go module build info.
	reg.MustRegister(collectors.NewBuildInfoCollector())
	reg.MustRegister(collectors.NewGoCollector())
	collector, err := monitoring.NewDasSchiffNetworkOperatorCollector(
		map[string]bool{
			"frr":     true,
			"netlink": false,
			"bpf":     false,
		})
	if err != nil {
		return nil, fmt.Errorf("failed to create collector %w", err)
	}
	reg.MustRegister(collector)

	return reg, nil
}

func setupMux(reg *prometheus.Registry, e *monitoring.Endpoint) *http.ServeMux {
	mux := e.CreateMux()
	mux.Handle("/metrics", promhttp.HandlerFor(
		reg,
		promhttp.HandlerOpts{
			// Opt into OpenMetrics to support exemplars.
			EnableOpenMetrics: true,
			Timeout:           time.Minute,
		},
	))
	return mux
}

func setupMonitoringEndpoint() (*monitoring.Endpoint, error) {
	clientConfig := ctrl.GetConfigOrDie()
	c, err := client.New(clientConfig, client.Options{})
	if err != nil {
		return nil, fmt.Errorf("error creating controller-runtime client: %w", err)
	}

	setupLog.Info("loaded kubernetes config")

	svcName, svcNamespace, err := monitoring.GetStatusServiceConfig()
	if err != nil {
		return nil, fmt.Errorf("error getting status service info: %w", err)
	}
	setupLog.Info("loaded status service config")

	return monitoring.NewEndpoint(c, frr.NewCli(), svcName, svcNamespace), nil
}
