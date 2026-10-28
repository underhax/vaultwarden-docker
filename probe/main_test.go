package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
)

var (
	errMockRequest   = errors.New("simulated request creation failure")
	errMockClose     = errors.New("simulated close failure")
	errMockTransport = errors.New("simulated transport failure")
	errMockDoRequest = errors.New("simulated do request failure")
)

var recordedExitCode = -1

func mockExit(code int) {
	recordedExitCode = code
}

func mockFailingCreateRequest(_ context.Context, _, _ string, _ io.Reader) (*http.Request, error) {
	return nil, errMockRequest
}

func mockFailingDoRequest(_ context.Context, _ string) error {
	return errMockDoRequest
}

type mockRoundTripper struct {
	response *http.Response
	err      error
}

func (m *mockRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

var activeMockTransport = &mockRoundTripper{}

func mockGetTransport() http.RoundTripper {
	return activeMockTransport
}

type failingCloseBody struct {
	io.Reader
}

func (f *failingCloseBody) Close() error {
	return errMockClose
}

type failingReadBody struct{}

func (f *failingReadBody) Read(_ []byte) (int, error) {
	return 0, errors.New("simulated read error")
}

func (f *failingReadBody) Close() error {
	return nil
}

var (
	mockFileData []byte
	errMockFile  error
)

func mockReadFile(_ string) ([]byte, error) {
	if errMockFile != nil {
		return nil, errMockFile
	}
	return mockFileData, nil
}

func TestResolveSubpathEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		domain   string
		expected string
	}{
		{
			name:     "empty_input",
			domain:   "   ",
			expected: defaultEndpoint,
		},
		{
			name:     "root_domain_no_path",
			domain:   "https://vault.example.com",
			expected: defaultEndpoint,
		},
		{
			name:     "root_domain_slash_path",
			domain:   "https://vault.example.com/",
			expected: defaultEndpoint,
		},
		{
			name:     "subpath_without_trailing_slash",
			domain:   "https://example.com/custom",
			expected: "http://127.0.0.1:8080/custom/alive",
		},
		{
			name:     "subpath_with_trailing_slash",
			domain:   "https://example.com/custom/",
			expected: "http://127.0.0.1:8080/custom/alive",
		},
		{
			name:     "deep_nested_subpath",
			domain:   "https://example.net/deep/nested",
			expected: "http://127.0.0.1:8080/deep/nested/alive",
		},
		{
			name:     "domain_without_scheme",
			domain:   "example.org/vault",
			expected: "http://127.0.0.1:8080/vault/alive",
		},
		{
			name:     "invalid_url_format",
			domain:   "https://example .com",
			expected: defaultEndpoint,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveSubpathEndpoint(tt.domain); got != tt.expected {
				t.Fatalf("expected endpoint %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestResolveEndpoint(t *testing.T) {
	if err := os.Unsetenv(probeEnvVar); err != nil {
		t.Fatalf("unsetting env: %v", err)
	}

	origReadFile := readFile
	readFile = mockReadFile
	t.Cleanup(func() { readFile = origReadFile })

	mockFileData = nil
	errMockFile = errors.New("simulated file read error")

	if ep := ResolveEndpoint(); ep != defaultEndpoint {
		t.Fatalf("expected default endpoint %q on read failure, got %q", defaultEndpoint, ep)
	}

	errMockFile = nil
	mockFileData = []byte("https://example.com/sub")
	if ep := ResolveEndpoint(); ep != "http://127.0.0.1:8080/sub/alive" {
		t.Fatalf("expected subpath endpoint from secret, got %q", ep)
	}

	custom := "http://example.com/alive"
	t.Setenv(probeEnvVar, custom)
	if ep := ResolveEndpoint(); ep != custom {
		t.Fatalf("expected env override %q, got %q", custom, ep)
	}

	t.Setenv(probeEnvVar, "   ")
	if ep := ResolveEndpoint(); ep != "http://127.0.0.1:8080/sub/alive" {
		t.Fatalf("expected secret endpoint when env is whitespace, got %q", ep)
	}
}

func TestDefaultReadFile(t *testing.T) {
	if _, err := defaultReadFile("non_existent_file_path"); err == nil {
		t.Fatal("expected error reading non-existent file, got nil")
	}

	data, err := defaultReadFile("main.go")
	if err != nil {
		t.Fatalf("unexpected error reading existing file: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty data from existing file")
	}
}

func TestCheckEndpoint(t *testing.T) {
	tests := []struct {
		transportErr error
		name         string
		statusCode   int
		wantCode     int
	}{
		{
			name:         "status_ok_success",
			transportErr: nil,
			statusCode:   http.StatusOK,
			wantCode:     0,
		},
		{
			name:         "status_service_unavailable",
			transportErr: nil,
			statusCode:   http.StatusServiceUnavailable,
			wantCode:     1,
		},
		{
			name:         "status_internal_server_error",
			transportErr: nil,
			statusCode:   http.StatusInternalServerError,
			wantCode:     1,
		},
		{
			name:         "status_not_found",
			transportErr: nil,
			statusCode:   http.StatusNotFound,
			wantCode:     1,
		},
		{
			name:         "transport_network_error",
			transportErr: errMockTransport,
			statusCode:   0,
			wantCode:     1,
		},
	}

	origTransport := getTransport
	getTransport = mockGetTransport
	t.Cleanup(func() { getTransport = origTransport })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.transportErr != nil {
				activeMockTransport.response = nil
				activeMockTransport.err = tt.transportErr
			} else {
				activeMockTransport.err = nil
				activeMockTransport.response = &http.Response{
					StatusCode: tt.statusCode,
					Body:       io.NopCloser(bytes.NewReader(nil)),
					Header:     make(http.Header),
				}
			}

			ctx := context.Background()
			code := CheckEndpoint(ctx, "http://example.com/alive")
			if code != tt.wantCode {
				t.Fatalf("expected code %d, got %d", tt.wantCode, code)
			}
		})
	}
}

func TestCheckEndpoint_EdgeCases(t *testing.T) {
	ctx := context.Background()

	if code := CheckEndpoint(ctx, "ftp://example.com/alive"); code != 1 {
		t.Fatalf("expected failure code 1 for unsupported scheme, got %d", code)
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := CheckEndpoint(cancelledCtx, "http://example.com/alive"); code != 1 {
		t.Fatalf("expected failure code 1 for cancelled context, got %d", code)
	}

	origDoRequest := doRequest
	doRequest = mockFailingDoRequest
	t.Cleanup(func() { doRequest = origDoRequest })

	if code := CheckEndpoint(ctx, "http://example.com/alive"); code != 1 {
		t.Fatalf("expected failure code 1 for mocked doRequest error, got %d", code)
	}
}

func TestDefaultCreateRequest(t *testing.T) {
	ctx := context.Background()
	req, err := defaultCreateRequest(ctx, http.MethodGet, "http://example.com", http.NoBody)
	if err != nil {
		t.Fatalf("unexpected error creating request: %v", err)
	}
	if req == nil {
		t.Fatal("expected request object, got nil")
	}

	if _, err := defaultCreateRequest(ctx, "INVALID METHOD", "http://example.com", http.NoBody); err == nil {
		t.Fatal("expected error with invalid HTTP method, got nil")
	}
}

func TestDefaultDoRequest_Errors(t *testing.T) {
	ctx := context.Background()

	if err := defaultDoRequest(ctx, "http://example .com"); err == nil {
		t.Fatal("expected URL parsing error, got nil")
	}

	if err := defaultDoRequest(ctx, "gopher://example.com"); err == nil {
		t.Fatal("expected unsupported scheme error, got nil")
	}

	origCreateReq := createRequest
	createRequest = mockFailingCreateRequest
	t.Cleanup(func() { createRequest = origCreateReq })

	if err := defaultDoRequest(ctx, "http://example.com"); err == nil {
		t.Fatal("expected request creation error, got nil")
	}

	createRequest = origCreateReq

	origTransport := getTransport
	getTransport = mockGetTransport
	t.Cleanup(func() { getTransport = origTransport })

	activeMockTransport.err = nil
	activeMockTransport.response = &http.Response{
		StatusCode: http.StatusOK,
		Body:       &failingCloseBody{Reader: http.NoBody},
		Header:     make(http.Header),
	}

	if err := defaultDoRequest(ctx, "http://example.com"); err == nil {
		t.Fatal("expected response body close error, got nil")
	}

	activeMockTransport.response = &http.Response{
		StatusCode: http.StatusOK,
		Body:       &failingReadBody{},
		Header:     make(http.Header),
	}

	if err := defaultDoRequest(ctx, "http://example.com"); err == nil {
		t.Fatal("expected response body read error, got nil")
	}
}

func TestMain_Execution(t *testing.T) {
	origExit := exitFunc
	origTransport := getTransport
	exitFunc = mockExit
	getTransport = mockGetTransport
	t.Cleanup(func() {
		exitFunc = origExit
		getTransport = origTransport
	})

	activeMockTransport.err = nil
	activeMockTransport.response = &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Header:     make(http.Header),
	}

	recordedExitCode = -1
	main()

	if recordedExitCode != 0 {
		t.Fatalf("expected exit code 0 on healthy endpoint, got %d", recordedExitCode)
	}

	activeMockTransport.response = &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Header:     make(http.Header),
	}

	recordedExitCode = -1
	main()

	if recordedExitCode != 1 {
		t.Fatalf("expected exit code 1 on unhealthy endpoint, got %d", recordedExitCode)
	}
}
