package monitoring

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"

	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/healthcheck"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	all          = "all"
	protocolIP   = "ip"
	protocolIPv4 = "ipv4"
	protocolIPv6 = "ipv6"
)

type Endpoint struct {
	cli *frr.Cli
	c   client.Client
}

func NewEndpoint() (*Endpoint, error) {
	clientConfig := ctrl.GetConfigOrDie()
	c, err := client.New(clientConfig, client.Options{})
	if err != nil {
		return nil, fmt.Errorf("error creating controller-runtime client: %w", err)
	}
	return &Endpoint{cli: frr.NewCli(), c: c}, nil
}

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

	nodename := os.Getenv(healthcheck.NodenameEnv)
	nodeField := fmt.Sprintf("%s:\n", nodename)

	result := []byte{}

	if nodename != "" {
		result = append(result, []byte(nodeField)...)
	}

	result = append(result, data...)

	writeResponse(&result, w)
}

func (e *Endpoint) ShowAllRoute(w http.ResponseWriter, r *http.Request) {
	ctx := context.TODO()
	pods := &corev1.PodList{}
	matchLabels := &client.MatchingLabels{
		"app.kubernetes.io/component": "worker",
	}

	err := e.c.List(ctx, pods, matchLabels)
	if err != nil {
		http.Error(w, fmt.Sprintf("error listing pods: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	if len(pods.Items) == 0 {
		http.Error(w, "error listing pods: no pods found", http.StatusInternalServerError)
		return
	}

	for _, pod := range pods.Items {
		fmt.Println(pod.Status.PodIP)
		url := fmt.Sprintf("http://%s/show/route?%s", pod.Status.PodIP, r.URL.RawQuery)
		fmt.Println(url)
		resp, err := http.Get(url)
		if err != nil {
			log.Fatalln(err)
		}

		data := []byte{}
		resp.Body.Read(data)
		resp.Body.Close()
		writeResponse(&data, w)
	}
}

func (e *Endpoint) ShowBGP(w http.ResponseWriter, r *http.Request) {
	vrf := r.URL.Query().Get("vrf")
	if vrf == "" {
		vrf = all
	}

	data := []byte{}

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
		if protocol == "" {
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

	writeResponse(&data, w)
}

func (e *Endpoint) ShowEVPN(w http.ResponseWriter, r *http.Request) {
	data := []byte{}
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

	writeResponse(&data, w)
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
