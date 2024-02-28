package monitoring

import (
	"net/http"
	"strings"

	"github.com/telekom/das-schiff-network-operator/pkg/frr"
)

const (
	vrfAll = "all"
)

type Endpoint struct {
	cli *frr.Cli
}

func NewEndpoint() *Endpoint {
	return &Endpoint{cli: frr.NewCli()}
}

func writeResponse(data *[]byte, w http.ResponseWriter) {
	_, err := w.Write(*data)
	if err != nil {
		http.Error(w, "failed to write response: "+err.Error(), http.StatusInternalServerError)
		return
	}
}

func (e *Endpoint) ShowRoute(w http.ResponseWriter, r *http.Request) {
	vrf := r.URL.Query().Get("vrf")
	if vrf == "" {
		vrf = vrfAll
	}

	protocol := r.URL.Query().Get("protocol")
	if protocol == "" {
		protocol = "ip"
	}

	data := e.cli.ExecuteWithJSON([]string{
		"show",
		protocol,
		"route",
		"vrf",
		vrf,
	})

	writeResponse(&data, w)
}

func (e *Endpoint) ShowBGP(w http.ResponseWriter, r *http.Request) {
	vrf := r.URL.Query().Get("vrf")
	if vrf == "" {
		vrf = vrfAll
	}

	data := []byte{}

	requestType := r.URL.Query().Get("type")
	if strings.EqualFold(requestType, "summary") {
		data = e.cli.ExecuteWithJSON([]string{
			"show",
			"bgp",
			"vrf",
			vrf,
			"summary",
		})
	} else {
		protocol := r.URL.Query().Get("protocol")
		if protocol == "" {
			protocol = "ipv4"
		}

		data = e.cli.ExecuteWithJSON([]string{
			"show",
			"bgp",
			"vrf",
			vrf,
			protocol,
			"unicast",
		})
	}

	writeResponse(&data, w)
}

func (e *Endpoint) ShowEVPN(w http.ResponseWriter, r *http.Request) {
	data := []byte{}
	requestType := r.URL.Query().Get("type")
	if requestType == "" {
		data = e.cli.ExecuteWithJSON([]string{
			"show",
			"evpn",
			"vni",
		})
	} else {
		vrf := r.URL.Query().Get("vrf")
		if vrf == "" {
			vrf = vrfAll
		}

		data = e.cli.ExecuteWithJSON([]string{
			"show",
			"evpn",
			requestType,
			"vni",
			vrf,
		})
	}

	writeResponse(&data, w)
}
