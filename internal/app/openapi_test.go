package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-yaml"
)

func TestManagementOpenAPICoversRegisteredAdminRoutes(t *testing.T) {
	content, err := fstest.MapFS{
		"openapi.yaml": &fstest.MapFile{Data: mustReadRepositoryFile(t, "../../web/openapi.yaml")},
	}.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		OpenAPI string                    `yaml:"openapi"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	if document.OpenAPI != "3.1.0" {
		t.Fatalf("openapi=%q, want 3.1.0", document.OpenAPI)
	}
	if strings.Contains(string(content), "external-auth") || strings.Contains(string(content), "ExternalAuth") {
		t.Fatal("OpenAPI must not expose the canceled external auth business")
	}
	if !strings.Contains(string(content), "name: auth_token_id") || !strings.Contains(string(content), "type: array") {
		t.Fatal("OpenAPI must document multi-value auth_token_id filtering")
	}

	for _, route := range managementOpenAPIRoutes() {
		methods, ok := document.Paths[route.path]
		if !ok {
			t.Errorf("OpenAPI missing path %s", route.path)
			continue
		}
		if _, ok := methods[route.method]; !ok {
			t.Errorf("OpenAPI missing operation %s %s", route.method, route.path)
		}
	}

}

func TestManagementOpenAPIDocumentsRequiredAuthDiscriminators(t *testing.T) {
	content := mustReadRepositoryFile(t, "../../web/openapi.yaml")
	var document struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string       `yaml:"required"`
				Properties map[string]any `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}

	login := document.Components.Schemas["LoginRequest"]
	if !containsString(login.Required, "mode") || !containsString(login.Required, "password") {
		t.Fatalf("LoginRequest.required=%v, want mode and password", login.Required)
	}
	if _, ok := login.Properties["mode"]; !ok {
		t.Fatal("LoginRequest must document mode")
	}

	channel := document.Components.Schemas["ChannelRequest"]
	if !containsString(channel.Required, "auth_type") {
		t.Fatalf("ChannelRequest.required=%v, want auth_type", channel.Required)
	}
	if _, ok := channel.Properties["auth_type"]; !ok {
		t.Fatal("ChannelRequest must document auth_type")
	}
}

func TestManagementOpenAPIDocumentsModelListMetadata(t *testing.T) {
	content := mustReadRepositoryFile(t, "../../web/openapi.yaml")
	var document struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string       `yaml:"required"`
				Properties map[string]any `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}

	for _, schemaName := range []string{"OpenAIModel", "AnthropicModel"} {
		schema := document.Components.Schemas[schemaName]
		for _, field := range []string{"displayName", "provider", "thinkingLevels", "contextWindow", "maxTokens", "inputTypes"} {
			if _, ok := schema.Properties[field]; !ok {
				t.Errorf("%s must document %s", schemaName, field)
			}
		}
	}
	anthropic := document.Components.Schemas["AnthropicModel"]
	if !containsString(anthropic.Required, "display_name") {
		t.Fatalf("AnthropicModel.required=%v, want display_name", anthropic.Required)
	}
	settings := document.Components.Schemas["SettingsBatchRequest"]
	if _, ok := settings.Properties["model_metadata_overrides"]; !ok {
		t.Fatal("SettingsBatchRequest must document model_metadata_overrides")
	}
}

func TestDocsRoutesServeSwaggerUIAndOpenAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := embedFS
	embedFS = fstest.MapFS{
		"docs.html":    &fstest.MapFile{Data: []byte("<html>SwaggerUIBundle</html>")},
		"openapi.yaml": &fstest.MapFile{Data: []byte("openapi: 3.1.0\n")},
	}
	t.Cleanup(func() { embedFS = original })

	router := gin.New()
	setupDocsRoutes(router)

	for _, tc := range []struct {
		path        string
		contentType string
		body        string
	}{
		{path: "/docs/", contentType: "text/html; charset=utf-8", body: "SwaggerUIBundle"},
		{path: "/docs/openapi.yaml", contentType: "application/yaml; charset=utf-8", body: "openapi: 3.1.0"},
	} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d", tc.path, recorder.Code)
		}
		if got := recorder.Header().Get("Content-Type"); got != tc.contentType {
			t.Fatalf("GET %s Content-Type=%q, want %q", tc.path, got, tc.contentType)
		}
		if body := recorder.Body.String(); !contains(body, tc.body) {
			t.Fatalf("GET %s body=%q, want substring %q", tc.path, body, tc.body)
		}
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/docs/missing", nil)
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("GET /docs/missing status=%d, want 404", recorder.Code)
	}
}

type documentedRoute struct {
	method string
	path   string
}

func managementOpenAPIRoutes() []documentedRoute {
	return []documentedRoute{
		{"get", "/admin/channels"}, {"post", "/admin/channels"},
		{"get", "/admin/channels/filter-options"}, {"get", "/admin/channels/export"},
		{"post", "/admin/channels/import"},
		{"post", "/admin/oauth/credentials/import"}, {"post", "/admin/oauth/credentials/import/stream"},
		{"post", "/admin/oauth/credentials/import/jobs"}, {"get", "/admin/oauth/credentials/import/jobs/{id}"},
		{"post", "/admin/codex/oauth/start"}, {"get", "/admin/codex/oauth/status"},
		{"post", "/admin/codex/oauth/cancel"}, {"post", "/admin/codex/oauth/callback"},
		{"post", "/admin/codex/credentials/import"},
		{"post", "/admin/antigravity/oauth/start"}, {"get", "/admin/antigravity/oauth/status"},
		{"post", "/admin/antigravity/oauth/cancel"}, {"post", "/admin/antigravity/oauth/callback"},
		{"post", "/admin/antigravity/credentials/import"},
		{"post", "/admin/xai/oauth/start"}, {"get", "/admin/xai/oauth/status"},
		{"post", "/admin/xai/oauth/cancel"}, {"post", "/admin/xai/oauth/callback"},
		{"post", "/admin/xai/credentials/import/stream"}, {"post", "/admin/xai/credentials/import/jobs"},
		{"post", "/admin/channels/check-duplicate"},
		{"post", "/admin/channels/batch-priority"}, {"post", "/admin/channels/batch-enabled"},
		{"post", "/admin/channels/batch-advanced"}, {"post", "/admin/channels/batch-delete"},
		{"post", "/admin/channels/cooldown-detection/test"},
		{"get", "/admin/channels/{id}"}, {"put", "/admin/channels/{id}"}, {"delete", "/admin/channels/{id}"},
		{"get", "/admin/channels/{id}/editor"}, {"get", "/admin/channels/{id}/keys"},
		{"get", "/admin/channels/{id}/model-stats"},
		{"get", "/admin/channels/{id}/url-stats"}, {"post", "/admin/channels/{id}/url-disable"},
		{"post", "/admin/channels/{id}/url-enable"}, {"post", "/admin/channels/{id}/key-disable"},
		{"post", "/admin/channels/{id}/key-enable"},
		{"post", "/admin/channels/{id}/codex-credential/refresh"},
		{"post", "/admin/channels/{id}/oauth-usage"},
		{"post", "/admin/channels/{id}/antigravity-credential/refresh"},
		{"post", "/admin/channels/models/fetch"},
		{"post", "/admin/channels/billing/fetch"}, {"post", "/admin/channels/websocket-probe"},
		{"post", "/admin/channels/models/refresh-batch"}, {"get", "/admin/channels/{id}/models/fetch"},
		{"post", "/admin/channels/{id}/models"}, {"delete", "/admin/channels/{id}/models"},
		{"post", "/admin/channels/{id}/test"}, {"post", "/admin/channels/{id}/test-url"},
		{"post", "/admin/channels/{id}/chat"}, {"post", "/admin/channels/{id}/cooldown"},
		{"post", "/admin/channels/{id}/keys/{keyIndex}/cooldown"}, {"delete", "/admin/channels/{id}/keys/{keyIndex}"},
		{"get", "/admin/logs"}, {"get", "/admin/logs/bootstrap"},
		{"post", "/admin/debug-logs/merged-response"}, {"get", "/admin/debug-logs/{log_id}"},
		{"get", "/admin/active-requests"}, {"get", "/admin/runtime-metrics"},
		{"get", "/admin/active-requests/{request_id}/debug-log"}, {"get", "/admin/metrics"},
		{"get", "/admin/stats"}, {"get", "/admin/stats/filter-options"}, {"get", "/admin/models"},
		{"get", "/admin/auth-tokens"}, {"post", "/admin/auth-tokens"},
		{"put", "/admin/auth-tokens/{id}"}, {"delete", "/admin/auth-tokens/{id}"},
		{"get", "/admin/settings"}, {"get", "/admin/settings/{key}"},
		{"put", "/admin/settings/{key}"}, {"post", "/admin/settings/{key}/reset"},
		{"post", "/admin/settings/batch"}, {"get", "/admin/fingerprints"},
		{"get", "/admin/fingerprints/test-results"}, {"delete", "/admin/fingerprints/test-results/{id}"},
		{"get", "/admin/fingerprints/{id}"}, {"delete", "/admin/fingerprints/{id}"},
		{"post", "/admin/fingerprints/calibrate"}, {"post", "/admin/fingerprints/test"},
		{"get", "/admin/fingerprints/jobs/{id}"}, {"get", "/admin/fingerprints/jobs/{id}/stream"},
		{"post", "/admin/fingerprints/jobs/{id}/cancel"},
	}
}

func mustReadRepositoryFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return content
}

func contains(value, substring string) bool {
	return strings.Contains(value, substring)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
