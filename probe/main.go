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
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultEndpoint     = "http://127.0.0.1:8080/alive"
	defaultDomainPath   = "/run/secrets/vw_domain"
	aliveEndpointSuffix = "/alive"
	probeEnvVar         = "VAULTWARDEN_PROBE_URL"
	defaultTimeout      = 2 * time.Second
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

func defaultReadFile(path string) ([]byte, error) {
	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("reading secret file %q: %w", cleanPath, err)
	}
	return data, nil
}

var (
	createRequest = defaultCreateRequest
	exitFunc      = os.Exit
	getTransport  = defaultGetTransport
	readFile      = defaultReadFile
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

func resolveSubpathEndpoint(domain string) string {
	cleanDomain := strings.TrimSpace(domain)
	if cleanDomain == "" {
		return defaultEndpoint
	}

	if !strings.Contains(cleanDomain, "://") {
		cleanDomain = "https://" + cleanDomain
	}

	parsed, err := url.Parse(cleanDomain)
	if err != nil {
		return defaultEndpoint
	}

	subpath := strings.TrimRight(parsed.Path, "/")
	if subpath == "" {
		return defaultEndpoint
	}

	return "http://127.0.0.1:8080" + subpath + aliveEndpointSuffix
}

// ResolveEndpoint discovers the liveness probe endpoint across environment variables,
// mounted domain secrets, and runtime defaults to accommodate subpath reverse proxy routing.
func ResolveEndpoint() string {
	if val := strings.TrimSpace(os.Getenv(probeEnvVar)); val != "" {
		return val
	}

	data, err := readFile(defaultDomainPath)
	if err == nil {
		return resolveSubpathEndpoint(string(data))
	}

	return defaultEndpoint
}

// CheckEndpoint executes the liveness check against the designated URL and signals container health via process exit code.
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
