package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/monitoring"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const (
	twenty = 20
)

func main() {
	var addr string
	flag.StringVar(&addr, "listen-address", ":7082", "The address to listen on for HTTP requests.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

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
		log.Fatal(fmt.Errorf("failed to create collector %w", err))
	}
	reg.MustRegister(collector)

	clientConfig := ctrl.GetConfigOrDie()
	c, err := client.New(clientConfig, client.Options{})
	if err != nil {
		log.Fatal(fmt.Errorf("error creating controller-runtime client: %w", err))
	}

	frrCli := frr.NewCli()

	endpoint := monitoring.NewEndpoint(c, frrCli)
	endpoint.SetHandlers()

	// Expose the registered metrics via HTTP.
	http.Handle("/metrics", promhttp.HandlerFor(
		reg,
		promhttp.HandlerOpts{
			// Opt into OpenMetrics to support exemplars.
			EnableOpenMetrics: true,
			Timeout:           time.Minute,
		},
	))
	server := http.Server{
		Addr:              addr,
		ReadHeaderTimeout: twenty * time.Second,
		ReadTimeout:       time.Minute,
	}
	err = server.ListenAndServe()
	// Run server
	if err != nil {
		log.Fatal(fmt.Errorf("failed to start server: %w", err))
	}
}
