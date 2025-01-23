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
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
)

const (
	serverCert = "/etc/cra/cert.pem"
	serverKey  = "/etc/cra/key.pem"

	frrConfigPath = "/etc/frr/frr.conf"

	baseConfigPath = "/etc/cra/base-config.yaml"
)

var (
	frrManager *frr.Manager
	nlManager  *nl.Manager
)

func applyNetlink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Parse Body into NetlinkConfiguration
	var netlinkConfiguration nl.NetlinkConfiguration
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Println("Failed to read request body", err)
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	err = json.Unmarshal(body, &netlinkConfiguration)
	if err != nil {
		log.Println("Failed to unmarshal request body", err)
		http.Error(w, "Failed to unmarshal request body", http.StatusInternalServerError)
		return
	}

	err = nlManager.ReconcileNetlinkConfiguration(netlinkConfiguration)
	if err != nil {
		log.Println("Failed to reconcile Netlink configuration", err)
		http.Error(w, fmt.Sprintf("Failed to reconcile Netlink configuration: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func applyFrr(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	file, err := os.OpenFile(frrConfigPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		log.Println("Failed to open FRR config file", err)
		http.Error(w, "Failed to open FRR config file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	_, err = io.Copy(file, r.Body)
	if err != nil {
		log.Println("Failed to write FRR config", err)
		http.Error(w, "Failed to write FRR config", http.StatusInternalServerError)
		return
	}

	err = frrManager.ReloadFRR()
	if err != nil {
		log.Println("Failed to reload FRR, trying to restart", err)

		err = frrManager.RestartFRR()
		if err != nil {
			log.Println("Failed to restart FRR", err)
			http.Error(w, "Failed to restart FRR", http.StatusInternalServerError)
			return
		}
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
	certPrivKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	certTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"FRR-CRA"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
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

func main() {
	ip := flag.String("ip", "169.254.1.0", "IP to listen on and generate certificate for")
	port := flag.Int("port", 8443, "Port to listen on")
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
	nlManager = nl.NewManager(&nl.Toolkit{}, *baseConfig)

	http.HandleFunc("/netlink/config", applyNetlink)
	http.HandleFunc("/frr/config", applyFrr)
	http.HandleFunc("/frr/execute", executeFrr)

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

	tlsConfig := &tls.Config{
		ClientCAs:  caCertPool,
		ClientAuth: tls.RequireAndVerifyClientCert,
	}

	server := &http.Server{
		Addr:      fmt.Sprintf(":%d", *port),
		TLSConfig: tlsConfig,
	}

	err = server.ListenAndServeTLS(serverCert, serverKey)
	if err != nil {
		log.Fatal("Failed to start server", err)
	}
}
