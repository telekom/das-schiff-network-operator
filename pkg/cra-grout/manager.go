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
)

// MetricsType selects a metrics subtree exposed by the grout-cra sidecar.
type MetricsType string

const (
	// MetricsGrout is the grout fast-path metrics subtree.
	MetricsGrout MetricsType = "grout"
	// MetricsFRR is the bundled-FRR metrics subtree.
	MetricsFRR MetricsType = "frr"
)

// Manager is the cra-grout control client. It renders nothing itself; the
// reconciler builds the FRR config and grcli batch and calls ApplyConfiguration,
// which POSTs both to the grout-cra sidecar over mTLS (mirroring cra-frr).
type Manager struct {
	craURLs []string
	client  http.Client
}

// NewManager builds a cra-grout Manager with an mTLS client trusting and
// presenting the shared CRA client certificate.
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

// ApplyConfiguration posts the rendered FRR config and grcli batch to the
// grout-cra sidecar.
func (m *Manager) ApplyConfiguration(ctx context.Context, frrConfig, grcliBatch string) error {
	craConfig := Configuration{
		FRRConfiguration: frrConfig,
		GrcliBatch:       grcliBatch,
	}
	jsonBody, err := json.Marshal(craConfig)
	if err != nil {
		return fmt.Errorf("error marshalling grout configuration: %w", err)
	}

	_, err = m.postRequest(ctx, "/grout/configuration", jsonBody)
	return err
}

// ExecuteGrcli runs an ad-hoc grcli command via the sidecar and returns its
// output, or nil when all CRA endpoints are unreachable.
func (m *Manager) ExecuteGrcli(args []string) []byte {
	command := strings.Join(args, " ")
	resBody, err := m.postRequest(context.Background(), "/grout/command", []byte(command))
	if err != nil {
		return nil
	}
	return resBody
}

func (m *Manager) postRequest(ctx context.Context, path string, body []byte) ([]byte, error) {
	for _, baseURL := range m.craURLs {
		url := fmt.Sprintf("%s%s", baseURL, path)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("error creating request: %w", err)
		}

		res, err := m.client.Do(req)
		if err != nil {
			// Try the next URL on a connection-level failure.
			continue
		}

		resBody, readErr := func() ([]byte, error) {
			defer res.Body.Close()
			return io.ReadAll(res.Body)
		}()
		if readErr != nil {
			return nil, fmt.Errorf("error reading response body: %w", readErr)
		}

		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status code (%d): %s", res.StatusCode, resBody)
		}

		return resBody, nil
	}

	return nil, fmt.Errorf("all CRA URLs failed due to connection issues")
}

// GetMetrics fetches a metrics subtree from the first reachable CRA endpoint.
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
