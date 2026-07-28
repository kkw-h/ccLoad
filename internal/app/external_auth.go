package app

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	externalAuthMinTimeoutMS = 100
	externalAuthMaxTimeoutMS = 10_000
	externalAuthMaxRetries   = 2
)

type externalAuthConfig struct {
	Enabled        bool
	WebhookURL     *url.URL
	Timeout        time.Duration
	MaxRetries     int
	BypassPrefixes []netip.Prefix
}

type externalAuthResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

func parseExternalAuthConfig(
	enabled bool,
	rawWebhookURL string,
	timeoutMS int,
	maxRetries int,
	rawBypassCIDRs string,
) (externalAuthConfig, error) {
	cfg := externalAuthConfig{
		Enabled:    enabled,
		Timeout:    time.Duration(timeoutMS) * time.Millisecond,
		MaxRetries: maxRetries,
	}
	if timeoutMS < externalAuthMinTimeoutMS || timeoutMS > externalAuthMaxTimeoutMS {
		return externalAuthConfig{}, fmt.Errorf(
			"external auth timeout must be between %d and %d milliseconds",
			externalAuthMinTimeoutMS,
			externalAuthMaxTimeoutMS,
		)
	}
	if maxRetries < 0 || maxRetries > externalAuthMaxRetries {
		return externalAuthConfig{}, fmt.Errorf("external auth max retries must be between 0 and %d", externalAuthMaxRetries)
	}

	rawWebhookURL = strings.TrimSpace(rawWebhookURL)
	if rawWebhookURL == "" {
		if enabled {
			return externalAuthConfig{}, fmt.Errorf("external auth webhook URL is required when enabled")
		}
	} else {
		parsed, err := url.Parse(rawWebhookURL)
		if err != nil {
			return externalAuthConfig{}, fmt.Errorf("parse external auth webhook URL: %w", err)
		}
		if parsed.Scheme != "https" || parsed.Hostname() == "" {
			return externalAuthConfig{}, fmt.Errorf("external auth webhook URL must use HTTPS")
		}
		if parsed.User != nil {
			return externalAuthConfig{}, fmt.Errorf("external auth webhook URL must not contain credentials")
		}
		cfg.WebhookURL = parsed
	}

	prefixes, err := parseExternalAuthBypassPrefixes(rawBypassCIDRs)
	if err != nil {
		return externalAuthConfig{}, err
	}
	cfg.BypassPrefixes = prefixes
	return cfg, nil
}

func parseExternalAuthBypassPrefixes(raw string) ([]netip.Prefix, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			return nil, fmt.Errorf("external auth bypass CIDR contains an empty entry")
		}
		if addr, err := netip.ParseAddr(value); err == nil {
			prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("invalid external auth bypass CIDR %q", value)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

func validateExternalAuthEndpoint(
	ctx context.Context,
	rawWebhookURL string,
	resolver externalAuthResolver,
) error {
	parsed, err := url.Parse(strings.TrimSpace(rawWebhookURL))
	if err != nil {
		return fmt.Errorf("parse external auth webhook URL: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return fmt.Errorf("external auth webhook URL must be credential-free HTTPS")
	}
	addrs, err := resolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil {
		return fmt.Errorf("resolve external auth webhook host: %w", err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("external auth webhook host resolved to no addresses")
	}
	for _, resolved := range addrs {
		addr, ok := netip.AddrFromSlice(resolved.IP)
		if !ok || isUnsafeExternalAuthIP(addr.Unmap()) {
			return fmt.Errorf("external auth webhook host resolved to a non-public address")
		}
	}
	return nil
}

func isUnsafeExternalAuthIP(ip netip.Addr) bool {
	return !ip.IsValid() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}
