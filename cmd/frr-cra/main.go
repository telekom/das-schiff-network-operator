package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/cra"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/monitoring"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/telekom/das-schiff-network-operator/pkg/utils"
)

const (
	serverCert = "/etc/cra/cert.pem"
	serverKey  = "/etc/cra/key.pem"

	frrConfigPath = "/etc/frr/frr.conf"

	baseConfigPath = "/etc/cra/base-config.yaml"

	defaultSleep = 2 * time.Second
)

var (
	frrManager *frr.Manager
	nlManager  *nl.Manager
)

func deleteLayer2(cfg nl.NetlinkConfiguration) error {
	existing, err := nlManager.ListL2()
	if err != nil {
		return fmt.Errorf("error listing L2: %w", err)
	}

	vrfsToDelete, err := getVRFsToDelete(cfg)
	if err != nil {
		return fmt.Errorf("error getting VRFs to delete: %w", err)
	}

	for i := range existing {
		needsDeletion := true
		for j := range cfg.Layer2s {
			if existing[i].VlanID == cfg.Layer2s[j].VlanID {
				needsDeletion = false
				break
			}
		}
		for j := range vrfsToDelete {
			if existing[i].VRF == vrfsToDelete[j].Name {
				needsDeletion = true
				break
			}
		}
		if needsDeletion {
			if err := nlManager.CleanupL2(&existing[i]); len(err) > 0 {
				return fmt.Errorf("error deleting L2 (VLAN: %d): %v", existing[i].VlanID, err)
			}
		}
	}

	return nil
}

func createLayer2(cfg nl.NetlinkConfiguration) error {
	existing, err := nlManager.ListL2()
	if err != nil {
		return fmt.Errorf("error listing L2: %w", err)
	}

	var currentConfig *nl.Layer2Information
	for i := range cfg.Layer2s {
		currentConfig = nil
		for j := range existing {
			if existing[j].VlanID == cfg.Layer2s[i].VlanID {
				currentConfig = &existing[j]
				break
			}
		}
		if currentConfig == nil {
			if err := nlManager.CreateL2(&cfg.Layer2s[i]); err != nil {
				return fmt.Errorf("error creating L2 (VLAN: %d): %w", cfg.Layer2s[i].VlanID, err)
			}
		} else {
			if err := nlManager.ReconcileL2(currentConfig, &cfg.Layer2s[i]); err != nil {
				return fmt.Errorf("error reconciling L2 (VLAN: %d): %w", cfg.Layer2s[i].VlanID, err)
			}
		}
	}

	return nil
}

func getVRFsToDelete(cfg nl.NetlinkConfiguration) ([]nl.VRFInformation, error) {
	existing, err := nlManager.ListL3()
	if err != nil {
		return nil, fmt.Errorf("error listing L3 VRF information: %w", err)
	}

	var toDelete []nl.VRFInformation

	for i := range existing {
		needsDeletion := true
		for j := range cfg.VRFs {
			if cfg.VRFs[j].Name == existing[i].Name && cfg.VRFs[j].VNI == existing[i].VNI {
				needsDeletion = false
				break
			}
		}
		if needsDeletion || existing[i].MarkForDelete {
			toDelete = append(toDelete, existing[i])
		}
	}

	return toDelete, nil
}

func createVRFs(cfg nl.NetlinkConfiguration) error {
	existing, err := nlManager.ListL3()
	if err != nil {
		return fmt.Errorf("error listing L3 VRF information: %w", err)
	}

	for i := range cfg.VRFs {
		alreadyExists := false
		for j := range existing {
			if existing[j].Name == cfg.VRFs[i].Name && existing[j].VNI == cfg.VRFs[i].VNI && !existing[j].MarkForDelete {
				alreadyExists = true
				break
			}
		}
		if !alreadyExists {
			log.Println("Creating VRF", cfg.VRFs[i].Name)
			if err := nlManager.CreateL3(cfg.VRFs[i]); err != nil {
				return fmt.Errorf("error creating L3 (VRF: %s): %w", cfg.VRFs[i].Name, err)
			}
			if err := nlManager.UpL3(cfg.VRFs[i]); err != nil {
				return fmt.Errorf("error setting up L3 (VRF: %s): %w", cfg.VRFs[i].Name, err)
			}
		}
	}

	return nil
}

func reconcileLayer3(cfg nl.NetlinkConfiguration) error {
	vrfsToDelete, err := getVRFsToDelete(cfg)
	if err != nil {
		return fmt.Errorf("error getting VRFs to delete: %w", err)
	}

	for i := range vrfsToDelete {
		errors := nlManager.CleanupL3(vrfsToDelete[i].Name)
		if len(errors) > 0 {
			return fmt.Errorf("error cleaning up L3 (VRF: %s): %v", vrfsToDelete[i].Name, errors)
		}
	}

	if len(vrfsToDelete) > 0 {
		err := reloadFRR()
		if err != nil {
			return fmt.Errorf("error reloading FRR: %w", err)
		}
	}

	time.Sleep(defaultSleep)

	if err := createVRFs(cfg); err != nil {
		return fmt.Errorf("error creating VRFs: %w", err)
	}

	return nil
}

func reloadFRR() error {
	err := frrManager.ReloadFRR()
	if err != nil {
		log.Println("Failed to reload FRR, trying to restart", err)

		err = frrManager.RestartFRR()
		if err != nil {
			log.Println("Failed to restart FRR", err)
			return fmt.Errorf("error reloading / restarting FRR systemd unit: %w", err)
		}
	}
	log.Println("Reloaded FRR config")
	return nil
}

func applyConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Parse Body into NetlinkConfiguration
	var craConfiguration cra.Configuration
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Println("Failed to read request body", err)
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	err = json.Unmarshal(body, &craConfiguration)
	if err != nil {
		log.Println("Failed to unmarshal request body", err)
		http.Error(w, "Failed to unmarshal request body", http.StatusInternalServerError)
		return
	}

	// Write FRR config
	file, err := os.OpenFile(frrConfigPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:mnd
	if err != nil {
		log.Println("Failed to open FRR config file", err)
		http.Error(w, "Failed to open FRR config file", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	_, err = io.Copy(file, strings.NewReader(craConfiguration.FRRConfiguration))
	if err != nil {
		log.Println("Failed to write FRR config", err)
		http.Error(w, "Failed to write FRR config", http.StatusInternalServerError)
		return
	}

	// Reload FRR
	err = reloadFRR()
	if err != nil {
		log.Println("Failed to reload FRR", err)
		http.Error(w, fmt.Sprintf("Failed to reload FRR: %v", err), http.StatusInternalServerError)
		return
	}

	// Delete Layer2
	err = deleteLayer2(craConfiguration.NetlinkConfiguration)
	if err != nil {
		log.Println("Failed to reconcile Layer2", err)
		http.Error(w, fmt.Sprintf("Failed to reconcile Layer2: %v", err), http.StatusInternalServerError)
		return
	}

	// Reconcile Layer3
	err = reconcileLayer3(craConfiguration.NetlinkConfiguration)
	if err != nil {
		log.Println("Failed to reconcile Layer3", err)
		http.Error(w, fmt.Sprintf("Failed to reconcile Layer3: %v", err), http.StatusInternalServerError)
		return
	}

	// Recreate Layer2
	err = createLayer2(craConfiguration.NetlinkConfiguration)
	if err != nil {
		log.Println("Failed to reconcile Layer2", err)
		http.Error(w, fmt.Sprintf("Failed to reconcile Layer2: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func executeFrr(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Body == nil {
		log.Println("Request body is empty")
		http.Error(w, "Request body is empty", http.StatusBadRequest)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		log.Println("Failed to read request body", err)
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}
	bodyContent := string(bodyBytes)

	data := frrManager.Cli.Execute(strings.Split(bodyContent, " "))
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write(data); err != nil {
		log.Println("Failed to write response", err)
		return
	}
}

func setupTLS(address net.IP) error {
	certPrivKey, err := rsa.GenerateKey(rand.Reader, 4096) //nolint:mnd
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	certTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"FRR-CRA"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0), //nolint:mnd
		KeyUsage:              x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:           []net.IP{address},
		BasicConstraintsValid: true,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, certTemplate, certTemplate, &certPrivKey.PublicKey, certPrivKey)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	certOut, err := os.Create(serverCert)
	if err != nil {
		return fmt.Errorf("failed to open certificate file: %w", err)
	}
	if err := pem.Encode(certOut, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	}); err != nil {
		return fmt.Errorf("failed to encode certificate: %w", err)
	}

	certPrivKeyPEM, err := os.Create(serverKey)
	if err != nil {
		return fmt.Errorf("failed to open private key file: %w", err)
	}
	if err := pem.Encode(certPrivKeyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(certPrivKey),
	}); err != nil {
		return fmt.Errorf("failed to encode private key: %w", err)
	}
	return nil
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
			"netlink": true,
		})
	if err != nil {
		return nil, fmt.Errorf("failed to create collector %w", err)
	}
	reg.MustRegister(collector)

	return reg, nil
}

func createListener(ip net.IP, port int, bindInterface string) (net.Listener, error) {
	var domain int
	var socketAddress syscall.Sockaddr
	if ip.To4() != nil {
		domain = syscall.AF_INET
		socketAddress = &syscall.SockaddrInet4{
			Port: port,
		}
		copy(socketAddress.(*syscall.SockaddrInet4).Addr[:], ip.To4())
	} else {
		domain = syscall.AF_INET6
		socketAddress = &syscall.SockaddrInet6{
			Port: port,
		}
		copy(socketAddress.(*syscall.SockaddrInet6).Addr[:], ip.To16())
	}

	fd, err := syscall.Socket(domain, syscall.SOCK_STREAM, syscall.IPPROTO_TCP)
	if err != nil {
		return nil, fmt.Errorf("failed to create socket: %w", err)
	}

	if bindInterface != "" {
		err = syscall.BindToDevice(fd, bindInterface)
		if err != nil {
			return nil, fmt.Errorf("failed to bind to device %s: %w", bindInterface, err)
		}
	}

	err = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to set socket options: %w", err)
	}

	if err := syscall.Bind(fd, socketAddress); err != nil {
		return nil, fmt.Errorf("failed to bind socket: %w", err)
	}

	if err := syscall.Listen(fd, syscall.SOMAXCONN); err != nil {
		return nil, fmt.Errorf("failed to listen on socket: %w", err)
	}

	file := os.NewFile(uintptr(fd), fmt.Sprintf("%s:%d", ip, port))
	listener, err := net.FileListener(file)
	if err != nil {
		return nil, fmt.Errorf("failed to create listener: %w", err)
	}
	return listener, nil
}

func main() {
	ip := flag.String("ip", "fd00:7:caa5::", "IP to listen on and generate certificate for")
	bindInterface := flag.String("bind-interface", "cluster", "Bind interface to use for netlink")
	port := flag.Int("port", 8443, "Port to listen on") //nolint:mnd
	flag.Parse()

	parsedIP := net.ParseIP(*ip)
	if parsedIP == nil {
		log.Fatal("Invalid IP")
	}

	baseConfig, err := config.LoadBaseConfig(baseConfigPath)
	if err != nil {
		log.Fatal("Failed to load base config", err)
	}

	frrManager = frr.NewFRRManager()
	nlManager = nl.NewManager(&nl.Toolkit{}, baseConfig)

	registry, err := setupPrometheusRegistry()
	if err != nil {
		log.Fatal("Failed to setup Prometheus registry", err)
	}

	http.HandleFunc("/frr/configuration", applyConfig)
	http.HandleFunc("/frr/command", executeFrr)
	http.Handle("/frr/metrics", promhttp.HandlerFor(
		registry,
		promhttp.HandlerOpts{
			// Opt into OpenMetrics to support exemplars.
			EnableOpenMetrics: true,
			Timeout:           time.Minute,
		},
	))

	exporterURL, err := url.Parse("http://localhost:9100")
	if err != nil {
		log.Fatal("Failed to parse URL", err)
	}
	// Build proxy for local node-exporter
	proxy := httputil.NewSingleHostReverseProxy(exporterURL)
	http.Handle("/node-exporter/metrics", utils.ExactPathHandler("/node-exporter/metrics", http.StripPrefix("/node-exporter", proxy)))

	// Check if the server certificate and key exist
	if _, err := os.Stat(serverCert); os.IsNotExist(err) {
		err = setupTLS(parsedIP)
		if err != nil {
			log.Fatal("Failed to setup TLS", err)
		}
	}
	if _, err := os.Stat(serverKey); os.IsNotExist(err) {
		err = setupTLS(parsedIP)
		if err != nil {
			log.Fatal("Failed to setup TLS", err)
		}
	}

	caCert, err := os.ReadFile(serverCert)
	if err != nil {
		log.Fatal("Failed to read CA certificate", err)
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	//nolint:gosec
	tlsConfig := &tls.Config{
		ClientCAs:  caCertPool,
		ClientAuth: tls.RequireAndVerifyClientCert,
	}

	//nolint:gosec
	server := &http.Server{
		TLSConfig: tlsConfig,
	}

	listener, err := createListener(parsedIP, *port, *bindInterface)
	if err != nil {
		log.Fatal("Failed to create listener", err)
	}

	err = server.ServeTLS(listener, serverCert, serverKey)
	if err != nil {
		log.Fatal("Failed to start server", err)
	}
}
