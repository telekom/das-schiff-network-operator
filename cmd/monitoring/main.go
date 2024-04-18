package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

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
	flag.StringVar(&addr, "listen-address", ":7083", "The address to listen on for HTTP requests.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	clientConfig := ctrl.GetConfigOrDie()
	c, err := client.New(clientConfig, client.Options{})
	if err != nil {
		log.Fatal(fmt.Errorf("error creating controller-runtime client: %w", err))
	}

	svcName, svcNamespace, err := monitoring.GetStatusServiceConfig()
	if err != nil {
		log.Fatal(fmt.Errorf("error getting status service info: %w", err))
	}

	endpoint := monitoring.NewEndpoint(c, frr.NewCli(), svcName, svcNamespace)

	server := http.Server{
		Addr:              addr,
		ReadHeaderTimeout: twenty * time.Second,
		ReadTimeout:       time.Minute,
		Handler:           endpoint.CreateMux(),
	}
	err = server.ListenAndServe()
	// Run server
	if err != nil {
		log.Fatal(fmt.Errorf("failed to start server: %w", err))
	}
}
