// Command grout-cra is the CRA-netns sidecar for the cra-grout flavor. It runs
// inside the grout container (alongside grcli, the grout control socket at
// /run/grout.sock, and a patched FRR with the dplane_grout.so zebra plugin) and
// exposes an mTLS HTTP API the cra-grout agent posts to. It mirrors the frr-cra
// sidecar but applies a grcli batch (the grout fast-path desired state) instead
// of a netlink configuration.
package main

import (
	"context"
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

// applyGrout handles POST /grout/configuration: it applies the grcli batch to
// grout, prunes stale ports, then writes the FRR config and reloads FRR.
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

	// The fast path is programmed before the control plane, and the order is
	// load-bearing rather than cosmetic. grout keeps a single FIB entry per
	// prefix and refuses to install a second one (EBUSY), while the tenant
	// aggregates the operator renders as blackhole statics cover exactly the
	// L2 subnets whose anycast gateway the batch puts on the IRB bridge. Let
	// zebra install its blackhole first and the interface address can never be
	// added afterwards -- zebra reinstalls the route faster than any
	// delete-then-add can slip in between, so the node stays gatewayless.
	//
	// Applied in this order, the connected route is already in grout when zebra
	// starts up; dplane_grout imports the address, zebra prefers the connected
	// route over the static (distance 0 against 1) and never installs the
	// blackhole into the fast path. That also holds on every later reconcile,
	// because the address outlives the config reload.
	// Detached from the request: a client that hangs up mid-apply must not
	// cancel a batch grout is already executing.
	ctx := context.WithoutCancel(r.Context())

	if out, err := applyGrcliBatch(ctx, cfg.GrcliBatch); err != nil {
		log.Print(logSanitizer.Replace(fmt.Sprintf("Failed to apply grcli batch: %v: %s", err, out)))
		http.Error(w, fmt.Sprintf("failed to apply grcli batch: %v: %s", err, out), http.StatusInternalServerError)
		return
	}

	if err := pruneStalePorts(ctx, cfg.GrcliBatch); err != nil {
		log.Print(logSanitizer.Replace(fmt.Sprintf("Failed to prune stale grout ports: %v", err)))
		http.Error(w, fmt.Sprintf("failed to prune stale grout ports: %v", err), http.StatusInternalServerError)
		return
	}

	//nolint:contextcheck // frrManager.ReloadFRR is the shared FRR helper used by every flavour and takes no context; wiring one in is a change to that package, not to this one.
	if err := applyFRRConfig(cfg.FRRConfiguration); err != nil {
		log.Print(logSanitizer.Replace(fmt.Sprintf("Failed to apply FRR config: %v", err)))
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	// args come from the mTLS-authenticated agent only.
	out, err := runGrcli(context.WithoutCancel(r.Context()), strings.Fields(string(body))...)
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
//
// Tolerating EEXIST is only sound because the batch names objects by identity
// rather than by position: a port's vdev name is derived from the port and a
// nexthop's id from (iface, address), so "already exists" really does mean "the
// live object is the one being asked for". When those names were allocated from
// a counter this same tolerance quietly preserved objects that no longer
// matched their new meaning -- see vdevName and nexthopID.
//
// A mid-batch failure leaves the earlier lines applied. That is recoverable
// rather than atomic, and deliberately so: the reconcile is retried and the
// replay converges, whereas rolling back would require deleting objects that
// may predate this request. What must not happen is pruning against a partial
// desired state, which is why the caller skips the prune when this fails.
func applyGrcliBatch(ctx context.Context, batch string) (string, error) {
	if strings.TrimSpace(batch) == "" {
		return "", nil
	}

	var output strings.Builder
	// Filled in on first use: most batches create no workload tap port, and a
	// reconcile should not pay for a grcli round trip it does not need.
	var live map[string]bool
	for _, raw := range strings.Split(batch, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// A workload port grout already has must not be created again. The add
		// carries the port MTU, and once the CNI has moved that port's tap into
		// the pod netns grout no longer owns the netdev: the line comes back
		// EPERM rather than the EEXIST a replay tolerates, which fails the whole
		// reconcile, marks the NodeNetworkConfig invalid and -- because grout
		// unwinds the half-created port -- destroys the tap out from under the
		// running pod.
		//
		// Skipping is only sound while the live port is what the line asks for,
		// so it is checked rather than assumed. A port that has drifted cannot
		// be brought back into line either -- its netdev is the pod's now -- so
		// this reports what has to happen instead of pretending to converge.
		if add, ok := cra.WorkloadTapPortAdd(line); ok {
			if live == nil {
				var err error
				if live, err = liveWorkloadPorts(ctx); err != nil {
					return output.String(), err
				}
			}
			if live[add.Name] {
				detail, err := workloadPortDetail(ctx, add.Name)
				if err != nil {
					return output.String(), err
				}
				if diff := add.Mismatch(detail); diff != "" {
					return output.String(), fmt.Errorf(
						"live workload port %s does not match the configuration (%s): its tap belongs to the pod, so the attachment has to be recreated",
						add.Name, diff)
				}
				continue
			}
		}

		// grcli takes the command as separate argv entries: handing it the whole
		// line as a single argument makes it one unparseable token and every
		// command fails with "invalid arguments". Our batch lines never contain
		// quoted arguments (devargs use commas, not spaces), so splitting on
		// whitespace is exact.
		out, err := runGrcli(ctx, append([]string{"-e"}, strings.Fields(line)...)...)
		_, _ = output.Write(out)
		if err != nil {
			if isGrcliExistsError(out) {
				// Object already present from a prior pod's reconcile; tolerate,
				// which is what makes a full desired-state replay idempotent.
				//
				// Tolerating means "leave it as it is", not "make it match": an
				// object whose desired configuration changed while its name
				// stayed the same keeps the old configuration, and the prune
				// does not catch it either, because its name is still desired.
				// Workload ports are safe -- their name is derived from the
				// container, so a changed attachment is a different name and the
				// old one is pruned -- but a renamed-in-place infrastructure
				// object (an uplink's devargs, a VRF's VNI) would need an
				// explicit delete to converge.
				continue
			}
			return output.String(), fmt.Errorf("grcli %q failed: %w", line, err)
		}
	}
	return output.String(), nil
}

// liveWorkloadPorts asks grout which workload ports it currently holds.
func liveWorkloadPorts(ctx context.Context) (map[string]bool, error) {
	out, err := runGrcli(ctx, "-j", "interface", "show")
	if err != nil {
		return nil, fmt.Errorf("listing grout interfaces: %w: %s", err, out)
	}
	ifaces, err := cra.ParseInterfaces(out)
	if err != nil {
		return nil, fmt.Errorf("reading grout interface list: %w", err)
	}
	return cra.LiveWorkloadPorts(ifaces), nil
}

// workloadPortDetail reads back one live workload port. The list liveWorkloadPorts
// uses carries neither the MTU nor a VRF the port is bound to, so verifying an
// adopted tap needs this second look -- taken only for the ports that are both
// in the batch and already live, which is at most one per attachment.
func workloadPortDetail(ctx context.Context, name string) (*cra.InterfaceDetail, error) {
	// name came out of the batch and has already passed the workload-port pattern.
	out, err := runGrcli(ctx, "-j", "interface", "show", name)
	if err != nil {
		return nil, fmt.Errorf("showing grout interface %q: %w: %s", name, err, out)
	}
	detail, err := cra.ParseInterfaceDetail(out)
	if err != nil {
		return nil, fmt.Errorf("reading grout interface %q: %w", name, err)
	}
	return detail, nil
}

// pruneStalePorts deletes the workload ports -- and the trunk VLAN
// sub-interfaces above them -- that grout still has but the desired state no
// longer declares.
//
// The batch is applied with create-only commands, so this is the only thing
// that ever removes a port: without it a deleted pod's tap or vhost-user port
// lives on in the fast path forever, together with its address and host routes.
// `interface del` also drops the addresses and nexthops bound to the interface,
// so ports and their sub-interfaces are the only objects that need pruning
// explicitly. StaleInterfaces returns them child-first, which grout requires.
//
// An entirely empty batch is treated as "nothing was rendered" rather than "no
// ports are wanted", and prunes nothing. A node that genuinely has no workload
// ports still renders its VRFs, so the empty string only occurs when rendering
// produced nothing at all -- and deleting every workload port on the node is
// too destructive to infer from an empty request.
func pruneStalePorts(ctx context.Context, batch string) error {
	if strings.TrimSpace(batch) == "" {
		return nil
	}

	out, err := runGrcli(ctx, "-j", "interface", "show")
	if err != nil {
		return fmt.Errorf("listing grout interfaces: %w: %s", err, out)
	}

	live, err := cra.ParseInterfaces(out)
	if err != nil {
		return fmt.Errorf("reading grout interface list: %w", err)
	}

	for _, name := range cra.StaleInterfaces(cra.DesiredInterfaceNames(batch), live) {
		// name comes from grout itself.
		if delOut, delErr := runGrcli(ctx, "interface", "del", name); delErr != nil {
			// An interface that is already gone is the desired outcome, not a
			// failure.
			if isGrcliMissingError(delOut) {
				continue
			}
			return fmt.Errorf("deleting stale interface %q: %w: %s", name, delErr, delOut)
		}
		log.Print(logSanitizer.Replace(fmt.Sprintf("Pruned stale grout interface %s", name)))
	}
	return nil
}

// grcliMissingMarkers are the substrings grcli emits when an object is already
// absent (ENOENT), which for a delete is success.
var grcliMissingMarkers = []string{"No such file or directory", "ENOENT", "does not exist", "not found"}

// isGrcliMissingError reports whether grcli output indicates the object was
// already gone.
func isGrcliMissingError(out []byte) bool {
	s := strings.ToLower(string(out))
	for _, m := range grcliMissingMarkers {
		if strings.Contains(s, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

// grcliExistsMarkers are the substrings grout/grcli emit when an object already
// exists (EEXIST) so a full desired-state replay can be applied idempotently.
//
// Each marker is a whole phrase grout actually emits, never the bare word
// "exists": that would also match failures which are not EEXIST at all ("iface
// exists in another domain"), and tolerating one of those would leave the
// object configured the old way while the reconcile reports success.
var grcliExistsMarkers = []string{"File exists", "already exists", "EEXIST", "object exists"}

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

// applyFRRConfig applies the FRR control-plane config for the grout flavor: it
// rewrites /etc/frr/frr.conf and hot-reloads FRR only when the desired config
// differs from what is already on disk; an unchanged config is a no-op.
//
// A reload, not a restart: `frr-reload.py` was verified against grout 0.16.3
// with FRR 10.6.1 to keep the L3VNIs intact -- adding, removing and re-adding a
// `vrf`/`vni` stanza all leave `show vrf vni` Up with the right VXLAN interface,
// SVI and Router MAC -- and to leave the grout FIB entries the node programmed
// with grcli untouched. zebra rebuilds an L3VNI on a `vni` re-add from its own
// cached interface tables, which a reload does not disturb, exactly as on a
// kernel dataplane.
//
// Node FRR config only changes on VRF/VNI topology edits -- routed workload
// /32,/128 host routes live in grout's FIB (applied via grcli), not in FRR -- so
// this runs rarely to begin with.
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
	if err := frrManager.ReloadFRR(); err != nil {
		return fmt.Errorf("failed to reload FRR: %w", err)
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
	// An empty pool would still start a listener, but every client would be
	// rejected with an opaque handshake error; fail here instead.
	if !caCertPool.AppendCertsFromPEM(caCert) {
		log.Fatalf("No certificates found in CA certificate file %q", serverCert)
	}

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

// grcliTimeout bounds a single grcli invocation.
//
// Every call runs under applyMu, so a grcli that never returns does not just
// fail one request: it wedges the sidecar for good, and the node's datapath
// stops converging with no error anywhere. The control socket is local and
// every command we issue is a single object operation, so anything still
// running after this long is stuck rather than slow.
//
// Callers pass a context detached from the request (context.WithoutCancel): an
// apply that is abandoned half way through leaves grout in a partial state, so
// a client that hangs up must not cancel a batch that is already being applied.
// The timeout is what bounds the command, not the client.
const grcliTimeout = 30 * time.Second

// runGrcli runs one grcli command with a timeout and returns its combined
// output.
// runGrcli is a variable so tests can exercise the batch logic -- above all the
// skip that keeps a replay from re-adding an adopted tap -- without a grout.
var runGrcli = func(parent context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, grcliTimeout)
	defer cancel()
	//nolint:gosec // grcliPath is an operator-controlled flag; args are agent-generated.
	out, err := exec.CommandContext(ctx, grcliPath, args...).CombinedOutput()
	if err != nil && ctx.Err() != nil {
		return out, fmt.Errorf("grcli timed out after %s: %w", grcliTimeout, err)
	}
	//nolint:wrapcheck // callers add the command context; this is the raw exec error.
	return out, err
}
