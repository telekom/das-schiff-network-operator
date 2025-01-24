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
	"os"
	"strings"
	"time"

	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/cra"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
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

func reconcileLayer2(config nl.NetlinkConfiguration) error {
	existing, err := nlManager.ListL2()
	if err != nil {
		return fmt.Errorf("error listing L2: %w", err)
	}

	var toCreate []nl.Layer2Information
	var toDelete []nl.Layer2Information

	for i := range config.Layer2s {
		var currentConfig *nl.Layer2Information = nil
		for j := range existing {
			if existing[j].VlanID == config.Layer2s[i].VlanID {
				currentConfig = &existing[j]
				break
			}
		}
		if currentConfig == nil {
			toCreate = append(toCreate, config.Layer2s[i])
		} else {
			if err := nlManager.ReconcileL2(currentConfig, &config.Layer2s[i]); err != nil {
				return fmt.Errorf("error reconciling L2 (VLAN: %d): %w", config.Layer2s[i].VlanID, err)
			}
		}
	}

	for i := range existing {
		needsDeletion := true
		for j := range config.Layer2s {
			if existing[i].VlanID == config.Layer2s[j].VlanID {
				needsDeletion = false
				break
			}
		}
		if needsDeletion {
			toDelete = append(toDelete, existing[i])
		}
	}

	for i := range toDelete {
		if err := nlManager.CleanupL2(&toDelete[i]); len(err) > 0 {
			return fmt.Errorf("error deleting L2 (VLAN: %d): %v", toDelete[i].VlanID, err)
		}
	}

	for i := range toCreate {
		if err := nlManager.CreateL2(&toCreate[i]); err != nil {
			return fmt.Errorf("error creating L2 (VLAN: %d): %w", toCreate[i].VlanID, err)
		}
	}

	return nil
}

func updateL3Netlink(config nl.NetlinkConfiguration) ([]nl.VRFInformation, bool, error) {
	existing, err := nlManager.ListL3()
	if err != nil {
		return nil, false, fmt.Errorf("error listing L3 VRF information: %w", err)
	}

	var toCreate []nl.VRFInformation
	var toDelete []nl.VRFInformation

	for i := range existing {
		needsDeletion := true
		for j := range config.VRFs {
			if config.VRFs[j].Name == existing[i].Name && config.VRFs[j].VNI == existing[i].VNI {
				needsDeletion = false
				break
			}
		}
		if needsDeletion || existing[i].MarkForDelete {
			toDelete = append(toDelete, existing[i])
		}
	}

	for i := range config.VRFs {
		alreadyExists := false
		for j := range existing {
			if existing[j].Name == config.VRFs[i].Name && existing[j].VNI == config.VRFs[i].VNI && !existing[j].MarkForDelete {
				alreadyExists = true
				break
			}
		}
		if !alreadyExists {
			toCreate = append(toCreate, config.VRFs[i])
		}
	}

	for i := range toDelete {
		errors := nlManager.CleanupL3(toDelete[i].Name)
		if len(errors) > 0 {
			return nil, false, fmt.Errorf("error cleaning up L3 (VRF: %s): %v", toDelete[i].Name, errors)
		}
	}

	for i := range toCreate {
		log.Println("Creating VRF", toCreate[i].Name)
		if err := nlManager.CreateL3(toCreate[i]); err != nil {
			return nil, false, fmt.Errorf("error creating L3 (VRF: %s): %w", toCreate[i].Name, err)
		}
	}

	return toCreate, len(toDelete) > 0, nil
}

func reconcileLayer3(config nl.NetlinkConfiguration) error {
	created, deletedVRF, err := updateL3Netlink(config)
	if err != nil {
		return fmt.Errorf("error updating L3: %w", err)
	}

	if deletedVRF {
		err := reloadFRR()
		if err != nil {
			return fmt.Errorf("error reloading FRR: %w", err)
		}
	}

	time.Sleep(defaultSleep)

	for i := range created {
		if err := nlManager.UpL3(created[i]); err != nil {
			return fmt.Errorf("error setting up L3 (VRF: %s): %w", created[i].Name, err)
		}
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
	var craConfiguration cra.CRAConfiguration
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
	file, err := os.OpenFile(frrConfigPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
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

	// Reconcile Layer2
	err = reconcileLayer2(craConfiguration.NetlinkConfiguration)
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

	http.HandleFunc("/config", applyConfig)
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
