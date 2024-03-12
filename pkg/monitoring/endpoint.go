package monitoring

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	all          = "all"
	protocolIP   = "ip"
	protocolIPv4 = "ipv4"
	protocolIPv6 = "ipv6"
)

//go:generate mockgen -destination ./mock/mock_endpoint.go . FRRClient
type FRRClient interface {
	ExecuteWithJSON(args []string) []byte
}

type Endpoint struct {
	cli FRRClient
	c   client.Client
}

// NewEndpoint creates new endpoint object.
func NewEndpoint(k8sClient client.Client, frrcli FRRClient) *Endpoint {
	return &Endpoint{cli: frrcli, c: k8sClient}
}

// SetHandlers configures HTTP handlers.
func (e *Endpoint) SetHandlers() {
	http.HandleFunc("/show/route", e.ShowRoute)
	http.HandleFunc("/show/bgp", e.ShowBGP)
	http.HandleFunc("/show/evpn", e.ShowEVPN)
	http.HandleFunc("/all/show/route", e.PassRequest)
	http.HandleFunc("/all/show/bgp", e.PassRequest)
	http.HandleFunc("/all/show/evpn", e.PassRequest)
}

// ShowRoute returns result of show ip/ipv6 route command.
// show ip/ipv6 route (vrf <vrf>) <input> (longer-prefixes).
func (e *Endpoint) ShowRoute(w http.ResponseWriter, r *http.Request) {
	vrf := r.URL.Query().Get("vrf")
	if vrf == "" {
		vrf = all
	}

	protocol := r.URL.Query().Get("protocol")
	if protocol == "" {
		protocol = protocolIP
	} else if protocol != protocolIP && protocol != protocolIPv6 {
		http.Error(w, fmt.Sprintf("protocol '%s' is not supported", protocol), http.StatusBadRequest)
		return
	}

	command := []string{
		"show",
		protocol,
		"route",
		"vrf",
		vrf,
	}

	if err := setInput(r, &command); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := setLongerPrefixes(r, &command); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	data := e.cli.ExecuteWithJSON(command)

	result, err := withNodename(&data)
	if err != nil {
		http.Error(w, fmt.Sprintf("error adding nodename: %s", err.Error()), http.StatusBadRequest)
		return
	}

	writeResponse(result, w)
}

// ShowBGP returns a result of show bgp command.
// show bgp (vrf <vrf>) ipv4/ipv6 unicast <input> (longer-prefixes).
// show bgp vrf <all|vrf> summary.
func (e *Endpoint) ShowBGP(w http.ResponseWriter, r *http.Request) {
	vrf := r.URL.Query().Get("vrf")
	if vrf == "" {
		vrf = all
	}

	var data []byte
	requestType := r.URL.Query().Get("type")
	switch requestType {
	case "summary":
		data = e.cli.ExecuteWithJSON([]string{
			"show",
			"bgp",
			"vrf",
			vrf,
			"summary",
		})
	case "":
		protocol := r.URL.Query().Get("protocol")
		if protocol == "" || protocol == protocolIP {
			protocol = protocolIPv4
		} else if protocol != protocolIPv4 && protocol != protocolIPv6 {
			http.Error(w, fmt.Sprintf("protocol '%s' is not supported", protocol), http.StatusBadRequest)
			return
		}

		command := []string{
			"show",
			"bgp",
			"vrf",
			vrf,
			protocol,
			"unicast",
		}

		if err := setInput(r, &command); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := setLongerPrefixes(r, &command); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		data = e.cli.ExecuteWithJSON(command)
	default:
		http.Error(w, fmt.Sprintf("request of type '%s' is not supported", requestType), http.StatusBadRequest)
		return
	}

	result, err := withNodename(&data)
	if err != nil {
		http.Error(w, fmt.Sprintf("error adding nodename: %s", err.Error()), http.StatusBadRequest)
		return
	}

	writeResponse(result, w)
}

// ShowEVPN returns result of show evpn command.
// show evpn vni json.
// show evpn rmac vni <all|vrf>.
// show evpn mac vni <all|vrf>.
// show evpn next-hops vni <all|vrf> json.
func (e *Endpoint) ShowEVPN(w http.ResponseWriter, r *http.Request) {
	var data []byte
	requestType := r.URL.Query().Get("type")
	switch requestType {
	case "":
		data = e.cli.ExecuteWithJSON([]string{
			"show",
			"evpn",
			"vni",
		})
	case "rmac", "mac", "next-hops":
		vni := r.URL.Query().Get("vni")
		if vni == "" {
			vni = all
		}

		data = e.cli.ExecuteWithJSON([]string{
			"show",
			"evpn",
			requestType,
			"vni",
			vni,
		})
	default:
		http.Error(w, fmt.Sprintf("request of type '%s' is not supported", requestType), http.StatusBadRequest)
		return
	}

	result, err := withNodename(&data)
	if err != nil {
		http.Error(w, fmt.Sprintf("error adding nodename: %s", err.Error()), http.StatusBadRequest)
		return
	}

	writeResponse(result, w)
}

//+kubebuilder:rbac:groups=core,resources=pods,verbs=list

// PassRequest - when called, will pass the request to all nodes and return their responses.
func (e *Endpoint) PassRequest(w http.ResponseWriter, r *http.Request) {
	pods := &corev1.PodList{}
	matchLabels := &client.MatchingLabels{
		"app.kubernetes.io/name":      "network-operator",
		"app.kubernetes.io/component": "worker",
	}

	err := e.c.List(r.Context(), pods, matchLabels)
	if err != nil {
		http.Error(w, fmt.Sprintf("error listing pods: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	if len(pods.Items) == 0 {
		http.Error(w, "error listing pods: no pods found", http.StatusInternalServerError)
		return
	}

	query := strings.ReplaceAll(r.URL.String(), "all/", "")

	s := strings.Split(r.Host, ":")
	port := ""
	if len(s) > 1 {
		port = s[1]
	}

	responses := []json.RawMessage{}

	var wg sync.WaitGroup
	results := make(chan []byte, len(pods.Items))
	errors := make(chan error, len(pods.Items))

	for i := range pods.Items {
		wg.Add(1)
		go passToPod(&pods.Items[i], port, query, results, errors, &wg)
	}

	wg.Wait()
	close(results)
	close(errors)

	for err := range errors {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for result := range results {
		responses = append(responses, json.RawMessage(result))
	}

	jsn, err := json.MarshalIndent(responses, "", "\t")
	if err != nil {
		http.Error(w, "error marshalling data: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeResponse(&jsn, w)
}

func writeResponse(data *[]byte, w http.ResponseWriter) {
	_, err := w.Write(*data)
	if err != nil {
		http.Error(w, "failed to write response: "+err.Error(), http.StatusInternalServerError)
		return
	}
}

func setLongerPrefixes(r *http.Request, command *[]string) error {
	longerPrefixes := r.URL.Query().Get("longer_prefixes")
	if longerPrefixes != "" {
		useLongerPrefixes, err := strconv.ParseBool(longerPrefixes)
		if err != nil {
			return fmt.Errorf("longer_prefixes value '%s' is not valid: %w", longerPrefixes, err)
		}
		if useLongerPrefixes {
			*command = append(*command, "longer-prefixes")
		}
	}
	return nil
}

func setInput(r *http.Request, command *[]string) error {
	input := r.URL.Query().Get("input")
	if input != "" {
		if _, _, err := net.ParseCIDR(input); err != nil {
			return fmt.Errorf("input '%s' is not valid: %w", input, err)
		}
		*command = append(*command, input)
	}
	return nil
}

func withNodename(data *[]byte) (*[]byte, error) {
	res := map[string]json.RawMessage{}
	nodename := os.Getenv(healthcheck.NodenameEnv)

	result := data
	if nodename != "" {
		res[nodename] = json.RawMessage(*data)
		var err error
		*result, err = json.MarshalIndent(res, "", "\t")
		if err != nil {
			return nil, fmt.Errorf("error marshalling data: %w", err)
		}
	}

	return result, nil
}

func passToPod(pod *corev1.Pod, port, query string, results chan []byte, errors chan error, wg *sync.WaitGroup) {
	defer wg.Done()

	url := fmt.Sprintf("http://%s:%s%s", pod.Status.PodIP, port, query)
	resp, err := http.Get(url) //nolint
	if err != nil {
		errors <- fmt.Errorf("error getting data from %s: %w", pod.Status.PodIP, err)
		return
	}

	data, err := io.ReadAll(resp.Body)
	defer resp.Body.Close()
	if err != nil {
		errors <- fmt.Errorf("error reading response from %s: %w", pod.Status.PodIP, err)
		return
	}

	results <- data
}
