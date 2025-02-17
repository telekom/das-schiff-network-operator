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

	"github.com/telekom/das-schiff-network-operator/pkg/nl"
)

type Manager struct {
	craURL string
	client http.Client
}

func NewManager(craURL, clientCert, clientKey string) (*Manager, error) {
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
		craURL: craURL,
		client: http.Client{
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
	// Send configuration to CRA via HTTP
	url := fmt.Sprintf("%s%s", m.craURL, path)

	bodyReader := bytes.NewReader(body)

	req, err := http.NewRequest(http.MethodPost, url, bodyReader)
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}
	res, err := m.client.Do(req.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("error sending request: %w", err)
	}
	defer res.Body.Close()

	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("error reading response body: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code (%d): %s", res.StatusCode, resBody)
	}
	return nil
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
