package app

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"ccLoad/internal/model"
)

func TestProxyGemini_ListModelsHandlers(t *testing.T) {
	server, store, cleanup := setupAdminTestServer(t)
	defer cleanup()

	ctx := context.Background()

	createModelConfig := func(t testing.TB, name, upstreamProtocol string, _ []string, priority int, modelName string) {
		t.Helper()
		_, err := store.CreateConfig(ctx, &model.Config{
			Name:     name,
			URLs:     model.ChannelURLs{{URL: "https://example.com"}},
			Priority: priority,
			Enabled:  true,
			ModelEntries: []model.ModelEntry{
				{Model: modelName},
			},
		})
		if err != nil {
			t.Fatalf("CreateConfig %s failed: %v", name, err)
		}
	}

	assertOpenAIModelListed := func(t testing.TB, req *http.Request, wantID, failurePrefix string) {
		t.Helper()
		c, w := newTestContext(t, req)

		server.handleListOpenAIModels(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
		}

		var resp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		mustUnmarshalJSON(t, w.Body.Bytes(), &resp)
		for _, item := range resp.Data {
			if item.ID == wantID {
				return
			}
		}
		t.Fatalf("%s, got %+v", failurePrefix, resp.Data)
	}

	assertGeminiModelListed := func(t testing.TB, wantName, failurePrefix string) {
		t.Helper()
		c, w := newTestContext(t, newRequest(http.MethodGet, "/v1beta/models", nil))

		server.handleListGeminiModels(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
		}

		var resp struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		mustUnmarshalJSON(t, w.Body.Bytes(), &resp)
		for _, item := range resp.Models {
			if item.Name == wantName {
				return
			}
		}
		t.Fatalf("%s, got %+v", failurePrefix, resp.Models)
	}

	_, err := store.CreateConfig(ctx, &model.Config{
		Name:     "g1",
		URLs:     model.ChannelURLs{{URL: "https://example.com"}},
		Priority: 1,
		Enabled:  true,
		ModelEntries: []model.ModelEntry{
			{Model: "gemini-2.5-flash-20250101"},
			{Model: "gemini-1.5-pro"},
		},
	})
	if err != nil {
		t.Fatalf("CreateConfig gemini failed: %v", err)
	}
	_, err = store.CreateConfig(ctx, &model.Config{
		Name:     "o1",
		URLs:     model.ChannelURLs{{URL: "https://example.com"}},
		Priority: 1,
		Enabled:  true,
		ModelEntries: []model.ModelEntry{
			{Model: "gpt-4o"},
		},
	})
	if err != nil {
		t.Fatalf("CreateConfig openai failed: %v", err)
	}

	t.Run("handleListGeminiModels", func(t *testing.T) {
		c, w := newTestContext(t, newRequest(http.MethodGet, "/v1beta/models", nil))

		server.handleListGeminiModels(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
		}

		var resp struct {
			Models []struct {
				Name        string `json:"name"`
				DisplayName string `json:"displayName"`
			} `json:"models"`
		}
		mustUnmarshalJSON(t, w.Body.Bytes(), &resp)
		if len(resp.Models) != 3 {
			t.Fatalf("models len=%d, want all 3 enabled models", len(resp.Models))
		}
		seen := make(map[string]bool, len(resp.Models))
		for _, m := range resp.Models {
			if m.Name == "" || m.DisplayName == "" {
				t.Fatalf("bad model entry: %+v", m)
			}
			if m.Name[:7] != "models/" {
				t.Fatalf("expected gemini name prefix models/, got %q", m.Name)
			}
			seen[m.Name] = true
		}
		for _, want := range []string{"models/gemini-1.5-pro", "models/gemini-2.5-flash-20250101", "models/gpt-4o"} {
			if !seen[want] {
				t.Fatalf("missing model %q: %+v", want, resp.Models)
			}
		}
	})

	t.Run("handleListOpenAIModels", func(t *testing.T) {
		c, w := newTestContext(t, newRequest(http.MethodGet, "/v1/models", nil))

		server.handleListOpenAIModels(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
		}

		var resp struct {
			Object string `json:"object"`
			Data   []struct {
				ID     string `json:"id"`
				Object string `json:"object"`
			} `json:"data"`
		}
		mustUnmarshalJSON(t, w.Body.Bytes(), &resp)
		if resp.Object != "list" || len(resp.Data) != 3 {
			t.Fatalf("unexpected resp: %+v", resp)
		}
		seen := make(map[string]bool, len(resp.Data))
		for _, item := range resp.Data {
			seen[item.ID] = true
		}
		for _, want := range []string{"gemini-1.5-pro", "gemini-2.5-flash-20250101", "gpt-4o"} {
			if !seen[want] {
				t.Fatalf("missing model %q: %+v", want, resp)
			}
		}
	})

	t.Run("handleListOpenAIModels filters by token allowed models", func(t *testing.T) {
		server.authService = newTestAuthService(t)
		tokenHash := model.HashToken("restricted-openai-token")
		server.authService.authTokensMux.Lock()
		server.authService.authTokenModels[tokenHash] = []string{"gpt-4o"}
		server.authService.authTokensMux.Unlock()

		_, err := store.CreateConfig(ctx, &model.Config{
			Name:     "o2",
			URLs:     model.ChannelURLs{{URL: "https://example.com"}},
			Priority: 2,
			Enabled:  true,
			ModelEntries: []model.ModelEntry{
				{Model: "gpt-5"},
			},
		})
		if err != nil {
			t.Fatalf("CreateConfig openai extra failed: %v", err)
		}

		c, w := newTestContext(t, newRequest(http.MethodGet, "/v1/models", nil))
		c.Set("token_hash", tokenHash)

		server.handleListOpenAIModels(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
		}

		var resp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		mustUnmarshalJSON(t, w.Body.Bytes(), &resp)
		if len(resp.Data) != 1 || resp.Data[0].ID != "gpt-4o" {
			t.Fatalf("unexpected filtered resp: %+v", resp)
		}
	})

	t.Run("handleListOpenAIModels hides models only provided by denied channels", func(t *testing.T) {
		server.authService = newTestAuthService(t)

		_, err := store.CreateConfig(ctx, &model.Config{
			Name:     "allowed-model-list-channel",
			URLs:     model.ChannelURLs{{URL: "https://example.com"}},
			Priority: 3,
			Enabled:  true,
			ModelEntries: []model.ModelEntry{
				{Model: "gpt-allowed-channel"},
			},
		})
		if err != nil {
			t.Fatalf("CreateConfig allowed channel failed: %v", err)
		}
		denied, err := store.CreateConfig(ctx, &model.Config{
			Name:     "disallowed-model-list-channel",
			URLs:     model.ChannelURLs{{URL: "https://example.com"}},
			Priority: 4,
			Enabled:  true,
			ModelEntries: []model.ModelEntry{
				{Model: "gpt-disallowed-channel"},
			},
		})
		if err != nil {
			t.Fatalf("CreateConfig disallowed channel failed: %v", err)
		}

		tokenHash := model.HashToken("channel-restricted-openai-token")
		server.authService.authTokensMux.Lock()
		server.authService.authTokenChannels[tokenHash] = mustChannelRestriction(t, model.ChannelRestrictionModeDeny, denied.ID)
		server.authService.authTokensMux.Unlock()

		c, w := newTestContext(t, newRequest(http.MethodGet, "/v1/models", nil))
		c.Set("token_hash", tokenHash)

		server.handleListOpenAIModels(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
		}

		var resp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		mustUnmarshalJSON(t, w.Body.Bytes(), &resp)
		visible := make(map[string]bool, len(resp.Data))
		for _, item := range resp.Data {
			visible[item.ID] = true
		}
		if visible["gpt-disallowed-channel"] {
			t.Fatalf("model provided only by denied channel is visible: %+v", resp)
		}
		if !visible["gpt-allowed-channel"] {
			t.Fatalf("model provided by allowed channel is missing: %+v", resp)
		}
	})

	t.Run("handleListOpenAIModels includes transformed gemini channel", func(t *testing.T) {
		createModelConfig(t, "g2-oai", "gemini", []string{"openai"}, 3, "gemini-2.5-pro")
		assertOpenAIModelListed(t, newRequest(http.MethodGet, "/v1/models", nil), "gemini-2.5-pro", "expected transformed gemini model in openai model list")
	})

	t.Run("handleListOpenAIModels returns codex view for openai upstream with codex transform", func(t *testing.T) {
		_, err := store.CreateConfig(ctx, &model.Config{
			Name:     "o2-codex",
			URLs:     model.ChannelURLs{{URL: "https://example.com"}},
			Priority: 3,
			Enabled:  true,
			ModelEntries: []model.ModelEntry{
				{Model: "gpt-5-codex"},
			},
		})
		if err != nil {
			t.Fatalf("CreateConfig openai->codex failed: %v", err)
		}

		req := newRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("User-Agent", "codex-cli/1.0")
		c, w := newTestContext(t, req)

		server.handleListOpenAIModels(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
		}

		var resp struct {
			Data []struct {
				ID                string `json:"id"`
				MultiAgentVersion string `json:"multi_agent_version"`
			} `json:"data"`
		}
		mustUnmarshalJSON(t, w.Body.Bytes(), &resp)
		found := false
		for _, item := range resp.Data {
			if item.ID == "gpt-5-codex" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected codex-exposed openai model in list, got %+v", resp.Data)
		}
		for _, item := range resp.Data {
			if item.ID == "gpt-5-codex" && item.MultiAgentVersion != "v2" {
				t.Fatalf("multi_agent_version = %q, want v2", item.MultiAgentVersion)
			}
		}
	})

	t.Run("handleListOpenAIModels returns openai view for codex upstream with openai transform", func(t *testing.T) {
		createModelConfig(t, "c2-openai", "codex", []string{"openai"}, 3, "gpt-4o")
		assertOpenAIModelListed(t, newRequest(http.MethodGet, "/v1/models", nil), "gpt-4o", "expected openai-exposed codex model in list")
	})

	t.Run("handleListGeminiModels exposes openai channels that declare gemini transform", func(t *testing.T) {
		createModelConfig(t, "o2-gemini", "openai", []string{"gemini"}, 4, "gpt-4.1")
		assertGeminiModelListed(t, "models/gpt-4.1", "expected transformed openai gemini model in list")
	})

	t.Run("handleListOpenAIModels returns anthropic style for anthropic view", func(t *testing.T) {
		_, err := store.CreateConfig(ctx, &model.Config{
			Name:     "g3-anthropic",
			URLs:     model.ChannelURLs{{URL: "https://example.com"}},
			Priority: 5,
			Enabled:  true,
			ModelEntries: []model.ModelEntry{
				{Model: "claude-3-5-sonnet"},
			},
		})
		if err != nil {
			t.Fatalf("CreateConfig transformed anthropic failed: %v", err)
		}

		req := newRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("anthropic-version", "2023-06-01")
		c, w := newTestContext(t, req)

		server.handleListOpenAIModels(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
		}

		var resp struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
				Type        string `json:"type"`
				CreatedAt   string `json:"created_at"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			FirstID string `json:"first_id"`
			LastID  string `json:"last_id"`
		}
		mustUnmarshalJSON(t, w.Body.Bytes(), &resp)
		if resp.HasMore {
			t.Fatalf("expected has_more=false, got true")
		}
		if len(resp.Data) == 0 {
			t.Fatalf("expected anthropic models, got empty response")
		}
		found := false
		for _, item := range resp.Data {
			if item.ID == "claude-3-5-sonnet" {
				found = true
				if item.Type != "model" {
					t.Fatalf("expected anthropic type=model, got %q", item.Type)
				}
				if item.DisplayName == "" || item.CreatedAt == "" {
					t.Fatalf("expected anthropic display_name/created_at, got %+v", item)
				}
				break
			}
		}
		if !found {
			t.Fatalf("expected transformed anthropic model in anthropic view, got %+v", resp.Data)
		}
		if resp.FirstID == "" || resp.LastID == "" {
			t.Fatalf("expected anthropic pagination ids, got first=%q last=%q", resp.FirstID, resp.LastID)
		}
	})

	t.Run("handleListOpenAIModels keeps openai shape for codex view", func(t *testing.T) {
		_, err := store.CreateConfig(ctx, &model.Config{
			Name:     "c1",
			URLs:     model.ChannelURLs{{URL: "https://example.com"}},
			Priority: 6,
			Enabled:  true,
			ModelEntries: []model.ModelEntry{
				{Model: "gpt-5-codex"},
			},
		})
		if err != nil {
			t.Fatalf("CreateConfig codex failed: %v", err)
		}

		req := newRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("User-Agent", "codex-cli/1.0")
		c, w := newTestContext(t, req)

		server.handleListOpenAIModels(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
		}

		var resp struct {
			Object string `json:"object"`
			Data   []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		mustUnmarshalJSON(t, w.Body.Bytes(), &resp)
		if resp.Object != "list" {
			t.Fatalf("expected openai-style list object for codex view, got %+v", resp)
		}
		found := false
		for _, item := range resp.Data {
			if item.ID == "gpt-5-codex" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected codex model in openai-shaped view, got %+v", resp.Data)
		}
	})

	t.Run("handleListGeminiModels filters by token allowed models", func(t *testing.T) {
		server.authService = newTestAuthService(t)
		tokenHash := model.HashToken("restricted-gemini-token")
		server.authService.authTokensMux.Lock()
		server.authService.authTokenModels[tokenHash] = []string{"gemini-1.5-pro"}
		server.authService.authTokensMux.Unlock()

		c, w := newTestContext(t, newRequest(http.MethodGet, "/v1beta/models", nil))
		c.Set("token_hash", tokenHash)

		server.handleListGeminiModels(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
		}

		var resp struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		mustUnmarshalJSON(t, w.Body.Bytes(), &resp)
		if len(resp.Models) != 1 || resp.Models[0].Name != "models/gemini-1.5-pro" {
			t.Fatalf("unexpected filtered resp: %+v", resp)
		}
	})
}

func TestProxyModelsExposeReasoningCapabilitiesForOpenAIAndAnthropic(t *testing.T) {
	server, store, cleanup := setupAdminTestServer(t)
	defer cleanup()
	resolver, err := newModelReasoningCapabilityResolver(`{}`)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	server.modelReasoningCapabilities = resolver

	_, err = store.CreateConfig(context.Background(), &model.Config{
		Name:         "reasoning-model-channel",
		URLs:         model.ChannelURLs{{URL: "https://example.com"}},
		Priority:     1,
		Enabled:      true,
		ModelEntries: []model.ModelEntry{{Model: "sciland-3.0", RedirectModel: "gpt-5.6-sol"}},
	})
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}

	tests := []struct {
		name    string
		headers map[string]string
	}{
		{name: "openai"},
		{name: "codex", headers: map[string]string{"User-Agent": "codex-cli/1.0"}},
		{name: "anthropic", headers: map[string]string{"anthropic-version": "2023-06-01"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newRequest(http.MethodGet, "/v1/models", nil)
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}
			c, w := newTestContext(t, req)
			server.handleListOpenAIModels(c)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			efforts := modelReasoningEffortsFromResponse(t, w.Body.Bytes(), "sciland-3.0")
			if efforts == nil || !reflect.DeepEqual(*efforts, []string{"low", "medium", "high", "xhigh"}) {
				t.Fatalf("efforts=%#v", efforts)
			}
		})
	}
}

func TestProxyModelsReasoningCapabilitiesIntersectionUnknownAndEmpty(t *testing.T) {
	server, store, cleanup := setupAdminTestServer(t)
	defer cleanup()
	resolver, err := newModelReasoningCapabilityResolver(`{
		"upstream-a":["low","medium","high"],
		"upstream-b":["medium","high","xhigh"],
		"no-reasoning":[]
	}`)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	server.modelReasoningCapabilities = resolver

	configs := []*model.Config{
		{Name: "intersection-a", URLs: model.ChannelURLs{{URL: "https://a.example.com"}}, Priority: 1, Enabled: true, ModelEntries: []model.ModelEntry{{Model: "shared-model", RedirectModel: "upstream-a"}}},
		{Name: "intersection-b", URLs: model.ChannelURLs{{URL: "https://b.example.com"}}, Priority: 2, Enabled: true, ModelEntries: []model.ModelEntry{{Model: "shared-model", RedirectModel: "upstream-b"}}},
		{Name: "unknown", URLs: model.ChannelURLs{{URL: "https://unknown.example.com"}}, Priority: 3, Enabled: true, ModelEntries: []model.ModelEntry{{Model: "unknown-model", RedirectModel: "unknown-upstream"}}},
		{Name: "empty", URLs: model.ChannelURLs{{URL: "https://empty.example.com"}}, Priority: 4, Enabled: true, ModelEntries: []model.ModelEntry{{Model: "empty-model", RedirectModel: "no-reasoning"}}},
	}
	for _, cfg := range configs {
		if _, err := store.CreateConfig(context.Background(), cfg); err != nil {
			t.Fatalf("CreateConfig %s: %v", cfg.Name, err)
		}
	}

	c, w := newTestContext(t, newRequest(http.MethodGet, "/v1/models", nil))
	server.handleListOpenAIModels(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	shared := modelReasoningEffortsFromResponse(t, w.Body.Bytes(), "shared-model")
	if shared == nil || !reflect.DeepEqual(*shared, []string{"medium", "high"}) {
		t.Fatalf("shared efforts=%#v", shared)
	}
	if unknown := modelReasoningEffortsFromResponse(t, w.Body.Bytes(), "unknown-model"); unknown != nil {
		t.Fatalf("unknown efforts must be omitted, got %#v", *unknown)
	}
	empty := modelReasoningEffortsFromResponse(t, w.Body.Bytes(), "empty-model")
	if empty == nil || len(*empty) != 0 {
		t.Fatalf("explicit empty efforts=%#v", empty)
	}
}

func TestProxyModelsReasoningCapabilitiesRespectTokenChannelRestrictions(t *testing.T) {
	server, store, cleanup := setupAdminTestServer(t)
	defer cleanup()
	resolver, err := newModelReasoningCapabilityResolver(`{
		"allowed-upstream":["low","high"],
		"denied-upstream":["medium"]
	}`)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	server.modelReasoningCapabilities = resolver
	server.authService = newTestAuthService(t)

	if _, err := store.CreateConfig(context.Background(), &model.Config{
		Name: "allowed", URLs: model.ChannelURLs{{URL: "https://allowed.example.com"}}, Priority: 1, Enabled: true,
		ModelEntries: []model.ModelEntry{{Model: "restricted-model", RedirectModel: "allowed-upstream"}},
	}); err != nil {
		t.Fatalf("create allowed channel: %v", err)
	}
	denied, err := store.CreateConfig(context.Background(), &model.Config{
		Name: "denied", URLs: model.ChannelURLs{{URL: "https://denied.example.com"}}, Priority: 2, Enabled: true,
		ModelEntries: []model.ModelEntry{{Model: "restricted-model", RedirectModel: "denied-upstream"}},
	})
	if err != nil {
		t.Fatalf("create denied channel: %v", err)
	}

	tokenHash := model.HashToken("reasoning-channel-restricted-token")
	server.authService.authTokensMux.Lock()
	server.authService.authTokenChannels[tokenHash] = mustChannelRestriction(t, model.ChannelRestrictionModeDeny, denied.ID)
	server.authService.authTokensMux.Unlock()

	c, w := newTestContext(t, newRequest(http.MethodGet, "/v1/models", nil))
	c.Set("token_hash", tokenHash)
	server.handleListOpenAIModels(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	efforts := modelReasoningEffortsFromResponse(t, w.Body.Bytes(), "restricted-model")
	if efforts == nil || !reflect.DeepEqual(*efforts, []string{"low", "high"}) {
		t.Fatalf("restricted efforts=%#v", efforts)
	}
}

func modelReasoningEffortsFromResponse(t testing.TB, body []byte, modelID string) *[]string {
	t.Helper()
	var response struct {
		Data []struct {
			ID                        string    `json:"id"`
			SupportedReasoningEfforts *[]string `json:"supported_reasoning_efforts"`
		} `json:"data"`
	}
	mustUnmarshalJSON(t, body, &response)
	for _, item := range response.Data {
		if item.ID == modelID {
			return item.SupportedReasoningEfforts
		}
	}
	t.Fatalf("model %q missing from response: %s", modelID, body)
	return nil
}
