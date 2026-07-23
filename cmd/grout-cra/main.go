// Command grout-cra is the CRA-netns sidecar for the cra-grout flavor. It runs
// inside the grout container (alongside grcli, the grout control socket at
// /run/grout.sock, and a patched FRR with the dplane_grout.so zebra plugin) and
// exposes an mTLS HTTP API the cra-grout agent posts to. It mirrors the frr-cra
// sidecar but applies a grcli batch (the grout fast-path desired state) instead
// of a netlink configuration.
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
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	cra "github.com/telekom/das-schiff-network-operator/pkg/cra-grout"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
)

const (
	serverCert = "/etc/cra/cert.pem"
	serverKey  = "/etc/cra/key.pem"

	grcliPathDefault = "grcli"

	// frrConfigFileMode is the permission mode for the rewritten FRR config file.
	frrConfigFileMode = 0o600
)

// frrConfigPath is the FRR config file the sidecar rewrites; a var so tests can
// point it at a temp file.
var frrConfigPath = "/etc/frr/frr.conf"

var (
	frrManager *frr.Manager
	grcliPath  string
	applyMu    sync.Mutex // serializes config applications
)

// logSanitizer strips newlines to prevent log-injection from request content.
var logSanitizer = strings.NewReplacer("\n", "", "\r", "")

// applyGrout handles POST /grout/configuration: it writes the FRR config,
// reloads FRR, and applies the grcli batch to grout.
func applyGrout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	applyMu.Lock()
	defer applyMu.Unlock()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	var cfg cra.Configuration
	if err := json.Unmarshal(body, &cfg); err != nil {
		log.Print(logSanitizer.Replace(fmt.Sprintf("Failed to parse request: %v", err)))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := applyFRRConfig(cfg.FRRConfiguration); err != nil {
		log.Print(logSanitizer.Replace(fmt.Sprintf("Failed to apply FRR config: %v", err)))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if out, err := applyGrcliBatch(cfg.GrcliBatch); err != nil {
		log.Print(logSanitizer.Replace(fmt.Sprintf("Failed to apply grcli batch: %v: %s", err, out)))
		http.Error(w, fmt.Sprintf("failed to apply grcli batch: %v: %s", err, out), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// executeGrcli handles POST /grout/command: an ad-hoc grcli invocation whose
// space-separated arguments are the request body.
func executeGrcli(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	args := strings.Fields(string(body))
	//nolint:gosec // args come from the mTLS-authenticated agent only.
	out, err := exec.Command(grcliPath, args...).CombinedOutput()
	if err != nil {
		http.Error(w, fmt.Sprintf("grcli error: %v: %s", err, out), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(out); err != nil {
		log.Println("Failed to write response", err)
	}
}

// applyGrcliBatch applies the grcli batch line-by-line (each line via `grcli -e`)
// rather than `grcli -ef <file>`, which aborts the whole batch on the first
// error. The batch is a full desired-state replay of every VRF/port/route on the
// node, so when a second pod's reconcile re-applies an object the first pod
// already created, grout returns an "exists" error for that line. Those errors
// are expected and tolerated (idempotent reconcile); any other error is fatal.
// Comment (`#`) and blank lines are skipped. It returns the accumulated grcli
// output, and the first non-tolerated error encountered.
func applyGrcliBatch(batch string) (string, error) {
	if strings.TrimSpace(batch) == "" {
		return "", nil
	}

	var output strings.Builder
	for _, raw := range strings.Split(batch, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		//nolint:gosec // grcliPath is an operator-controlled flag; the batch line is agent-generated.
		out, err := exec.Command(grcliPath, "-e", line).CombinedOutput()
		_, _ = output.Write(out)
		if err != nil {
			if isGrcliExistsError(out) {
				// Object already present from a prior pod's reconcile; tolerate.
				continue
			}
			return output.String(), fmt.Errorf("grcli %q failed: %w", line, err)
		}
	}
	return output.String(), nil
}

// grcliExistsMarkers are the substrings grout/grcli emit when an object already
// exists (EEXIST) so a full desired-state replay can be applied idempotently.
var grcliExistsMarkers = []string{"File exists", "already exists", "EEXIST", "exists"}

// isGrcliExistsError reports whether grcli output indicates the object already
// exists (a tolerated, idempotent-reconcile condition).
func isGrcliExistsError(out []byte) bool {
	s := strings.ToLower(string(out))
	for _, m := range grcliExistsMarkers {
		if strings.Contains(s, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

// applyFRRConfig applies the FRR control-plane config for the grout flavor using
// restart-on-change semantics: it rewrites /etc/frr/frr.conf and RESTARTS FRR
// only when the desired config differs from what is already on disk; an
// unchanged config is a no-op (no restart, no BGP flap).
//
// A full restart -- not a hot `frr-reload.py` reload -- is required on the grout
// flavor: a hot reload de-classifies grout's EVPN VNIs via the dplane_grout
// zebra plugin (the L2VNI/L3VNI fdb+FIB survive in grout, but `show evpn vni`
// empties and type-5 import breaks), whereas a clean restart re-syncs the VNIs.
// Node FRR config only changes on VRF/VNI topology edits -- routed workload
// /32,/128 host routes live in grout's FIB (applied via grcli), not in FRR -- so
// restart-on-change does not flap BGP on pod/VM churn.
func applyFRRConfig(frrConfig string) error {
	changed, err := frrConfigChanged(frrConfig)
	if err != nil {
		return fmt.Errorf("failed to compare FRR config: %w", err)
	}
	if !changed {
		return nil
	}
	if err := writeFRRConfig(frrConfig); err != nil {
		return fmt.Errorf("failed to write FRR config: %w", err)
	}
	if err := frrManager.RestartFRR(); err != nil {
		return fmt.Errorf("failed to restart FRR: %w", err)
	}
	return nil
}

// frrConfigChanged reports whether desired differs from the current on-disk FRR
// config at frrConfigPath. A missing config file counts as changed.
func frrConfigChanged(desired string) (bool, error) {
	current, err := os.ReadFile(frrConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("failed to read FRR config file: %w", err)
	}
	return string(current) != desired, nil
}

func writeFRRConfig(frrConfig string) error {
	file, err := os.OpenFile(frrConfigPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, frrConfigFileMode)
	if err != nil {
		return fmt.Errorf("failed to open FRR config file: %w", err)
	}
	if _, err := io.Copy(file, strings.NewReader(frrConfig)); err != nil {
		_ = file.Close()
		return fmt.Errorf("failed to write FRR config: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to close FRR config file: %w", err)
	}
	return nil
}

func setupTLS(address net.IP) error {
	certPrivKey, err := rsa.GenerateKey(rand.Reader, 4096) //nolint:mnd
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	certTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"GROUT-CRA"}},
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

	if err := writePEM(serverCert, "CERTIFICATE", certBytes); err != nil {
		return err
	}
	return writePEM(serverKey, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(certPrivKey))
}

func writePEM(path, blockType string, der []byte) error {
	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", path, err)
	}
	if err := pem.Encode(out, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		_ = out.Close()
		return fmt.Errorf("failed to encode %s: %w", path, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("failed to close %s: %w", path, err)
	}
	return nil
}

func createListener(ip net.IP, port int, bindInterface string) (net.Listener, error) {
	var domain int
	var socketAddress syscall.Sockaddr
	if ip.To4() != nil {
		domain = syscall.AF_INET
		sa := &syscall.SockaddrInet4{Port: port}
		copy(sa.Addr[:], ip.To4())
		socketAddress = sa
	} else {
		domain = syscall.AF_INET6
		sa := &syscall.SockaddrInet6{Port: port}
		copy(sa.Addr[:], ip.To16())
		socketAddress = sa
	}

	fd, err := syscall.Socket(domain, syscall.SOCK_STREAM, syscall.IPPROTO_TCP)
	if err != nil {
		return nil, fmt.Errorf("failed to create socket: %w", err)
	}
	if bindInterface != "" {
		if err := syscall.BindToDevice(fd, bindInterface); err != nil {
			return nil, fmt.Errorf("failed to bind to device %s: %w", bindInterface, err)
		}
	}
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
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
	bindInterface := flag.String("bind-interface", "", "optional interface to bind the listener to")
	port := flag.Int("port", 8443, "Port to listen on") //nolint:mnd
	grcli := flag.String("grcli", grcliPathDefault, "path to the grcli binary")
	flag.Parse()

	grcliPath = *grcli

	parsedIP := net.ParseIP(*ip)
	if parsedIP == nil {
		log.Fatal("Invalid IP")
	}

	frrManager = frr.NewFRRManager()

	http.HandleFunc("/grout/configuration", applyGrout)
	http.HandleFunc("/grout/command", executeGrcli)

	if _, err := os.Stat(serverCert); os.IsNotExist(err) {
		if err := setupTLS(parsedIP); err != nil {
			log.Fatal("Failed to setup TLS", err)
		}
	}
	if _, err := os.Stat(serverKey); os.IsNotExist(err) {
		if err := setupTLS(parsedIP); err != nil {
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
	server := &http.Server{TLSConfig: tlsConfig}

	listener, err := createListener(parsedIP, *port, *bindInterface)
	if err != nil {
		log.Fatal("Failed to create listener", err)
	}

	if err := server.ServeTLS(listener, serverCert, serverKey); err != nil {
		log.Fatal("Failed to start server", err)
	}
}
