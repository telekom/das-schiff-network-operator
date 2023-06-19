package frr

import (
	_ "embed"

	"bytes"
	"io/ioutil"
	"regexp"
	"text/template"
)

// Template for VRF config
//go:embed tpl/vrf.tpl
var VRF_RAW_TPL string

// Template for route-maps
//go:embed tpl/route-map.tpl
var ROUTE_MAP_RAW_TPL string

// Template for ip prefix-list
//go:embed tpl/prefix-list.tpl
var PREFIX_LIST_RAW_TPL string

// Template for bgp neighbor
//go:embed tpl/bgp-neighbor.tpl
var NEIGHBOR_RAW_TPL string

// Template for bgp v4 neighbor
//go:embed tpl/bgp-neighbor-v4.tpl
var NEIGHBOR_V4_RAW_TPL string

// Template for bgp v4 neighbor
//go:embed tpl/bgp-neighbor-v6.tpl
var NEIGHBOR_V6_RAW_TPL string

// Template for VRF BGP instance
//go:embed tpl/bgp.tpl
var BGP_INSTANCE_RAW_TPL string

var (
	VRF_TPL          = mustParse("vrf", VRF_RAW_TPL)
	ROUTE_MAP_TPL    = mustParse("route-map", ROUTE_MAP_RAW_TPL)
	PREFIX_LIST_TPL  = mustParse("prefix-list", PREFIX_LIST_RAW_TPL)
	NEIGHBOR_TPL     = mustParse("neighbor", NEIGHBOR_RAW_TPL)
	NEIGHBOR_V4_TPL  = mustParse("neighborv4", NEIGHBOR_V4_RAW_TPL)
	NEIGHBOR_V6_TPL  = mustParse("neighborv6", NEIGHBOR_V6_RAW_TPL)
	BGP_INSTANCE_TPL = mustParse("bgpinstance", BGP_INSTANCE_RAW_TPL)
)

type bgpInstanceConfig struct {
	VRFs     []VRFConfiguration
	RouterID string
	ASN      int
}

func mustParse(name string, rawtpl string) *template.Template {
	tpl, err := template.New(name).Parse(rawtpl)
	if err != nil {
		panic(err)
	}
	return tpl
}

func render(tpl *template.Template, vrfs interface{}) ([]byte, error) {
	buf := bytes.Buffer{}
	err := tpl.Execute(&buf, vrfs)
	if err != nil {
		return []byte{}, err
	}
	return buf.Bytes(), nil
}

func generateTemplateConfig(template string, original string) error {
	bytes, err := ioutil.ReadFile(original)
	if err != nil {
		return err
	}
	content := string(bytes)

	commentToRemove := regexp.MustCompile(`(?m)^\#\+\+`)
	content = commentToRemove.ReplaceAllString(content, "")

	err = ioutil.WriteFile(template, []byte(content), FRR_PERMISSIONS)
	if err != nil {
		return err
	}

	return nil
}
