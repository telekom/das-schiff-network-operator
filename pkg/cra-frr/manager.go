package cra

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/telekom/das-schiff-network-operator/pkg/nl"
)

type MetricsType string

const (
	MetricsFRR          MetricsType = "frr"
	MetricsNodeExporter MetricsType = "node-exporter"
)

type Manager struct {
	craURLs []string
	client  http.Client
}

func NewManager(craURLs []string, timeout time.Duration, clientCert, clientKey string) (*Manager, error) {
	clientCertData, err := os.ReadFile(clientCert)
	if err != nil {
		return nil, fmt.Errorf("error reading client cert file: %w", err)
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(clientCertData)

	cert, err := tls.LoadX509KeyPair(clientCert, clientKey)
	if err != nil {
		return nil, fmt.Errorf("error loading client cert and key: %w", err)
	}

	return &Manager{
		craURLs: craURLs,
		client: http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					RootCAs:      caCertPool,
					Certificates: []tls.Certificate{cert},
					MinVersion:   tls.VersionTLS12,
				},
			},
		},
	}, nil
}

func (m *Manager) postRequest(ctx context.Context, path string, body []byte) ([]byte, error) {
	bodyReader := bytes.NewReader(body)

	for _, baseURL := range m.craURLs {
		url := fmt.Sprintf("%s%s", baseURL, path)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
		if err != nil {
			return nil, fmt.Errorf("error creating request: %w", err)
		}

		res, err := m.client.Do(req)
		if err != nil {
			// Continue to the next URL if there is a connection issue
			continue
		}

		// Ensure the response body is closed after processing
		resBody, readErr := func() ([]byte, error) {
			defer res.Body.Close()
			return io.ReadAll(res.Body)
		}()
		if readErr != nil {
			return nil, fmt.Errorf("error reading response body: %w", readErr)
		}

		// Fail directly if a response is received, regardless of status code
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status code (%d): %s", res.StatusCode, resBody)
		}

		// Success, return nil
		return resBody, nil
	}

	// If all URLs fail due to connection issues
	return nil, fmt.Errorf("all CRA URLs failed due to connection issues")
}

func (m *Manager) ApplyConfiguration(ctx context.Context, netlinkConfig *nl.NetlinkConfiguration, frrConfig string) error {
	craConfig := Configuration{
		NetlinkConfiguration: *netlinkConfig,
		FRRConfiguration:     frrConfig,
	}
	jsonBody, err := json.Marshal(craConfig)
	if err != nil {
		return fmt.Errorf("error marshalling netlink configuration: %w", err)
	}

	_, err = m.postRequest(ctx, "/frr/configuration", jsonBody)
	return err
}

func (m *Manager) ExecuteWithJSON(args []string) []byte {
	command := strings.Join(args, " ")

	resBody, err := m.postRequest(context.Background(), "/frr/command", []byte(command))
	if err != nil {
		return nil
	}

	return resBody
}

func (m *Manager) GetMetrics(ctx context.Context, metricsType MetricsType) ([]byte, error) {
	for _, baseURL := range m.craURLs {
		url := fmt.Sprintf("%s/%s/metrics", baseURL, metricsType)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
		if err != nil {
			return nil, fmt.Errorf("error creating request: %w", err)
		}

		res, err := m.client.Do(req)
		if err != nil {
			continue
		}

		resBody, readErr := func() ([]byte, error) {
			defer res.Body.Close()
			return io.ReadAll(res.Body)
		}()
		if readErr != nil {
			return nil, fmt.Errorf("error reading response body: %w", readErr)
		}

		return resBody, nil
	}

	return nil, fmt.Errorf("all CRA URLs failed due to connection issues")
}
