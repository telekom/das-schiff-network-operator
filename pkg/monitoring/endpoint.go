package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	all          = "all"
	protocolIP   = "ip"
	protocolIPv4 = "ipv4"
	protocolIPv6 = "ipv6"

	StatusSvcNameEnv      = "STATUS_SVC_NAME"
	StatusSvcNamespaceEnv = "STATUS_SVC_NAMESPACE"
)

var validation = regexp.MustCompile("^[a-zA-Z0-9_]")

//go:generate mockgen -destination ./mock/mock_endpoint.go . FRRClient
type FRRClient interface {
	ExecuteWithJSON(args []string) []byte
}

type Endpoint struct {
	cli FRRClient
	c   client.Client

	statusSvcName      string
	statusSvcNamespace string
	logr.Logger
}

// NewEndpoint creates new endpoint object.
func NewEndpoint(k8sClient client.Client, frrcli FRRClient, svcName, svcNamespace string) *Endpoint {
	return &Endpoint{
		cli:                frrcli,
		c:                  k8sClient,
		statusSvcName:      svcName,
		statusSvcNamespace: svcNamespace,
		Logger:             log.Log.WithName("monitoring"),
	}
}

// CreateMux configures HTTP handlers.
func (e *Endpoint) CreateMux() *http.ServeMux {
	sm := http.NewServeMux()
	sm.HandleFunc("/show/route", e.ShowRoute)
	sm.HandleFunc("/show/bgp", e.ShowBGP)
	sm.HandleFunc("/show/evpn", e.ShowEVPN)
	sm.HandleFunc("/all/show/route", e.QueryAll)
	sm.HandleFunc("/all/show/bgp", e.QueryAll)
	sm.HandleFunc("/all/show/evpn", e.QueryAll)
	e.Logger.Info("created ServeMux")
	return sm
}

// ShowRoute returns result of show ip/ipv6 route command.
// show ip/ipv6 route (vrf <vrf>) <input> (longer-prefixes).
func (e *Endpoint) ShowRoute(w http.ResponseWriter, r *http.Request) {
	e.Logger.Info("got ShowRoute request")

	vrf := r.URL.Query().Get("vrf")
	if vrf == "" {
		vrf = all
	}

	if !validation.MatchString(vrf) {
		e.Logger.Error(fmt.Errorf("invalid VRF value"), "error validating value")
		http.Error(w, "invalid VRF value", http.StatusBadRequest)
		return
	}

	protocol := r.URL.Query().Get("protocol")
	if protocol == "" {
		protocol = protocolIP
	} else if protocol != protocolIP && protocol != protocolIPv6 {
		e.Logger.Error(fmt.Errorf("protocol '%s' is not supported", protocol), "protocol not supported")
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
		e.Logger.Error(err, "unable to set input")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := setLongerPrefixes(r, &command); err != nil {
		e.Logger.Error(err, "unable to set longer prefixes")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	e.Logger.Info("command to be executed", "command", command)

	data := e.cli.ExecuteWithJSON(command)

	result, err := withNodename(&data)
	if err != nil {
		e.Logger.Error(err, "unable to add nodename")
		http.Error(w, fmt.Sprintf("error adding nodename: %s", err.Error()), http.StatusBadRequest)
		return
	}

	e.writeResponse(result, w, "ShowRoute")
}

// ShowBGP returns a result of show bgp command.
// show bgp (vrf <vrf>) ipv4/ipv6 unicast <input> (longer-prefixes).
// show bgp vrf <all|vrf> summary.
func (e *Endpoint) ShowBGP(w http.ResponseWriter, r *http.Request) {
	e.Logger.Info("got ShowBGP request")
	vrf := r.URL.Query().Get("vrf")
	if vrf == "" {
		vrf = all
	}

	if !validation.MatchString(vrf) {
		e.Logger.Error(fmt.Errorf("invalid VRF value"), "error validating value")
		http.Error(w, "invalid VRF value", http.StatusBadRequest)
		return
	}

	command, err := prepareBGPCommand(r, vrf)
	if err != nil {
		e.Logger.Error(err, "error preparing ShowBGP command")
		http.Error(w, "error preparing ShowBGP command: "+err.Error(), http.StatusBadRequest)
		return
	}

	e.Logger.Info("command to be executed", "command", command)
	data := e.cli.ExecuteWithJSON(command)

	result, err := withNodename(&data)
	if err != nil {
		e.Logger.Error(err, "error adding nodename")
		http.Error(w, fmt.Sprintf("error adding nodename: %s", err.Error()), http.StatusBadRequest)
		return
	}

	e.writeResponse(result, w, "ShowBGP")
}

func prepareBGPCommand(r *http.Request, vrf string) ([]string, error) {
	var command []string
	requestType := r.URL.Query().Get("type")
	switch requestType {
	case "summary":
		command = []string{
			"show",
			"bgp",
			"vrf",
			vrf,
			"summary",
		}
	case "":
		protocol := r.URL.Query().Get("protocol")
		if protocol == "" || protocol == protocolIP {
			protocol = protocolIPv4
		} else if protocol != protocolIPv4 && protocol != protocolIPv6 {
			return nil, fmt.Errorf("protocol %s is not supported", protocol)
		}
		command = []string{
			"show",
			"bgp",
			"vrf",
			vrf,
			protocol,
			"unicast",
		}

		if err := setInput(r, &command); err != nil {
			return nil, fmt.Errorf("unable to set input: %w", err)
		}

		if err := setLongerPrefixes(r, &command); err != nil {
			return nil, fmt.Errorf("unable to set longer prefixes: %w", err)
		}
	default:
		return nil, fmt.Errorf("request of type '%s' is not supported", requestType)
	}

	return command, nil
}

// ShowEVPN returns result of show evpn command.
// show evpn vni json.
// show evpn rmac vni <all|vrf>.
// show evpn mac vni <all|vrf>.
// show evpn next-hops vni <all|vrf> json.
func (e *Endpoint) ShowEVPN(w http.ResponseWriter, r *http.Request) {
	e.Logger.Info("got ShowEVPN request")
	var command []string
	requestType := r.URL.Query().Get("type")
	switch requestType {
	case "":
		command = []string{
			"show",
			"evpn",
			"vni",
		}
	case "rmac", "mac", "next-hops":
		vni := r.URL.Query().Get("vni")
		if vni == "" {
			vni = all
		}

		if !validation.MatchString(vni) {
			e.Logger.Error(fmt.Errorf("invalid VNI value"), "error validating value")
			http.Error(w, "invalid VNI value", http.StatusBadRequest)
			return
		}

		command = []string{
			"show",
			"evpn",
			requestType,
			"vni",
			vni,
		}
	default:
		e.Logger.Error(fmt.Errorf("request of type '%s' is not supported", requestType), "request type not supported")
		http.Error(w, fmt.Sprintf("request of type '%s' is not supported", requestType), http.StatusBadRequest)
		return
	}

	e.Logger.Info("command to be executed", "command", command)
	data := e.cli.ExecuteWithJSON(command)

	result, err := withNodename(&data)
	if err != nil {
		e.Logger.Error(err, "error adding nodename")
		http.Error(w, fmt.Sprintf("error adding nodename: %s", err.Error()), http.StatusBadRequest)
		return
	}

	e.writeResponse(result, w, "ShowEVPN")
}

//+kubebuilder:rbac:groups=core,resources=services,verbs=get

// QueryAll - when called, will pass the request to all nodes and return their responses.
func (e *Endpoint) QueryAll(w http.ResponseWriter, r *http.Request) {
	e.Logger.Info("got QueryAll request")
	service := &corev1.Service{}
	err := e.c.Get(r.Context(), client.ObjectKey{Name: e.statusSvcName, Namespace: e.statusSvcNamespace}, service)
	if err != nil {
		e.Logger.Error(err, "error getting service")
		http.Error(w, fmt.Sprintf("error getting service: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	addr, err := e.getAddresses(r.Context(), service)
	if err != nil {
		e.Logger.Error(err, "error getting addresses")
		http.Error(w, fmt.Sprintf("error getting addresses: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	if len(addr) == 0 {
		e.Logger.Error(fmt.Errorf("addr slice length: %d", len(addr)), "error listing addresses: no addresses found")
		http.Error(w, "error listing addresses: no addresses found", http.StatusInternalServerError)
		return
	}

	e.Logger.Info("will querry endpoints", "endpoints", addr)
	response, errs := queryEndpoints(r, addr)
	if len(errs) > 0 {
		for _, err := range errs {
			e.Logger.Error(err, "error querying endpoint")
		}
		if len(errs) == 1 {
			http.Error(w, fmt.Sprintf("error querying endpoints - %s", errs[0].Error()), http.StatusInternalServerError)
			return
		}
		http.Error(w, "multiple errors occurred while querying endpoints - please check logs for the details", http.StatusInternalServerError)
		return
	}

	e.writeResponse(&response, w, "QueryAll")
}

func (e *Endpoint) writeResponse(data *[]byte, w http.ResponseWriter, requestType string) {
	_, err := w.Write(*data)
	if err != nil {
		http.Error(w, "failed to write response: "+err.Error(), http.StatusInternalServerError)
		return
	}
	e.Logger.Info("response written", "type", requestType)
}

func setLongerPrefixes(r *http.Request, command *[]string) error {
	longerPrefixes := r.URL.Query().Get("longer_prefixes")
	if longerPrefixes != "" {
		useLongerPrefixes, err := strconv.ParseBool(longerPrefixes)
		if err != nil {
			return fmt.Errorf("longer_prefixes value is not valid: %w", err)
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
			return fmt.Errorf("input value is not valid: %w", err)
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

func passRequest(r *http.Request, addr, query string, results chan []byte, errors chan error) {
	s := strings.Split(r.Host, ":")
	port := ""
	if len(s) > 1 {
		port = s[1]
	}

	protocol := "http"
	if r.TLS != nil {
		protocol = "https"
	}

	url := fmt.Sprintf("%s://%s:%s%s", protocol, addr, port, query)
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

//+kubebuilder:rbac:groups=core,resources=pods,verbs=list

func (e *Endpoint) getAddresses(ctx context.Context, svc *corev1.Service) ([]string, error) {
	var serviceLabels labels.Set = svc.Spec.Selector
	pods := &corev1.PodList{}
	if err := e.c.List(ctx, pods, &client.ListOptions{
		LabelSelector: serviceLabels.AsSelector(),
		Namespace:     svc.Namespace,
	}); err != nil {
		return nil, fmt.Errorf("error getting pods: %w", err)
	}

	addresses := []string{}
	for i := range pods.Items {
		addresses = append(addresses, pods.Items[i].Status.PodIP)
	}

	return addresses, nil
}

func queryEndpoints(r *http.Request, addr []string) ([]byte, []error) {
	query := strings.ReplaceAll(r.URL.RequestURI(), "all/", "")
	responses := []json.RawMessage{}

	var wg sync.WaitGroup
	results := make(chan []byte, len(addr))
	requestErrors := make(chan error, len(addr))

	for i := range addr {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			passRequest(r, addr[i], query, results, requestErrors)
		}(i)
	}

	wg.Wait()
	close(results)
	close(requestErrors)

	if len(requestErrors) > 0 {
		err := []error{}
		for e := range requestErrors {
			err = append(err, e)
		}
		return nil, err
	}

	for result := range results {
		responses = append(responses, json.RawMessage(result))
	}

	jsn, err := json.MarshalIndent(responses, "", "\t")
	if err != nil {
		return nil, []error{fmt.Errorf("error marshaling data: %w", err)}
	}

	return jsn, nil
}

// GetStatusServiceConfig gets status service's name and namespace from the environment.
func GetStatusServiceConfig() (name, namespace string, err error) {
	name = os.Getenv(StatusSvcNameEnv)
	if name == "" {
		err = fmt.Errorf("environment variable %s is not set", StatusSvcNameEnv)
		return name, namespace, err
	}

	namespace = os.Getenv(StatusSvcNamespaceEnv)
	if namespace == "" {
		err = fmt.Errorf("environment variable %s is not set", StatusSvcNamespaceEnv)
		return name, namespace, err
	}

	return name, namespace, err
}
