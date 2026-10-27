// Package main implements a healthcheck probe querying the Vaultwarden liveness endpoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultEndpoint = "http://127.0.0.1:8080/alive"
	probeEnvVar     = "VAULTWARDEN_PROBE_URL"
	defaultTimeout  = 2 * time.Second
)

var (
	errUnsupportedScheme = errors.New("unsupported probe protocol scheme")
	errUnhealthyStatus   = errors.New("unexpected probe status code")
)

func defaultGetTransport() http.RoundTripper {
	return http.DefaultTransport
}

func defaultCreateRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("creating HTTP request: %w", err)
	}
	return req, nil
}

var (
	createRequest = defaultCreateRequest
	exitFunc      = os.Exit
	getTransport  = defaultGetTransport
)

func defaultDoRequest(ctx context.Context, endpoint string) (err error) {
	parsedURL, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parsing probe endpoint: %w", err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("%w: %s", errUnsupportedScheme, parsedURL.Scheme)
	}

	req, err := createRequest(ctx, http.MethodGet, parsedURL.String(), http.NoBody)
	if err != nil {
		return fmt.Errorf("creating probe request: %w", err)
	}

	client := &http.Client{
		Timeout:   defaultTimeout,
		Transport: getTransport(),
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("executing probe request: %w", err)
	}
	defer func() {
		_, copyErr := io.Copy(io.Discard, resp.Body)
		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("closing response body: %w", closeErr)
		} else if copyErr != nil && err == nil {
			err = fmt.Errorf("draining response body: %w", copyErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %d", errUnhealthyStatus, resp.StatusCode)
	}

	return nil
}

var doRequest = defaultDoRequest

// ResolveEndpoint returns the probe target URL from the environment or default.
func ResolveEndpoint() string {
	if val := strings.TrimSpace(os.Getenv(probeEnvVar)); val != "" {
		return val
	}
	return defaultEndpoint
}

// CheckEndpoint queries the target endpoint and returns exit code 0 on success.
func CheckEndpoint(ctx context.Context, endpoint string) int {
	if err := doRequest(ctx, endpoint); err != nil {
		return 1
	}
	return 0
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	exitFunc(CheckEndpoint(ctx, ResolveEndpoint()))
}
