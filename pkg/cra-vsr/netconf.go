/*
Copyright 2025.

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

package cra

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"nemith.io/netconf"
	"nemith.io/netconf/rpc"
	ncssh "nemith.io/netconf/transport/ssh"
)

type Datastore string

const (
	Running     Datastore = "running"
	Candidate   Datastore = "candidate"
	Startup     Datastore = "startup"
	Operational Datastore = "operational"
)

type Operation string

const (
	Merge   Operation = "merge"
	Replace Operation = "replace"
	None    Operation = "none"
)

type GetData struct {
	XMLName            xml.Name  `xml:"urn:ietf:params:xml:ns:yang:ietf-netconf-nmda get-data"`
	XmlnsDatastoreAttr string    `xml:"xmlns:ds,attr"`
	Datastore          Datastore `xml:"datastore"`
	Filter             string    `xml:"xpath-filter,omitempty"`
}

//nolint:revive
type GetDataReply struct {
	netconf.RPCReply
	Data struct {
		Payload []byte `xml:",innerxml"`
	} `xml:"data"`
}

type EditData struct {
	XMLName            xml.Name  `xml:"urn:ietf:params:xml:ns:yang:ietf-netconf-nmda edit-data"`
	XmlnsBaseAttr      string    `xml:"xmlns:nc,attr"`
	XmlnsDatastoreAttr string    `xml:"xmlns:ds,attr"`
	Datastore          Datastore `xml:"datastore"`
	Operation          Operation `xml:"default-operation,omitempty"`
	Data               any       `xml:"config,omitempty"`
}

type RPCReply struct {
	netconf.RPCReply
	Refresh []byte `xml:"refresh-rpc,omitempty"`
	Status  []byte `xml:"status-rpc,omitempty"`
	Stop    []byte `xml:"stop-command,omitempty"`
	Exit    *int   `xml:"exit-code,omitempty"`
	Body    []byte `xml:",innerxml"`
}

type Netconf struct {
	logger    logr.Logger
	session   *netconf.Session
	timeout   time.Duration
	sshConfig *ssh.ClientConfig
	urls      []string
}

// NewNetconfWithKnownHosts creates a NETCONF client that authenticates with auth
// and verifies CRA host keys against the supplied known_hosts file.
func NewNetconfWithKnownHosts(
	urls []string,
	user string,
	auth ssh.AuthMethod,
	knownHostsPath string,
	timeout time.Duration,
	logger logr.Logger,
) (*Netconf, error) {
	if auth == nil {
		return nil, errors.New("no CRA SSH auth method provided")
	}
	normalizedURLs := normalizeCRAURLs(urls)
	hostKeyCallback, err := validateKnownHostsEntries(knownHostsPath, normalizedURLs)
	if err != nil {
		return nil, err
	}

	return buildNetconf(normalizedURLs, user, auth, timeout, hostKeyCallback, logger), nil
}

// SSH algorithms the NETCONF client accepts (CORE 3.04 / CORE 3.50). The Go
// defaults for ciphers already contain only AEAD and CTR modes and stay in place.
var (
	sshKeyExchanges = []string{
		ssh.KeyExchangeCurve25519,
		ssh.KeyExchangeECDHP521,
		ssh.KeyExchangeECDHP384,
		ssh.KeyExchangeECDHP256,
		ssh.KeyExchangeDH16SHA512,
	}
	sshMACs = []string{
		ssh.HMACSHA512ETM,
		ssh.HMACSHA256ETM,
		ssh.HMACSHA512,
		ssh.HMACSHA256,
	}
	sshHostKeyAlgorithms = []string{
		ssh.KeyAlgoED25519,
		ssh.KeyAlgoECDSA521,
		ssh.KeyAlgoECDSA384,
		ssh.KeyAlgoECDSA256,
		ssh.KeyAlgoRSASHA512,
		ssh.KeyAlgoRSASHA256,
	}
)

// minRSAHostKeyBits is the smallest RSA host key the client accepts.
const minRSAHostKeyBits = 3072

func buildNetconf(
	urls []string,
	user string,
	auth ssh.AuthMethod,
	timeout time.Duration,
	hostKeyCallback ssh.HostKeyCallback,
	logger logr.Logger,
) *Netconf {
	return &Netconf{
		logger:  logger,
		urls:    normalizeCRAURLs(urls),
		timeout: timeout,
		sshConfig: &ssh.ClientConfig{
			Config: ssh.Config{
				KeyExchanges: sshKeyExchanges,
				MACs:         sshMACs,
			},
			User:              user,
			Auth:              []ssh.AuthMethod{auth},
			HostKeyCallback:   rejectWeakRSAHostKey(hostKeyCallback),
			HostKeyAlgorithms: sshHostKeyAlgorithms,
		},
	}
}

// rejectWeakRSAHostKey wraps next and refuses RSA host keys shorter than
// minRSAHostKeyBits before any credential is sent.
func rejectWeakRSAHostKey(next ssh.HostKeyCallback) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if cryptoKey, ok := key.(ssh.CryptoPublicKey); ok {
			if rsaKey, ok := cryptoKey.CryptoPublicKey().(*rsa.PublicKey); ok && rsaKey.N.BitLen() < minRSAHostKeyBits {
				return fmt.Errorf("host key of %s: RSA key has %d bits, need at least %d", hostname, rsaKey.N.BitLen(), minRSAHostKeyBits)
			}
		}
		return next(hostname, remote, key)
	}
}

// AuthFromFiles returns public-key authentication with the private key in
// keyFile or, when keyFile is empty, password authentication with the content
// of passwordFile without trailing newlines. The file must not be accessible beyond mode 0600.
func AuthFromFiles(keyFile, passwordFile string) (ssh.AuthMethod, error) {
	switch {
	case keyFile != "":
		data, err := readCredentialFile(keyFile)
		if err != nil {
			return nil, err
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			var passphraseErr *ssh.PassphraseMissingError
			if errors.As(err, &passphraseErr) {
				return nil, fmt.Errorf("private key file %q is encrypted; the agent needs an unencrypted key", keyFile)
			}
			return nil, fmt.Errorf("parse private key file %q: %w", keyFile, err)
		}
		return ssh.PublicKeys(signer), nil
	case passwordFile != "":
		data, err := readCredentialFile(passwordFile)
		if err != nil {
			return nil, err
		}
		password := strings.TrimRight(string(data), "\r\n")
		if password == "" {
			return nil, fmt.Errorf("password file %q is empty", passwordFile)
		}
		return ssh.Password(password), nil
	default:
		return nil, errors.New("no private key file or password file provided")
	}
}

// readCredentialFile checks the mode and reads the file through the same file
// descriptor, so the file cannot be swapped between check and read.
func readCredentialFile(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // path is operator configuration
	if err != nil {
		return nil, fmt.Errorf("credential file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("credential file: %w", err)
	}
	if perm := info.Mode().Perm(); perm&^0o600 != 0 {
		return nil, fmt.Errorf("credential file %q has mode %04o, must not be wider than 0600", path, perm)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("credential file: %w", err)
	}
	return data, nil
}

func normalizeCRAURLs(urls []string) []string {
	normalized := make([]string, 0, len(urls))
	for _, rawURL := range urls {
		url := strings.TrimSpace(rawURL)
		if url == "" {
			continue
		}
		normalized = append(normalized, url)
	}
	return normalized
}

func validateKnownHostsEntries(knownHostsPath string, urls []string) (ssh.HostKeyCallback, error) {
	knownHostsPath = strings.TrimSpace(knownHostsPath)
	if knownHostsPath == "" {
		return nil, fmt.Errorf("known hosts file path is required")
	}

	normalizedURLs := normalizeCRAURLs(urls)
	if len(normalizedURLs) == 0 {
		return nil, fmt.Errorf("no CRA URLs provided")
	}

	hostKeyCallback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load known hosts file %q: %w", knownHostsPath, err)
	}

	validationKey, err := newKnownHostsValidationKey()
	if err != nil {
		return nil, err
	}

	missing := []string{}
	for _, url := range normalizedURLs {
		if _, _, err := net.SplitHostPort(url); err != nil {
			return nil, fmt.Errorf("CRA URL %q must be in host:port form: %w", url, err)
		}

		hasEntry, err := knownHostsHasEntry(hostKeyCallback, url, validationKey)
		if err != nil {
			return nil, fmt.Errorf("validate known hosts entry for CRA URL %q in file %q: %w", url, knownHostsPath, err)
		}
		if !hasEntry {
			missing = append(missing, knownhosts.Normalize(url))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("known hosts file %q does not contain CRA URL entries: %s", knownHostsPath, strings.Join(missing, ", "))
	}

	return hostKeyCallback, nil
}

func newKnownHostsValidationKey() (ssh.PublicKey, error) {
	publicKey := make(ed25519.PublicKey, ed25519.PublicKeySize)
	for i := range publicKey {
		publicKey[i] = byte(i + 1)
	}
	sshKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		return nil, fmt.Errorf("create validation SSH key: %w", err)
	}
	return sshKey, nil
}

func knownHostsHasEntry(hostKeyCallback ssh.HostKeyCallback, address string, validationKey ssh.PublicKey) (bool, error) {
	const (
		validationCallbackLoopback = "127.0.0.1"
		validationCallbackPort     = 22
	)

	callbackAddress := knownHostsCallbackAddress(address)
	err := hostKeyCallback(callbackAddress, &net.TCPAddr{IP: net.ParseIP(validationCallbackLoopback), Port: validationCallbackPort}, validationKey)
	if err == nil {
		return true, nil
	}

	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		return len(keyErr.Want) > 0, nil
	}

	return false, err
}

func knownHostsCallbackAddress(address string) string {
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address
	}

	normalized := knownhosts.Normalize(address)
	if _, _, err := net.SplitHostPort(normalized); err == nil {
		return normalized
	}

	return net.JoinHostPort(strings.Trim(normalized, "[]"), "22")
}

func (nc *Netconf) Open(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, nc.timeout)
	defer cancel()

	if nc.session != nil {
		nc.session.Close(ctx)
		nc.session = nil
	}

	var dialErrs []error
	for _, url := range nc.urls {
		transport, err := ncssh.Dial(ctx, "tcp", url, nc.sshConfig)
		if err != nil {
			nc.logger.Error(err, "failed to connect to CRA", "url", url)
			dialErrs = append(dialErrs, fmt.Errorf("%s: %w", url, err))
			continue
		}

		nc.session, err = netconf.NewSession(transport)
		if err != nil {
			return fmt.Errorf("failed to open netconf session on %s: %w", url, err)
		}

		return nil
	}

	return fmt.Errorf("all CRA URLs failed due to connection issues: %w", errors.Join(dialErrs...))
}

func (nc *Netconf) Send(ctx context.Context, req, rep any) error {
	if nc.session == nil {
		if err := nc.Open(ctx); err != nil {
			return fmt.Errorf("failed to open netconf session: %w", err)
		}
	}

	for i := 0; i < 2; i++ {
		var err error

		subctx, cancel := context.WithTimeout(ctx, nc.timeout)
		err = nc.session.Exec(subctx, req, rep)
		cancel()

		if err == nil {
			return nil
		}

		if !errors.Is(err, io.EOF) && !errors.Is(err, netconf.ErrClosed) {
			return fmt.Errorf("netconf request failed with: %w", err)
		}

		if i == 0 {
			if err := nc.Open(ctx); err != nil {
				return fmt.Errorf("failed to re-open netconf session: %w", err)
			}
		}
	}

	if nc.session != nil {
		nc.session.Close(ctx)
		nc.session = nil
	}

	return fmt.Errorf("all netconf request sending attempts failed")
}

func (nc *Netconf) Get(ctx context.Context, ds Datastore, filter string) ([]byte, error) {
	filter = strings.Join(strings.Fields(filter), " ")
	req := GetData{
		XmlnsDatastoreAttr: "urn:ietf:params:xml:ns:yang:ietf-datastores",
		Datastore:          "ds:" + ds,
		Filter:             filter,
	}

	var rep GetDataReply
	if err := nc.Send(ctx, &req, &rep); err != nil {
		return []byte{}, fmt.Errorf("failed to get netconf ds=%s filter=%s: %w", ds, filter, err)
	}

	return rep.Data.Payload, nil
}

func (nc *Netconf) GetUnmarshal(ctx context.Context, ds Datastore, filter string, out any) error {
	data, err := nc.Get(ctx, ds, filter)
	if err != nil {
		return err
	}

	if err := xml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("failed to un-marshal netconf data=%s: %w", data, err)
	}

	return nil
}

func (nc *Netconf) Edit(
	ctx context.Context,
	ds Datastore,
	op Operation,
	data any,
) error {
	req := EditData{
		XmlnsBaseAttr:      "urn:ietf:params:xml:ns:netconf:base:1.0",
		XmlnsDatastoreAttr: "urn:ietf:params:xml:ns:yang:ietf-datastores",
		Datastore:          "ds:" + ds,
		Operation:          op,
	}

	switch v := data.(type) {
	case string:
		req.Data = struct {
			Inner []byte `xml:",innerxml"`
		}{
			Inner: []byte(v),
		}
	case []byte:
		req.Data = struct {
			Inner []byte `xml:",innerxml"`
		}{
			Inner: v,
		}
	default:
		req.Data = struct {
			Inner any `xml:"config"`
		}{
			Inner: data,
		}
	}

	var rep rpc.OkReply
	if err := nc.Send(ctx, &req, &rep); err != nil {
		return fmt.Errorf("failed to edit netconf: %w", err)
	}

	return nil
}

func (nc *Netconf) Commit(ctx context.Context) error {
	var rep rpc.OkReply
	var req rpc.Commit

	if err := nc.Send(ctx, &req, &rep); err != nil {
		return fmt.Errorf("failed to commit netconf: %w", err)
	}

	return nil
}

func (nc *Netconf) RPC(ctx context.Context, req, out any) error {
	var rep RPCReply
	if err := nc.Send(ctx, req, &rep); err != nil {
		return fmt.Errorf("failed to send netconf rpc: %w", err)
	}

	buf := bytes.Buffer{}
	buf.WriteString("<wrap>")
	buf.Write(rep.Body)

	refresh := rep.Refresh
	status := rep.Status
	isLong := len(refresh) > 0 && len(status) > 0

	//nolint:mnd
	for isLong && rep.Exit != nil {
		time.Sleep(50 * time.Millisecond)

		rep = RPCReply{}
		if err := nc.Send(ctx, &refresh, &rep); err != nil {
			return fmt.Errorf("failed to refresh netconf rpc: %w", err)
		}

		rep = RPCReply{}
		if err := nc.Send(ctx, &status, &rep); err != nil {
			return fmt.Errorf("failed to get status of netconf rpc: %w", err)
		}

		buf.Write(rep.Body)
	}

	buf.WriteString("</wrap>")

	if err := xml.Unmarshal(buf.Bytes(), out); err != nil {
		return fmt.Errorf("failed to unmarshal netconf rpc: %w", err)
	}

	return nil
}
