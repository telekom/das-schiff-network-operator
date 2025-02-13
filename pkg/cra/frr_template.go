package cra

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"strings"
	"text/template"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
)

const (
	recursionLimit = 1000
)

type FRRTemplate struct {
	FRRTemplatePath string
}

type frrTemplateData struct {
	Config     *config.BaseConfig
	NodeConfig *v1alpha1.NodeNetworkConfigSpec
}

func (tpl FRRTemplate) TemplateFRR(cfg *config.BaseConfig, nodeConfig *v1alpha1.NodeNetworkConfigSpec) (string, error) {
	frrTemplate, err := os.ReadFile(tpl.FRRTemplatePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %w", tpl.FRRTemplatePath, err)
	}

	data := frrTemplateData{
		Config:     cfg,
		NodeConfig: nodeConfig,
	}

	t := template.New("frr")
	recursionMap := make(map[string]int)

	tmpl, err := t.Funcs(template.FuncMap{
		"isIPv4": func(ip string) bool {
			parsedIP, _, err := net.ParseCIDR(ip)
			if err != nil {
				parsedIP = net.ParseIP(ip)
				if parsedIP == nil {
					return false
				}
				if parsedIP.To4() != nil {
					return true
				}
				return false
			}
			return parsedIP.To4() != nil
		},
		"containsKey": func(mm reflect.Value, key string) bool {
			if mm.Kind() != reflect.Map {
				return false
			}
			keys := mm.MapKeys()
			for _, k := range keys {
				if k.String() == key {
					return true
				}
			}
			return false
		},
		"include": func(name string, data interface{}) (string, error) {
			var buf strings.Builder
			if v, ok := recursionMap[name]; ok {
				if v > recursionLimit {
					return "", fmt.Errorf("recursion limit reached")
				}
				recursionMap[name]++
			} else {
				recursionMap[name] = 1
			}
			err := t.ExecuteTemplate(&buf, name, data)
			recursionMap[name]--
			return buf.String(), err
		},
		"hash": func(s string) (string, error) {
			hash := sha256.New()
			if _, err := hash.Write([]byte(s)); err != nil {
				return "", fmt.Errorf("failed to write hash: %w", err)
			}
			hashBytes := hash.Sum(nil)
			hashHex := hex.EncodeToString(hashBytes)
			return hashHex[:8], nil
		},
		"dict": func(values ...interface{}) (map[string]interface{}, error) {
			if len(values)%2 != 0 {
				return nil, errors.New("invalid dict call")
			}
			dict := make(map[string]interface{}, len(values)/2) //nolint:mnd
			for i := 0; i < len(values); i += 2 {
				key, ok := values[i].(string)
				if !ok {
					return nil, errors.New("dict keys must be strings")
				}
				dict[key] = values[i+1]
			}
			return dict, nil
		},
		"add": func(i, j int) int {
			return i + j
		},
		"join": strings.Join,
	}).Parse(string(frrTemplate))
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var result bytes.Buffer
	if err := tmpl.Execute(&result, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}
	return result.String(), nil
}
