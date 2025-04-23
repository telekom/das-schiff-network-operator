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
	"time"

	"github.com/telekom/das-schiff-network-operator/pkg/nl"
)

type Manager struct {
	craURLs []string
	client  http.Client
}

func NewManager(craURLs []string, clientCert, clientKey string) (*Manager, error) {
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
			Timeout: 30 * time.Second,
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

func (m Manager) postRequest(ctx context.Context, path string, body []byte) error {
	bodyReader := bytes.NewReader(body)

	for _, baseURL := range m.craURLs {
		url := fmt.Sprintf("%s%s", baseURL, path)

		req, err := http.NewRequest(http.MethodPost, url, bodyReader)
		if err != nil {
			return fmt.Errorf("error creating request: %w", err)
		}

		res, err := m.client.Do(req.WithContext(ctx))
		if err != nil {
			// Continue to the next URL if there is a connection issue
			continue
		}
		defer res.Body.Close()

		// Read the response body
		resBody, err := io.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("error reading response body: %w", err)
		}

		// Fail directly if a response is received, regardless of status code
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("unexpected status code (%d): %s", res.StatusCode, resBody)
		}

		// Success, return nil
		return nil
	}

	// If all URLs fail due to connection issues
	return fmt.Errorf("all CRA URLs failed due to connection issues")
}

func (m Manager) ApplyConfiguration(ctx context.Context, netlinkConfig *nl.NetlinkConfiguration, frrConfig string) error {
	craConfig := Configuration{
		NetlinkConfiguration: *netlinkConfig,
		FRRConfiguration:     frrConfig,
	}
	jsonBody, err := json.Marshal(craConfig)
	if err != nil {
		return fmt.Errorf("error marshalling netlink configuration: %w", err)
	}

	return m.postRequest(ctx, "/config", jsonBody)
}
