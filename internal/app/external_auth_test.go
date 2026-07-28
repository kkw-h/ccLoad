package app

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
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
