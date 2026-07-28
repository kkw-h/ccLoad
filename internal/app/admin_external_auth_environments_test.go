package app

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"ccLoad/internal/model"

	"github.com/gin-gonic/gin"
)

func TestAdminExternalAuthEnvironmentCRUDAndSnapshot(t *testing.T) {
	server, _, cleanup := setupAdminTestServer(t)
	defer cleanup()
	server.externalAuthResolver = staticExternalAuthResolver{
		addrs: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}},
	}
	legacy, err := url.Parse("https://legacy.example.com/internal/llm/authz")
	if err != nil {
		t.Fatal(err)
	}
	server.externalAuthService = newExternalAuthService(externalAuthConfig{
		Enabled:    true,
		WebhookURL: legacy,
		Timeout:    time.Second,
	}, newTestHTTPClient(), nil)

	createCtx, createW := newTestContext(t, newJSONRequest(t, http.MethodPost, "/admin/external-auth/environments", map[string]any{
		"environment": "develop",
		"authz_url":   "https://sedna-dev.example.com/internal/llm/authz",
		"is_active":   true,
	}))
	server.AdminCreateExternalAuthEnvironment(createCtx)
	if createW.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createW.Code, createW.Body.String())
	}

	target, err := server.externalAuthService.environmentTarget("develop")
	if err != nil || target.AuthzURL.String() != "https://sedna-dev.example.com/internal/llm/authz" {
		t.Fatalf("runtime target=%#v err=%v", target, err)
	}

	listCtx, listW := newTestContext(t, newRequest(http.MethodGet, "/admin/external-auth/environments", nil))
	server.AdminListExternalAuthEnvironments(listCtx)
	if listW.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listW.Code, listW.Body.String())
	}

	items, err := server.store.ListExternalAuthEnvironments(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("stored items=%#v err=%v", items, err)
	}
	id := items[0].ID

	updateCtx, updateW := newTestContext(t, newJSONRequest(t, http.MethodPut, "/admin/external-auth/environments/1", map[string]any{
		"environment": "develop",
		"authz_url":   "https://sedna-dev.example.com/v2/authz",
		"is_active":   false,
	}))
	updateCtx.Params = gin.Params{{Key: "id", Value: "1"}}
	if id != 1 {
		updateCtx.Params[0].Value = "999"
	}
	server.AdminUpdateExternalAuthEnvironment(updateCtx)
	if updateW.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s id=%d", updateW.Code, updateW.Body.String(), id)
	}
	if _, err := server.externalAuthService.environmentTarget("develop"); !isExternalAuthErrorKind(err, externalAuthErrorDenied) {
		t.Fatalf("inactive environment error=%v, want denied", err)
	}

	deleteCtx, deleteW := newTestContext(t, newRequest(http.MethodDelete, "/admin/external-auth/environments/1", nil))
	deleteCtx.Params = gin.Params{{Key: "id", Value: "1"}}
	server.AdminDeleteExternalAuthEnvironment(deleteCtx)
	if deleteW.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleteW.Code, deleteW.Body.String())
	}
}

func TestAdminCreateExternalAuthEnvironmentValidation(t *testing.T) {
	server, _, cleanup := setupAdminTestServer(t)
	defer cleanup()
	server.externalAuthResolver = staticExternalAuthResolver{
		addrs: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}},
	}

	tests := []struct {
		name        string
		environment string
		authzURL    string
	}{
		{name: "uppercase", environment: "Develop", authzURL: "https://auth.example.com/check"},
		{name: "plain HTTP", environment: "develop", authzURL: "http://auth.example.com/check"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, w := newTestContext(t, newJSONRequest(t, http.MethodPost, "/admin/external-auth/environments", &model.ExternalAuthEnvironment{
				Environment: tt.environment,
				AuthzURL:    tt.authzURL,
				IsActive:    true,
			}))
			server.AdminCreateExternalAuthEnvironment(c)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
