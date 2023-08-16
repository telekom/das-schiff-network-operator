package frr

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"regexp"
	"text/template"
)

// Template for VRF config
//
//go:embed tpl/vrf.tpl
var vrfRawTpl string

// Template for route-maps
//
//go:embed tpl/route-map.tpl
var routeMapRawTpl string

// Template for ip prefix-list
//
//go:embed tpl/prefix-list.tpl
var prefixListRawTpl string

// Template for bgp neighbor
//
//go:embed tpl/bgp-neighbor.tpl
var neighborRawTpl string

// Template for bgp v4 neighbor
//
//go:embed tpl/bgp-neighbor-v4.tpl
var neighborV4RawTpl string

// Template for bgp v4 neighbor
//
//go:embed tpl/bgp-neighbor-v6.tpl
var neighborV6RawTpl string

// Template for VRF BGP instance
//
//go:embed tpl/bgp.tpl
var bgpInstanceRawTpl string

var (
	vrfTpl         = mustParse("vrf", vrfRawTpl)
	routeMapTpl    = mustParse("route-map", routeMapRawTpl)
	prefixListTpl  = mustParse("prefix-list", prefixListRawTpl)
	neighborTpl    = mustParse("neighbor", neighborRawTpl)
	neighborV4Tpl  = mustParse("neighborv4", neighborV4RawTpl)
	neighborV6Tpl  = mustParse("neighborv6", neighborV6RawTpl)
	bgpInstanceTpl = mustParse("bgpinstance", bgpInstanceRawTpl)
)

type bgpInstanceConfig struct {
	VRFs     []VRFConfiguration
	RouterID string
	ASN      int
}

func mustParse(name, rawtpl string) *template.Template {
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
		return []byte{}, fmt.Errorf("error executing template: %w", err)
	}
	return buf.Bytes(), nil
}

func generateTemplateConfig(tplFile, original string) error {
	fileContent, err := os.ReadFile(original)
	if err != nil {
		return fmt.Errorf("error reading template file %s: %w", tplFile, err)
	}
	content := string(fileContent)

	commentToRemove := regexp.MustCompile(`(?m)^\#\+\+`)
	content = commentToRemove.ReplaceAllString(content, "")

	err = os.WriteFile(tplFile, []byte(content), frrPermissions)
	if err != nil {
		return fmt.Errorf("error writing template file %s: %w", tplFile, err)
	}

	return nil
}
