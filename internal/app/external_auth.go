package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
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

type externalAuthRequest struct {
	RequestID             string `json:"request_id"`
	Method                string `json:"method"`
	Path                  string `json:"path"`
	Model                 string `json:"model,omitempty"`
	Stream                bool   `json:"stream"`
	ClientIP              string `json:"client_ip,omitempty"`
	OriginalAuthorization string `json:"-"`
}

type externalAuthResult struct {
	ExternalUserID string
	CCLoadToken    string
	ExpiresAt      time.Time
}

type externalAuthErrorKind uint8

const (
	externalAuthErrorDenied externalAuthErrorKind = iota + 1
	externalAuthErrorUnavailable
)

type externalAuthError struct {
	kind externalAuthErrorKind
	msg  string
}

func (e *externalAuthError) Error() string { return e.msg }

func isExternalAuthErrorKind(err error, kind externalAuthErrorKind) bool {
	var target *externalAuthError
	return errors.As(err, &target) && target.kind == kind
}

type externalAuthMetrics struct {
	requestsTotal atomic.Int64
	allowed       atomic.Int64
	denied        atomic.Int64
	errors        atomic.Int64
	retries       atomic.Int64
	bypassed      atomic.Int64
	durationNanos atomic.Int64
}

type externalAuthMetricsSnapshot struct {
	RequestsTotal   int64 `json:"requests_total"`
	Allowed         int64 `json:"allowed"`
	Denied          int64 `json:"denied"`
	Errors          int64 `json:"errors"`
	Retries         int64 `json:"retries"`
	Bypassed        int64 `json:"bypassed"`
	DurationMSTotal int64 `json:"duration_ms_total"`
}

type ExternalAuthService struct {
	config externalAuthConfig
	client *http.Client
	jitter func(time.Duration) time.Duration
	now    func() time.Time
	stats  externalAuthMetrics
}

func newExternalAuthService(
	cfg externalAuthConfig,
	client *http.Client,
	jitter func(time.Duration) time.Duration,
) *ExternalAuthService {
	if client == nil {
		client = &http.Client{}
	}
	if jitter == nil {
		jitter = func(delay time.Duration) time.Duration { return delay }
	}
	return &ExternalAuthService{
		config: cfg,
		client: client,
		jitter: jitter,
		now:    time.Now,
	}
}

func (s *ExternalAuthService) Metrics() externalAuthMetricsSnapshot {
	if s == nil {
		return externalAuthMetricsSnapshot{}
	}
	return externalAuthMetricsSnapshot{
		RequestsTotal:   s.stats.requestsTotal.Load(),
		Allowed:         s.stats.allowed.Load(),
		Denied:          s.stats.denied.Load(),
		Errors:          s.stats.errors.Load(),
		Retries:         s.stats.retries.Load(),
		Bypassed:        s.stats.bypassed.Load(),
		DurationMSTotal: s.stats.durationNanos.Load() / int64(time.Millisecond),
	}
}

func (s *ExternalAuthService) Authorize(
	ctx context.Context,
	input externalAuthRequest,
) (externalAuthResult, error) {
	started := s.now()
	s.stats.requestsTotal.Add(1)
	defer func() {
		s.stats.durationNanos.Add(s.now().Sub(started).Nanoseconds())
	}()

	payload, err := json.Marshal(input)
	if err != nil {
		s.stats.errors.Add(1)
		return externalAuthResult{}, unavailableExternalAuthError("encode authorization request")
	}
	for attempt := 0; attempt <= s.config.MaxRetries; attempt++ {
		if attempt > 0 {
			s.stats.retries.Add(1)
			if err := waitExternalAuthRetry(ctx, s.jitter(externalAuthRetryDelay(attempt))); err != nil {
				s.stats.errors.Add(1)
				return externalAuthResult{}, unavailableExternalAuthError("authorization request canceled")
			}
		}
		result, retry, err := s.authorizeAttempt(ctx, input.OriginalAuthorization, payload)
		if err == nil {
			s.stats.allowed.Add(1)
			return result, nil
		}
		if isExternalAuthErrorKind(err, externalAuthErrorDenied) {
			s.stats.denied.Add(1)
			return externalAuthResult{}, err
		}
		if !retry || attempt == s.config.MaxRetries {
			s.stats.errors.Add(1)
			return externalAuthResult{}, err
		}
	}
	panic("unreachable")
}

func (s *ExternalAuthService) authorizeAttempt(
	ctx context.Context,
	originalAuthorization string,
	payload []byte,
) (externalAuthResult, bool, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, s.config.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, s.config.WebhookURL.String(), bytes.NewReader(payload))
	if err != nil {
		return externalAuthResult{}, false, unavailableExternalAuthError("build authorization request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Original-Authorization", originalAuthorization)
	resp, err := s.client.Do(req)
	if err != nil {
		return externalAuthResult{}, true, unavailableExternalAuthError("authorization service request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return externalAuthResult{}, false, &externalAuthError{kind: externalAuthErrorDenied, msg: "external authorization denied"}
	case resp.StatusCode == http.StatusRequestTimeout ||
		resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode >= http.StatusInternalServerError:
		return externalAuthResult{}, true, unavailableExternalAuthError("authorization service temporarily unavailable")
	case resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices:
		return externalAuthResult{}, false, unavailableExternalAuthError("authorization service returned an invalid status")
	}

	externalUserID := strings.TrimSpace(resp.Header.Get("X-User-Id"))
	if _, err := uuid.Parse(externalUserID); err != nil {
		return externalAuthResult{}, false, unavailableExternalAuthError("authorization service returned an invalid user ID")
	}
	ccLoadToken := strings.TrimSpace(resp.Header.Get("X-Ccload-Token"))
	if ccLoadToken == "" {
		return externalAuthResult{}, false, unavailableExternalAuthError("authorization service returned no local token")
	}
	expUnix, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-Authz-Token-Exp")), 10, 64)
	if err != nil {
		return externalAuthResult{}, false, unavailableExternalAuthError("authorization service returned an invalid expiration")
	}
	expiresAt := time.Unix(expUnix, 0)
	if !expiresAt.After(s.now().Add(5 * time.Second)) {
		return externalAuthResult{}, false, unavailableExternalAuthError("authorization result expires too soon")
	}
	return externalAuthResult{
		ExternalUserID: externalUserID,
		CCLoadToken:    ccLoadToken,
		ExpiresAt:      expiresAt,
	}, false, nil
}

func unavailableExternalAuthError(msg string) error {
	return &externalAuthError{kind: externalAuthErrorUnavailable, msg: msg}
}

func externalAuthRetryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return 100 * time.Millisecond
	}
	return 300 * time.Millisecond
}

func waitExternalAuthRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
