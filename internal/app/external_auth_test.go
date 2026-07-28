package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"ccLoad/internal/model"
)

type staticExternalAuthResolver struct {
	addrs []net.IPAddr
	err   error
}

func (r staticExternalAuthResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addrs, r.err
}

func TestParseExternalAuthConfig(t *testing.T) {
	cfg, err := parseExternalAuthConfig(
		true,
		"https://auth.example.com/check",
		2000,
		2,
		"203.0.113.7, 2001:db8::/32",
	)
	if err != nil {
		t.Fatalf("parseExternalAuthConfig() error = %v", err)
	}
	if cfg.Timeout != 2*time.Second {
		t.Fatalf("Timeout = %v, want 2s", cfg.Timeout)
	}
	if cfg.MaxRetries != 2 {
		t.Fatalf("MaxRetries = %d, want 2", cfg.MaxRetries)
	}
	if len(cfg.BypassPrefixes) != 2 {
		t.Fatalf("BypassPrefixes len = %d, want 2", len(cfg.BypassPrefixes))
	}
	if got, want := cfg.BypassPrefixes[0], netip.MustParsePrefix("203.0.113.7/32"); got != want {
		t.Fatalf("first prefix = %v, want %v", got, want)
	}
}

func TestParseExternalAuthConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name       string
		enabled    bool
		webhookURL string
		timeoutMS  int
		retries    int
		cidrs      string
	}{
		{name: "enabled without URL", enabled: true, timeoutMS: 2000, retries: 2},
		{name: "plain HTTP", enabled: true, webhookURL: "http://auth.example.com/check", timeoutMS: 2000, retries: 2},
		{name: "URL credentials", enabled: true, webhookURL: "https://user:pass@auth.example.com/check", timeoutMS: 2000, retries: 2},
		{name: "timeout too small", enabled: true, webhookURL: "https://auth.example.com/check", timeoutMS: 99, retries: 2},
		{name: "too many retries", enabled: true, webhookURL: "https://auth.example.com/check", timeoutMS: 2000, retries: 3},
		{name: "invalid CIDR", enabled: true, webhookURL: "https://auth.example.com/check", timeoutMS: 2000, retries: 2, cidrs: "bad-cidr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseExternalAuthConfig(tt.enabled, tt.webhookURL, tt.timeoutMS, tt.retries, tt.cidrs); err == nil {
				t.Fatal("parseExternalAuthConfig() error = nil, want error")
			}
		})
	}
}

func TestExternalAuthConfigFromConfigServiceUsesManagedEnvironments(t *testing.T) {
	service := &ConfigService{cache: map[string]*model.SystemSetting{
		"external_auth_enabled":      {Value: "true"},
		"external_auth_timeout_ms":   {Value: "1500"},
		"external_auth_max_retries":  {Value: "1"},
		"external_auth_bypass_cidrs": {Value: "203.0.113.7"},
	}}
	cfg, err := externalAuthConfigFromConfigService(service)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.Timeout != 1500*time.Millisecond || cfg.MaxRetries != 1 {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.Environments == nil {
		t.Fatal("managed environments map must be non-nil to fail closed")
	}
}

func TestValidateExternalAuthEndpointRejectsUnsafeTargets(t *testing.T) {
	tests := []struct {
		name       string
		rawURL     string
		resolvedIP string
	}{
		{name: "loopback", rawURL: "https://auth.example.com/check", resolvedIP: "127.0.0.1"},
		{name: "private", rawURL: "https://auth.example.com/check", resolvedIP: "10.0.0.1"},
		{name: "link local metadata", rawURL: "https://auth.example.com/check", resolvedIP: "169.254.169.254"},
		{name: "unspecified", rawURL: "https://auth.example.com/check", resolvedIP: "0.0.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := staticExternalAuthResolver{addrs: []net.IPAddr{{IP: net.ParseIP(tt.resolvedIP)}}}
			if err := validateExternalAuthEndpoint(context.Background(), tt.rawURL, resolver); err == nil {
				t.Fatal("validateExternalAuthEndpoint() error = nil, want error")
			}
		})
	}
}

func TestValidateExternalAuthEndpointAllowsPublicHTTPS(t *testing.T) {
	resolver := staticExternalAuthResolver{addrs: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}}
	if err := validateExternalAuthEndpoint(context.Background(), "https://auth.example.com/check", resolver); err != nil {
		t.Fatalf("validateExternalAuthEndpoint() error = %v", err)
	}
}

func TestExternalAuthAuthorizeRetriesTransientFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Original-Authorization"); got != "Bearer platform-jwt" {
			t.Errorf("X-Original-Authorization = %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode webhook payload: %v", err)
		}
		if _, exists := payload["prompt"]; exists {
			t.Error("webhook payload contains prompt")
		}
		if calls.Add(1) < 3 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("X-User-Id", "d9428888-122b-11e1-b85c-61cd3cbb3210")
		w.Header().Set("X-Ccload-Token", "local-secret")
		w.Header().Set("X-Authz-Token-Exp", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	service := newTestExternalAuthService(t, server, 2)
	result, err := service.Authorize(context.Background(), externalAuthRequest{
		RequestID:             "req-1",
		Method:                http.MethodPost,
		Path:                  "/v1/responses",
		Model:                 "gpt-5",
		Stream:                true,
		ClientIP:              "198.51.100.9",
		OriginalAuthorization: "Bearer platform-jwt",
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if result.CCLoadToken != "local-secret" {
		t.Fatalf("CCLoadToken = %q", result.CCLoadToken)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
	stats := service.Metrics()
	if stats.RequestsTotal != 1 || stats.Allowed != 1 || stats.Retries != 2 {
		t.Fatalf("metrics = %+v", stats)
	}
}

func TestExternalAuthAuthorizeDoesNotRetryDenial(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer server.Close()

	_, err := newTestExternalAuthService(t, server, 2).Authorize(context.Background(), externalAuthRequest{
		OriginalAuthorization: "Bearer platform-jwt",
	})
	if !isExternalAuthErrorKind(err, externalAuthErrorDenied) {
		t.Fatalf("Authorize() error = %v, want denied", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestExternalAuthAuthorizeRejectsExpiringToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-User-Id", "d9428888-122b-11e1-b85c-61cd3cbb3210")
		w.Header().Set("X-Ccload-Token", "local-secret")
		w.Header().Set("X-Authz-Token-Exp", strconv.FormatInt(time.Now().Add(5*time.Second).Unix(), 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, err := newTestExternalAuthService(t, server, 0).Authorize(context.Background(), externalAuthRequest{
		OriginalAuthorization: "Bearer platform-jwt",
	})
	if !isExternalAuthErrorKind(err, externalAuthErrorUnavailable) {
		t.Fatalf("Authorize() error = %v, want unavailable", err)
	}
}

func TestExternalAuthAuthorizeSelectsConfiguredEnvironment(t *testing.T) {
	var developCalls atomic.Int32
	develop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		developCalls.Add(1)
		if got := r.Header.Get("X-Sedna-Env"); got != "develop" {
			t.Errorf("X-Sedna-Env = %q, want develop", got)
		}
		w.Header().Set("X-User-Id", "d9428888-122b-11e1-b85c-61cd3cbb3210")
		w.Header().Set("X-Ccload-Token", "develop-local-secret")
		w.Header().Set("X-Authz-Token-Exp", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer develop.Close()

	service := newTestExternalAuthServiceWithEnvironments(t, develop.Client(), map[string]string{
		"develop": develop.URL,
	})
	result, err := service.Authorize(context.Background(), externalAuthRequest{
		Environment:           "develop",
		OriginalAuthorization: "Bearer platform-jwt",
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if result.CCLoadToken != "develop-local-secret" || developCalls.Load() != 1 {
		t.Fatalf("result = %#v, calls = %d", result, developCalls.Load())
	}
}

func TestExternalAuthAuthorizeRejectsUnknownEnvironmentBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	service := newTestExternalAuthServiceWithEnvironments(t, server.Client(), map[string]string{
		"develop": server.URL,
	})
	_, err := service.Authorize(context.Background(), externalAuthRequest{
		Environment:           "test",
		OriginalAuthorization: "Bearer platform-jwt",
	})
	if !isExternalAuthErrorKind(err, externalAuthErrorDenied) {
		t.Fatalf("Authorize() error = %v, want denied", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("network calls = %d, want 0", calls.Load())
	}
}

func newTestExternalAuthServiceWithEnvironments(t *testing.T, client *http.Client, raw map[string]string) *ExternalAuthService {
	t.Helper()
	targets := make(map[string]externalAuthEnvironmentTarget, len(raw))
	for environment, endpoint := range raw {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		targets[environment] = externalAuthEnvironmentTarget{Environment: environment, AuthzURL: parsed}
	}
	return newExternalAuthService(externalAuthConfig{
		Enabled:      true,
		Environments: targets,
		Timeout:      time.Second,
	}, client, func(time.Duration) time.Duration { return 0 })
}

func newTestExternalAuthService(t *testing.T, server *httptest.Server, retries int) *ExternalAuthService {
	t.Helper()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return newExternalAuthService(externalAuthConfig{
		Enabled:    true,
		WebhookURL: endpoint,
		Timeout:    time.Second,
		MaxRetries: retries,
	}, server.Client(), func(time.Duration) time.Duration { return 0 })
}
