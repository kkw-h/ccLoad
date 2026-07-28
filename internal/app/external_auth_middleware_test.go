package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestExternalAuthMiddlewareUsesReturnedLocalTokenAndPreservesAuthorization(t *testing.T) {
	var calls atomic.Int32
	authz := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("X-Original-Authorization"); got != "Bearer user-jwt" {
			t.Errorf("X-Original-Authorization = %q", got)
		}
		if got := r.Header.Get("X-Sedna-Env"); got != "develop" {
			t.Errorf("X-Sedna-Env = %q", got)
		}
		var payload externalAuthRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode authz payload: %v", err)
		}
		if payload.Path != "/v1/responses" || payload.Model != "gpt-5" || !payload.Stream {
			t.Errorf("authz payload = %+v", payload)
		}
		w.Header().Set("X-User-Id", "d9428888-122b-11e1-b85c-61cd3cbb3210")
		w.Header().Set("X-Ccload-Token", "local-secret")
		w.Header().Set("X-Authz-Token-Exp", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer authz.Close()

	authzURL, err := url.Parse(authz.URL)
	if err != nil {
		t.Fatal(err)
	}
	external := newExternalAuthService(externalAuthConfig{
		Enabled: true,
		Environments: map[string]externalAuthEnvironmentTarget{
			"develop": {Environment: "develop", AuthzURL: authzURL},
		},
		Timeout: time.Second,
	}, authz.Client(), func(time.Duration) time.Duration { return 0 })
	local := newTestAuthService(t)
	injectAPIToken(local, "local-secret", 0, 42)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/v1/responses",
		captureClientRequestMetadata(),
		external.Middleware(),
		local.RequireAPIAuth(),
		func(c *gin.Context) {
			identity, ok := externalAuthIdentityFromContext(c)
			c.JSON(http.StatusOK, gin.H{
				"authorization": c.GetHeader("Authorization"),
				"token_id":      c.GetInt64("token_id"),
				"user_id":       identity.ExternalUserID,
				"has_identity":  ok,
			})
		},
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"gpt-5","stream":true,"input":"secret prompt"}`))
	req.Header.Set("Authorization", "Bearer user-jwt")
	req.Header.Set("X-Sedna-Env", " develop ")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["authorization"] != "Bearer user-jwt" {
		t.Fatalf("authorization changed: %v", body["authorization"])
	}
	if body["token_id"] != float64(42) || body["has_identity"] != true {
		t.Fatalf("local identity not applied: %#v", body)
	}
	if calls.Load() != 1 {
		t.Fatalf("authz calls = %d", calls.Load())
	}
}

func TestExternalAuthMiddlewareFailsClosedBeforeLocalAuth(t *testing.T) {
	external := newExternalAuthService(externalAuthConfig{
		Enabled:      true,
		Environments: map[string]externalAuthEnvironmentTarget{},
		Timeout:      time.Second,
	}, nil, nil)
	local := newTestAuthService(t)
	injectAPIToken(local, "local-secret", 0, 42)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/v1/responses", external.Middleware(), local.RequireAPIAuth(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer user-jwt")
	req.Header.Set("X-Sedna-Env", "unknown")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestExternalAuthMiddlewareDisabledKeepsLegacyLocalAuth(t *testing.T) {
	external := newExternalAuthService(externalAuthConfig{Enabled: false}, nil, nil)
	local := newTestAuthService(t)
	injectAPIToken(local, "legacy-token", 0, 7)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/v1/responses", external.Middleware(), local.RequireAPIAuth(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer legacy-token")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestExternalAuthMiddlewareBypassesConfiguredClientCIDR(t *testing.T) {
	external := newExternalAuthService(externalAuthConfig{
		Enabled:        true,
		Environments:   map[string]externalAuthEnvironmentTarget{},
		Timeout:        time.Second,
		BypassPrefixes: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
	}, nil, nil)
	local := newTestAuthService(t)
	injectAPIToken(local, "legacy-token", 0, 7)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.SetTrustedProxies(nil)
	engine.POST("/v1/responses", external.Middleware(), local.RequireAPIAuth(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("Authorization", "Bearer legacy-token")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if external.Metrics().Bypassed != 1 {
		t.Fatalf("bypassed = %d", external.Metrics().Bypassed)
	}
}

func TestExternalAuthIsMountedOnlyOnPublicProxyRoutes(t *testing.T) {
	var calls atomic.Int32
	authz := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("X-User-Id", "d9428888-122b-11e1-b85c-61cd3cbb3210")
		w.Header().Set("X-Ccload-Token", "local-secret")
		w.Header().Set("X-Authz-Token-Exp", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer authz.Close()
	authzURL, err := url.Parse(authz.URL)
	if err != nil {
		t.Fatal(err)
	}

	server := newInMemoryServer(t)
	injectAPIToken(server.authService, "local-secret", 0, 42)
	server.externalAuthService = newExternalAuthService(externalAuthConfig{
		Enabled: true,
		Environments: map[string]externalAuthEnvironmentTarget{
			"test": {Environment: "test", AuthzURL: authzURL},
		},
		Timeout: time.Second,
	}, authz.Client(), nil)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	server.SetupRoutes(engine)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/chat/completions"},
		{http.MethodPost, "/v1beta/models/gemini-pro:generateContent"},
		{http.MethodGet, "/backend-api/codex/responses"},
	} {
		before := calls.Load()
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer user-jwt")
		req.Header.Set("X-Sedna-Env", "test")
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if calls.Load() != before+1 {
			t.Errorf("%s %s authz calls = %d, want %d", tc.method, tc.path, calls.Load(), before+1)
		}
	}

	before := calls.Load()
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if calls.Load() != before {
		t.Fatalf("health endpoint called authz")
	}
}

func TestResponsesWebsocketTurnReauthorizesWithReturnedLocalToken(t *testing.T) {
	var calls atomic.Int32
	authz := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("X-User-Id", "d9428888-122b-11e1-b85c-61cd3cbb3210")
		w.Header().Set("X-Ccload-Token", "turn-local-token")
		w.Header().Set("X-Authz-Token-Exp", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer authz.Close()
	authzURL, err := url.Parse(authz.URL)
	if err != nil {
		t.Fatal(err)
	}

	server := &Server{
		externalAuthService: newExternalAuthService(externalAuthConfig{
			Enabled: true,
			Environments: map[string]externalAuthEnvironmentTarget{
				"develop": {Environment: "develop", AuthzURL: authzURL},
			},
			Timeout: time.Second,
		}, authz.Client(), nil),
		authService: newTestAuthService(t),
	}
	injectAPIToken(server.authService, "turn-local-token", 0, 88)

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/backend-api/codex/responses", nil)
	c.Request.Header.Set("Authorization", "Bearer user-jwt")
	c.Request.Header.Set("X-Sedna-Env", "develop")

	err = server.authorizeResponsesWebsocketTurn(c, []byte(`{"type":"response.create","response":{"model":"gpt-5"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.GetInt64("token_id"); got != 88 {
		t.Fatalf("token_id = %d", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("authz calls = %d", calls.Load())
	}
}
