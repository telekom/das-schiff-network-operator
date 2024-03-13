package monitoring

import (
	"context"
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
	discoveryv1 "k8s.io/api/discovery/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	all          = "all"
	protocolIP   = "ip"
	protocolIPv4 = "ipv4"
	protocolIPv6 = "ipv6"

	defaultNamespace = "kube-system"
)

//go:generate mockgen -destination ./mock/mock_endpoint.go . FRRClient
type FRRClient interface {
	ExecuteWithJSON(args []string) []byte
}

type Endpoint struct {
	cli   FRRClient
	c     client.Client
	httpC http.Client
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

//+kubebuilder:rbac:groups=core,resources=services,verbs=get
//+kubebuilder:rbac:groups=core,resources=endpoints,verbs=get
//+kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=list

// PassRequest - when called, will pass the request to all nodes and return their responses.
func (e *Endpoint) PassRequest(w http.ResponseWriter, r *http.Request) {
	serviceName := r.URL.Query().Get("service")
	if serviceName == "" {
		http.Error(w, "error looking for service: service name not provided", http.StatusBadRequest)
		return
	}

	namespace := r.URL.Query().Get("namespace")
	if namespace == "" {
		namespace = defaultNamespace
	}

	service := &corev1.Service{}
	err := e.c.Get(r.Context(), client.ObjectKey{Name: serviceName, Namespace: namespace}, service)
	if err != nil {
		http.Error(w, fmt.Sprintf("error listing pods: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	addr, err := e.getAddresses(r.Context(), service)
	if err != nil {
		http.Error(w, fmt.Sprintf("error getting addresses: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	if len(addr) == 0 {
		http.Error(w, "error listing addresses: no addresses found", http.StatusInternalServerError)
		return
	}

	response, err := queryEndpoints(r, addr)
	if err != nil {
		http.Error(w, "error querying endpoints: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeResponse(&response, w)
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
			return nil, fmt.Errorf("error marshaling data: %w", err)
		}
	}

	return result, nil
}

func passRequest(r *http.Request, addr, query string, results chan []byte, errors chan error, wg *sync.WaitGroup) {
	defer wg.Done()

	s := strings.Split(r.Host, ":")
	port := ""
	if len(s) > 1 {
		port = s[1]
	}

	url := strings.Replace(query, r.Host, addr+":"+port, 1)

	// url := fmt.Sprintf("http://%s:%s%s", addr, port, query)
	resp, err := http.Get(url) //nolint
	if err != nil {
		errors <- fmt.Errorf("error getting data from %s: %w", addr, err)
		return
	}

	data, err := io.ReadAll(resp.Body)
	defer resp.Body.Close()
	if err != nil {
		errors <- fmt.Errorf("error reading response from %s: %w", addr, err)
		return
	}

	results <- data
}

func (e *Endpoint) getEndpointslices(ctx context.Context, svc *corev1.Service) (*discoveryv1.EndpointSlice, error) {
	endpoints := &discoveryv1.EndpointSliceList{}
	if err := e.c.List(ctx, endpoints); err != nil {
		return nil, fmt.Errorf("error listing endpointslices: %w", err)
	}

	for i := range endpoints.Items {
		for _, or := range endpoints.Items[i].OwnerReferences {
			if or.UID == svc.UID {
				return &endpoints.Items[i], nil
			}
		}
	}

	return nil, nil
}

func (e *Endpoint) getEndpoints(ctx context.Context, svc *corev1.Service) (*corev1.Endpoints, error) {
	endpoints := &corev1.Endpoints{}
	if err := e.c.Get(ctx, client.ObjectKeyFromObject(svc), endpoints); err != nil {
		return nil, fmt.Errorf("error getting endpoints: %w", err)
	}
	return endpoints, nil
}

func (e *Endpoint) getAddresses(ctx context.Context, svc *corev1.Service) ([]string, error) {
	es, err := e.getEndpointslices(ctx, svc)
	addresses := []string{}
	if err != nil {
		return nil, fmt.Errorf("error getting endpointslices: %w", err)
	}
	if es != nil {
		for _, ep := range es.Endpoints {
			addresses = append(addresses, ep.Addresses...)
		}
		return addresses, nil
	}
	ep, err := e.getEndpoints(ctx, svc)
	if err != nil {
		return nil, fmt.Errorf("error getting endpoints: %w", err)
	}
	for _, s := range ep.Subsets {
		for _, a := range s.Addresses {
			addresses = append(addresses, a.IP)
		}
	}
	return addresses, nil
}

func queryEndpoints(r *http.Request, addr []string) ([]byte, error) {
	query := strings.ReplaceAll(r.RequestURI, "all/", "")

	responses := []json.RawMessage{}

	var wg sync.WaitGroup
	results := make(chan []byte, len(addr))
	errors := make(chan error, len(addr))

	for i := range addr {
		wg.Add(1)
		go passRequest(r, addr[i], query, results, errors, &wg)
	}

	wg.Wait()
	close(results)
	close(errors)

	for err := range errors {
		return nil, fmt.Errorf("error occurred: %w", err)
	}

	for result := range results {
		responses = append(responses, json.RawMessage(result))
	}

	jsn, err := json.MarshalIndent(responses, "", "\t")
	if err != nil {
		return nil, fmt.Errorf("error marshaling data: %w", err)
	}

	return jsn, nil
}
