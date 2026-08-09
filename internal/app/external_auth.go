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
	"sync"
	"sync/atomic"
	"time"

	"ccLoad/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	externalAuthMinTimeoutMS = 100
	externalAuthMaxTimeoutMS = 10_000
	externalAuthMaxRetries   = 2
	externalAuthLoadTimeout  = 10 * time.Second
)

type externalAuthConfig struct {
	Enabled        bool
	WebhookURL     *url.URL
	Environments   map[string]externalAuthEnvironmentTarget
	Timeout        time.Duration
	MaxRetries     int
	BypassPrefixes []netip.Prefix
}

type externalAuthEnvironmentTarget struct {
	Environment string
	AuthzURL    *url.URL
}

type externalAuthResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type externalAuthRequest struct {
	Environment           string `json:"-"`
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

const externalAuthIdentityContextKey = "ccLoad.externalAuthIdentity"

type externalAuthIdentity struct {
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
	config        externalAuthConfig
	environmentMu sync.RWMutex
	client        *http.Client
	jitter        func(time.Duration) time.Duration
	now           func() time.Time
	stats         externalAuthMetrics
}

func newExternalAuthLoadContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), externalAuthLoadTimeout)
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

func (s *ExternalAuthService) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s == nil || !s.config.Enabled {
			c.Next()
			return
		}
		if s.bypassesClientIP(c.ClientIP()) {
			s.stats.bypassed.Add(1)
			c.Next()
			return
		}

		originalAuthorization := c.GetHeader("Authorization")
		if originalAuthorization == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or missing authorization"})
			c.Abort()
			return
		}
		environmentHeaders := c.Request.Header.Values("X-Sedna-Env")
		if len(environmentHeaders) != 1 {
			c.JSON(http.StatusForbidden, gin.H{"error": "external authorization environment denied"})
			c.Abort()
			return
		}
		modelName, stream := readExternalAuthRequestMetadata(c.Request)

		result, err := s.Authorize(c.Request.Context(), externalAuthRequest{
			Environment:           environmentHeaders[0],
			RequestID:             c.GetHeader("X-Request-Id"),
			Method:                c.Request.Method,
			Path:                  c.Request.URL.Path,
			Model:                 modelName,
			Stream:                stream,
			ClientIP:              c.ClientIP(),
			OriginalAuthorization: originalAuthorization,
		})
		if err != nil {
			status := http.StatusServiceUnavailable
			if isExternalAuthErrorKind(err, externalAuthErrorDenied) {
				status = http.StatusForbidden
			}
			c.JSON(status, gin.H{"error": err.Error()})
			c.Abort()
			return
		}
		c.Set(externalAuthIdentityContextKey, externalAuthIdentity{
			ExternalUserID: result.ExternalUserID,
			CCLoadToken:    result.CCLoadToken,
			ExpiresAt:      result.ExpiresAt,
		})
		c.Next()
	}
}

func readExternalAuthRequestMetadata(req *http.Request) (string, bool) {
	if req == nil || req.Body == nil {
		return "", false
	}
	var captured bytes.Buffer
	reader := io.TeeReader(req.Body, &captured)
	var metadata struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	err := json.NewDecoder(io.LimitReader(reader, 1<<20)).Decode(&metadata)
	req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(captured.Bytes()), req.Body))
	if err != nil {
		return "", false
	}
	return metadata.Model, metadata.Stream
}

func (s *ExternalAuthService) bypassesClientIP(raw string) bool {
	if s == nil || len(s.config.BypassPrefixes) == 0 {
		return false
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	for _, prefix := range s.config.BypassPrefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func externalAuthIdentityFromContext(c *gin.Context) (externalAuthIdentity, bool) {
	if c == nil {
		return externalAuthIdentity{}, false
	}
	value, ok := c.Get(externalAuthIdentityContextKey)
	if !ok {
		return externalAuthIdentity{}, false
	}
	identity, ok := value.(externalAuthIdentity)
	return identity, ok
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
	target, err := s.environmentTarget(input.Environment)
	if err != nil {
		s.stats.denied.Add(1)
		return externalAuthResult{}, err
	}

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
		result, retry, err := s.authorizeAttempt(ctx, target, input.OriginalAuthorization, payload)
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
	target externalAuthEnvironmentTarget,
	originalAuthorization string,
	payload []byte,
) (externalAuthResult, bool, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, s.config.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, target.AuthzURL.String(), bytes.NewReader(payload))
	if err != nil {
		return externalAuthResult{}, false, unavailableExternalAuthError("build authorization request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Original-Authorization", originalAuthorization)
	if target.Environment != "" {
		req.Header.Set("X-Sedna-Env", target.Environment)
	}
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

func (s *ExternalAuthService) environmentTarget(raw string) (externalAuthEnvironmentTarget, error) {
	s.environmentMu.RLock()
	defer s.environmentMu.RUnlock()
	if s.config.Environments == nil {
		if s.config.WebhookURL != nil {
			return externalAuthEnvironmentTarget{AuthzURL: s.config.WebhookURL}, nil
		}
		return externalAuthEnvironmentTarget{}, unavailableExternalAuthError("authorization service is not configured")
	}
	environment, err := model.NormalizeExternalAuthEnvironment(raw)
	if err != nil {
		return externalAuthEnvironmentTarget{}, &externalAuthError{kind: externalAuthErrorDenied, msg: "external authorization environment denied"}
	}
	target, ok := s.config.Environments[environment]
	if !ok || target.AuthzURL == nil || target.Environment != environment {
		return externalAuthEnvironmentTarget{}, &externalAuthError{kind: externalAuthErrorDenied, msg: "external authorization environment denied"}
	}
	return target, nil
}

func (s *ExternalAuthService) ReplaceEnvironments(targets map[string]externalAuthEnvironmentTarget) {
	if s == nil {
		return
	}
	snapshot := make(map[string]externalAuthEnvironmentTarget, len(targets))
	for key, target := range targets {
		snapshot[key] = target
	}
	s.environmentMu.Lock()
	s.config.Environments = snapshot
	s.environmentMu.Unlock()
}

func buildExternalAuthEnvironmentTargets(items []*model.ExternalAuthEnvironment) (map[string]externalAuthEnvironmentTarget, error) {
	targets := make(map[string]externalAuthEnvironmentTarget)
	for _, item := range items {
		if item == nil || !item.IsActive {
			continue
		}
		environment, err := model.NormalizeExternalAuthEnvironment(item.Environment)
		if err != nil {
			return nil, err
		}
		parsed, err := url.Parse(strings.TrimSpace(item.AuthzURL))
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
			return nil, fmt.Errorf("invalid external auth URL for environment %q", environment)
		}
		targets[environment] = externalAuthEnvironmentTarget{Environment: environment, AuthzURL: parsed}
	}
	return targets, nil
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

func externalAuthConfigFromConfigService(configService *ConfigService) (externalAuthConfig, error) {
	if configService == nil {
		return externalAuthConfig{}, fmt.Errorf("external auth config service is required")
	}
	timeoutMS := configService.GetInt("external_auth_timeout_ms", 2000)
	maxRetries := configService.GetInt("external_auth_max_retries", 2)
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
	prefixes, err := parseExternalAuthBypassPrefixes(configService.GetString("external_auth_bypass_cidrs", ""))
	if err != nil {
		return externalAuthConfig{}, err
	}
	return externalAuthConfig{
		Enabled:        configService.GetBool("external_auth_enabled", false),
		Environments:   make(map[string]externalAuthEnvironmentTarget),
		Timeout:        time.Duration(timeoutMS) * time.Millisecond,
		MaxRetries:     maxRetries,
		BypassPrefixes: prefixes,
	}, nil
}

func newExternalAuthHTTPClient(resolver externalAuthResolver) *http.Client {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	transport := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("parse external auth address: %w", err)
		}
		addrs, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve external auth host: %w", err)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("external auth host resolved to no addresses")
		}
		for _, resolved := range addrs {
			addr, ok := netip.AddrFromSlice(resolved.IP)
			if !ok || isUnsafeExternalAuthIP(addr.Unmap()) {
				return nil, fmt.Errorf("external auth host resolved to a non-public address")
			}
		}
		var lastErr error
		for _, resolved := range addrs {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, fmt.Errorf("connect external auth host: %w", lastErr)
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
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
