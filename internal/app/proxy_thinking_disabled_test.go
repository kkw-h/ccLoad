package app

import (
	"io"
	"net/http"
	"testing"

	"github.com/tidwall/gjson"
)

// Assert what an actual HTTP upstream receives after routing, translation,
// thinking suffixes, and the final Anthropic wire normalization have all run.
func TestProxy_ExplicitThinkingDisabledReachesUpstream(t *testing.T) {
	tests := []struct {
		name             string
		model            string
		upstreamProtocol string
		thinkingType     string
		suffix           string
		forcedTool       bool
		openAIClient     bool
	}{
		{name: "deepseek native disabled", model: "deepseek-v4-flash", upstreamProtocol: "anthropic", thinkingType: "disabled"},
		{name: "deepseek native off alias", model: "deepseek-v4-flash", upstreamProtocol: "anthropic", thinkingType: "off"},
		{name: "deepseek native none alias", model: "deepseek-v4-flash", upstreamProtocol: "anthropic", thinkingType: "none"},
		{name: "deepseek native suffix", model: "deepseek-v4-flash", upstreamProtocol: "anthropic", thinkingType: "enabled", suffix: "(none)"},
		{name: "deepseek translated disabled", model: "deepseek-v4-flash", upstreamProtocol: "openai", thinkingType: "disabled"},
		{name: "deepseek translated suffix", model: "deepseek-v4-flash", upstreamProtocol: "openai", thinkingType: "enabled", suffix: "(none)"},
		{name: "claude native disabled", model: "claude-sonnet-4-5", upstreamProtocol: "anthropic", thinkingType: "disabled"},
		{name: "claude native suffix", model: "claude-sonnet-4-5", upstreamProtocol: "anthropic", thinkingType: "enabled", suffix: "(none)"},
		{name: "claude forced tool disabled", model: "claude-sonnet-4-5", upstreamProtocol: "anthropic", thinkingType: "disabled", forcedTool: true},
		{name: "anyrouter default respects disabled", model: "deepseek-v4-flash", upstreamProtocol: "anthropic", thinkingType: "disabled"},
		{name: "anyrouter default respects suffix", model: "deepseek-v4-flash", upstreamProtocol: "anthropic", thinkingType: "enabled", suffix: "(none)"},
		{name: "openai native none", model: "deepseek-v4-flash", upstreamProtocol: "openai", openAIClient: true},
		{name: "openai translated none", model: "deepseek-v4-flash", upstreamProtocol: "anthropic", openAIClient: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestBody := make(chan []byte, 1)
			upstream := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read upstream request: %v", err)
				}
				requestBody <- body
				w.Header().Set("Content-Type", "application/json")
				if tt.upstreamProtocol == "openai" {
					_, _ = io.WriteString(w, `{"id":"chat-1","object":"chat.completion","model":"deepseek-v4-flash","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
				} else {
					_, _ = io.WriteString(w, `{"id":"msg-1","type":"message","role":"assistant","model":"test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
				}
			}))
			defer upstream.Close()
			env := setupProxyTestEnv(t, []testChannel{{
				name: tt.name, upstreamProtocol: tt.upstreamProtocol, models: tt.model,
			}}, map[int]string{0: upstream.URL})
			body := map[string]any{
				"model": tt.model + tt.suffix, "max_tokens": 160,
				"messages":      []map[string]string{{"role": "user", "content": "Return one short JSON object."}},
				"thinking":      map[string]any{"type": tt.thinkingType, "budget_tokens": 1024},
				"output_config": map[string]any{"effort": "high", "format": map[string]any{"type": "json_schema", "schema": map[string]any{"type": "object"}}},
			}
			if tt.forcedTool {
				body["tool_choice"] = map[string]string{"type": "tool", "name": "answer"}
				body["tools"] = []map[string]any{{"name": "answer", "input_schema": map[string]any{"type": "object"}}}
			}
			requestPath := "/v1/messages"
			if tt.openAIClient {
				requestPath = "/v1/chat/completions"
				delete(body, "thinking")
				delete(body, "output_config")
				body["reasoning_effort"] = "none"
			}
			response := doProxyRequest(t, env.engine, requestPath, body, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var outbound []byte
			select {
			case outbound = <-requestBody:
			default:
				t.Fatal("upstream received no request")
			}
			path, want := "thinking.type", "disabled"
			if tt.upstreamProtocol == "openai" {
				path, want = "reasoning_effort", "none"
			}
			if got := gjson.GetBytes(outbound, path).String(); got != want {
				t.Errorf("upstream %s=%q, want %q; explicit disable was lost", path, got, want)
			}
			if tt.upstreamProtocol == "anthropic" {
				for _, key := range []string{"thinking.budget_tokens", "output_config.effort", "context_management"} {
					if gjson.GetBytes(outbound, key).Exists() {
						t.Errorf("disabled thinking retained incompatible %s", key)
					}
				}
				if !tt.openAIClient && !gjson.GetBytes(outbound, "output_config.format").Exists() {
					t.Error("removing thinking effort discarded the caller's output format")
				}
			}
		})
	}
}
