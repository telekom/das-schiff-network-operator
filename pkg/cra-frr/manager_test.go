package cra

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPostRequestRetriesWithFreshBody(t *testing.T) {
	t.Parallel()

	const requestBody = `{"frr":"router bgp 65000"}`

	failedAttemptBody := make(chan string, 1)
	successfulAttemptBody := make(chan string, 1)

	failingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read failing server request body: %v", err)
		}
		failedAttemptBody <- string(body)

		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("response writer does not support hijacking")
			return
		}

		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack response connection: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer failingServer.Close()

	successfulServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read successful server request body: %v", err)
		}
		successfulAttemptBody <- string(body)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer successfulServer.Close()

	manager := &Manager{
		craURLs: []string{failingServer.URL, successfulServer.URL},
		client:  http.Client{Timeout: time.Second},
	}

	responseBody, err := manager.postRequest(context.Background(), "/frr/configuration", []byte(requestBody))
	if err != nil {
		t.Fatalf("post request: %v", err)
	}

	if string(responseBody) != "ok" {
		t.Fatalf("response body = %q, want %q", responseBody, "ok")
	}

	assertRequestBody(t, "failed attempt", failedAttemptBody, requestBody)
	assertRequestBody(t, "successful attempt", successfulAttemptBody, requestBody)
}

func assertRequestBody(t *testing.T, attempt string, bodies <-chan string, want string) {
	t.Helper()

	select {
	case got := <-bodies:
		if got == "" {
			t.Fatalf("%s body is empty", attempt)
		}
		if got != want {
			t.Fatalf("%s body = %q, want %q", attempt, got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s body", attempt)
	}
}
