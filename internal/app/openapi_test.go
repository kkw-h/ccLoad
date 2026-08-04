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
}

type documentedRoute struct {
	method string
	path   string
}

func managementOpenAPIRoutes() []documentedRoute {
	return []documentedRoute{
		{"get", "/admin/channels"}, {"post", "/admin/channels"},
		{"get", "/admin/channels/filter-options"}, {"get", "/admin/channels/export"},
		{"post", "/admin/channels/import"}, {"post", "/admin/channels/check-duplicate"},
		{"post", "/admin/channels/batch-priority"}, {"post", "/admin/channels/batch-enabled"},
		{"post", "/admin/channels/batch-protocol-mode"}, {"post", "/admin/channels/batch-delete"},
		{"post", "/admin/channels/cooldown-detection/test"},
		{"get", "/admin/channels/{id}"}, {"put", "/admin/channels/{id}"}, {"delete", "/admin/channels/{id}"},
		{"get", "/admin/channels/{id}/keys"}, {"get", "/admin/channels/{id}/model-stats"},
		{"get", "/admin/channels/{id}/url-stats"}, {"post", "/admin/channels/{id}/url-disable"},
		{"post", "/admin/channels/{id}/url-enable"}, {"post", "/admin/channels/{id}/key-disable"},
		{"post", "/admin/channels/{id}/key-enable"}, {"post", "/admin/channels/models/fetch"},
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
		{"post", "/admin/settings/batch"}, {"get", "/admin/external-auth/environments"},
		{"post", "/admin/external-auth/environments"}, {"put", "/admin/external-auth/environments/{id}"},
		{"delete", "/admin/external-auth/environments/{id}"}, {"get", "/admin/fingerprints"},
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
