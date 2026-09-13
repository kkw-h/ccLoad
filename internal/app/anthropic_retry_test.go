package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ccLoad/internal/model"
	"ccLoad/internal/protocol"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Synthetic fixtures only: no production prompts, results or credentials.
const anthropicToolRetryRequest = `{"model":"deepseek-v4-pro","max_tokens":4096,"tools":[{"name":"lookup","description":"Test lookup","input_schema":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}],"tool_choice":{"type":"auto","disable_parallel_tool_use":false},"messages":[{"role":"user","content":"Run the test lookup"},{"role":"assistant","content":[{"type":"tool_use","id":"call_a","name":"lookup","input":{"q":"a"}},{"type":"tool_use","id":"call_b","name":"lookup","input":{"q":"b"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_a","content":[{"type":"text","text":"test result"}],"is_error":false},{"type":"tool_result","tool_use_id":"call_b","content":"test failure","is_error":true}]}]}`

const anthropicToolRetryResponse = `{"id":"msg_test","type":"message","role":"assistant","model":"deepseek-v4-pro","content":[{"type":"tool_use","id":"call_next","name":"lookup","input":{"q":"next"}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`

func assertAnthropicToolContract(t testing.TB, before, after []byte) {
	t.Helper()
	for _, path := range []string{"tools", "tool_choice"} {
		if !reflect.DeepEqual(gjson.GetBytes(before, path).Value(), gjson.GetBytes(after, path).Value()) {
			t.Errorf("%s changed during recovery", path)
		}
	}
	toolHistory := func(body []byte) []any {
		var history []any
		for _, message := range gjson.GetBytes(body, "messages").Array() {
			for _, block := range message.Get("content").Array() {
				switch block.Get("type").String() {
				case "tool_use", "tool_result":
					history = append(history, message.Get("role").String(), block.Value())
				}
			}
		}
		return history
	}
	if !reflect.DeepEqual(toolHistory(before), toolHistory(after)) {
		t.Error("structured tool history changed during recovery")
	}
}

func anthropicRetryError(message string) []byte {
	body, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{
		"type": "invalid_request_error", "message": message,
	}})
	return body
}

func TestAnthropicRejectedRequestPreservesToolContract(t *testing.T) {
	t.Parallel()
	for _, message := range []string{
		"messages.1.content.0: invalid tool_use block",
		"tool_result must match tool_use",
		"tool_choice is unsupported",
		"thinking block validation failed",
	} {
		t.Run(message, func(t *testing.T) {
			body := []byte(anthropicToolRetryRequest)
			original := bytes.Clone(body)
			retry, strategy, ok := retryBodyForRejectedRequest(protocol.Anthropic, nil,
				protocol.TransformPlan{TranslatedBody: body},
				&fwResult{Status: http.StatusBadRequest, Body: anthropicRetryError(message)})
			if ok {
				assertAnthropicToolContract(t, body, retry)
				t.Errorf("unrepairable request was retried with %q", strategy)
			}
			if !bytes.Equal(body, original) {
				t.Error("retry mutated its source request")
			}
		})
	}
}

func TestAnthropicRetryPreservesExplicitThinkingDisable(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"disabled", "off", "none", " DISABLED "} {
		for _, message := range []string{"thinking block validation failed", "thinking budget_tokens must be less than max_tokens"} {
			t.Run(mode+"/"+message, func(t *testing.T) {
				body, err := sjson.Set(anthropicToolRetryRequest, "thinking.type", mode)
				if err != nil {
					t.Fatal(err)
				}
				_, strategy, ok := retryBodyForRejectedRequest(protocol.Anthropic, nil,
					protocol.TransformPlan{TranslatedBody: []byte(body)},
					&fwResult{Status: http.StatusBadRequest, Body: anthropicRetryError(message)})
				if ok {
					t.Fatalf("explicit disable retried with %q", strategy)
				}
			})
		}
	}
}

func TestProxy_AnthropicRetryPreservesTools(t *testing.T) {
	t.Parallel()
	const thinkingError = "thinking block validation failed"
	const budgetError = "thinking budget_tokens must be less than max_tokens"
	const toolError = "messages.1.content.0: invalid tool_use block"
	for _, test := range []struct {
		name          string
		thinking      string
		history       bool
		errors        []string
		wantCalls     int
		wantStatus    int
		wantThinking  string
		wantMaxTokens int64
	}{
		{name: "normal tool response", wantCalls: 1, wantStatus: 200},
		{name: "tool validation", errors: []string{toolError}, wantCalls: 1, wantStatus: 400},
		{name: "tool result validation", errors: []string{"invalid tool_result block"}, wantCalls: 1, wantStatus: 400},
		{name: "tool choice validation", errors: []string{"tool_choice is unsupported"}, wantCalls: 1, wantStatus: 400},
		{name: "thinking without repairable fields", errors: []string{thinkingError}, wantCalls: 1, wantStatus: 400},
		{name: "thinking history repair", thinking: `{"type":"enabled","budget_tokens":1024}`, history: true, errors: []string{thinkingError}, wantCalls: 2, wantStatus: 200},
		{name: "budget repair", thinking: `{"type":"enabled","budget_tokens":4096}`, errors: []string{budgetError}, wantCalls: 2, wantStatus: 200, wantThinking: "enabled", wantMaxTokens: 64000},
		{name: "disabled thinking", thinking: `{"type":"disabled"}`, errors: []string{thinkingError}, wantCalls: 1, wantStatus: 400, wantThinking: "disabled"},
		{name: "disabled budget", thinking: `{"type":"disabled"}`, errors: []string{budgetError}, wantCalls: 1, wantStatus: 400, wantThinking: "disabled"},
		{name: "disabled with stale history", thinking: `{"type":"disabled"}`, history: true, errors: []string{thinkingError}, wantCalls: 2, wantStatus: 200, wantThinking: "disabled"},
		{name: "tool rejection after thinking repair", thinking: `{"type":"enabled","budget_tokens":1024}`, errors: []string{thinkingError, toolError}, wantCalls: 2, wantStatus: 400},
		{name: "repeated thinking rejection", thinking: `{"type":"enabled","budget_tokens":1024}`, errors: []string{thinkingError, thinkingError}, wantCalls: 2, wantStatus: 400},
		{name: "budget then thinking then tool rejection", thinking: `{"type":"enabled","budget_tokens":4096}`, history: true, errors: []string{budgetError, thinkingError, toolError}, wantCalls: 3, wantStatus: 400},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", test.name, streaming), func(t *testing.T) {
				t.Parallel()
				var mu sync.Mutex
				var bodies [][]byte
				success := anthropicToolRetryResponse
				if streaming {
					success = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"deepseek-v4-pro\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
						"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_next\",\"name\":\"lookup\",\"input\":{}}}\n\n" +
						"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"q\\\":\\\"next\\\"}\"}}\n\n" +
						"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
						"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":1}}\n\n" +
						"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					mu.Lock()
					bodies = append(bodies, body)
					attempt := len(bodies)
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					if attempt <= len(test.errors) {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write(anthropicRetryError(test.errors[attempt-1]))
						return
					}
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					_, _ = io.WriteString(w, success)
				}))
				defer upstream.Close()
				env := setupProxyTestEnv(t, []testChannel{{
					name: "anthropic-tool-retry", upstreamProtocol: "anthropic", models: "deepseek-v4-pro",
				}}, map[int]string{0: upstream.URL})
				env.server.client = upstream.Client()
				var request map[string]any
				if err := json.Unmarshal([]byte(anthropicToolRetryRequest), &request); err != nil {
					t.Fatal(err)
				}
				request["stream"] = streaming
				if test.thinking != "" {
					request["thinking"] = json.RawMessage(test.thinking)
				}
				if test.history {
					messages := request["messages"].([]any)
					assistant := messages[1].(map[string]any)
					assistant["content"] = append([]any{map[string]any{
						"type": "thinking", "thinking": "test reasoning", "signature": "test signature",
					}}, assistant["content"].([]any)...)
				}
				response := doProxyRequest(t, env.engine, "/v1/messages", request, map[string]string{
					"X-App": "cli", "User-Agent": "claude-cli/2.1.220 (external, cli)",
					"Anthropic-Beta": "claude-code-20250219", "Anthropic-Version": "2023-06-01",
				})
				mu.Lock()
				defer mu.Unlock()
				if response.Code != test.wantStatus || len(bodies) != test.wantCalls {
					t.Fatalf("status=%d calls=%d, want status=%d calls=%d; response=%s", response.Code, len(bodies), test.wantStatus, test.wantCalls, response.Body.String())
				}
				for _, body := range bodies {
					assertAnthropicToolContract(t, []byte(anthropicToolRetryRequest), body)
				}
				lastBody := bodies[len(bodies)-1]
				if got := gjson.GetBytes(lastBody, "thinking.type").String(); got != test.wantThinking {
					t.Errorf("final thinking.type=%q, want %q", got, test.wantThinking)
				}
				if test.wantMaxTokens != 0 && (gjson.GetBytes(lastBody, "max_tokens").Int() != test.wantMaxTokens || gjson.GetBytes(lastBody, "thinking.budget_tokens").Int() != 32000) {
					t.Error("thinking budget was not repaired")
				}
				wantResponse := success
				if test.wantStatus == http.StatusBadRequest {
					wantResponse = string(anthropicRetryError(test.errors[test.wantCalls-1]))
				}
				if strings.TrimSpace(response.Body.String()) != strings.TrimSpace(wantResponse) {
					t.Fatalf("response changed: got %s, want %s", response.Body.String(), wantResponse)
				}
			})
		}
	}
}

func TestProxy_AnthropicRetryKeepsRejectedAttemptEvidence(t *testing.T) {
	t.Parallel()
	for _, debugEnabled := range []bool{false, true} {
		for _, finalStatus := range []int{http.StatusOK, http.StatusBadRequest, StatusClientClosedRequest} {
			t.Run(fmt.Sprintf("debug=%v/status=%d", debugEnabled, finalStatus), func(t *testing.T) {
				t.Parallel()
				const apiKey = "sk-synthetic-retry-test-secret"
				const privateEcho = "synthetic private upstream echo"
				errorBodies := [][]byte{
					anthropicRetryError("thinking budget_tokens must be less than max_tokens: " + privateEcho),
					anthropicRetryError("thinking block validation failed: " + privateEcho),
					anthropicRetryError("invalid tool_use block"),
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var mu sync.Mutex
				var bodies [][]byte
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					mu.Lock()
					bodies = append(bodies, body)
					attempt := len(bodies)
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					if attempt <= 2 {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write(errorBodies[attempt-1])
						return
					}
					switch finalStatus {
					case StatusClientClosedRequest:
						cancel()
						<-r.Context().Done()
					case http.StatusBadRequest:
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write(errorBodies[2])
					default:
						_, _ = io.WriteString(w, anthropicToolRetryResponse)
					}
				}))
				defer upstream.Close()
				env := setupProxyTestEnvWithSettings(t, []testChannel{{
					name: "anthropic-retry-evidence", upstreamProtocol: "anthropic", models: "deepseek-v4-pro", apiKey: apiKey,
				}}, map[int]string{0: upstream.URL}, map[string]string{"debug_log_enabled": fmt.Sprint(debugEnabled)})
				env.server.client = upstream.Client()
				body, err := sjson.SetRaw(anthropicToolRetryRequest, "thinking", `{"type":"enabled","budget_tokens":4096}`)
				if err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)).WithContext(ctx)
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", "Bearer test-api-key")
				request.Header.Set("X-App", "cli")
				request.Header.Set("User-Agent", "claude-cli/2.1.220 (external, cli)")
				request.Header.Set("Anthropic-Beta", "claude-code-20250219")
				response := httptest.NewRecorder()
				env.engine.ServeHTTP(response, request)
				if finalStatus != StatusClientClosedRequest && response.Code != finalStatus {
					t.Fatalf("status=%d, want %d", response.Code, finalStatus)
				}
				mu.Lock()
				defer mu.Unlock()
				if len(bodies) != 3 {
					t.Fatalf("upstream attempts=%d, want 3", len(bodies))
				}
				var entries []*model.LogEntry
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					entries, err = env.store.ListLogs(context.Background(), time.Now().Add(-time.Minute), 10, 0, &model.LogFilter{Model: "deepseek-v4-pro"})
					if err != nil {
						t.Fatal(err)
					}
					if len(entries) >= 3 {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				if len(entries) != 3 {
					t.Fatalf("persisted attempts=%d, want 3", len(entries))
				}
				seen := make(map[int]bool)
				for _, entry := range entries {
					attempt := 2
					switch entry.Message {
					case "request repair retry [rectify_anthropic_thinking_budget]":
						attempt = 0
					case "request repair retry [downgrade_anthropic_thinking]":
						attempt = 1
					}
					seen[attempt] = true
					wantStatus := http.StatusBadRequest
					if attempt == 2 {
						wantStatus = finalStatus
						if !strings.Contains(entry.Message, "rectify_anthropic_thinking_budget,downgrade_anthropic_thinking") {
							t.Error("final outcome lost its retry strategies")
						}
					}
					if entry.StatusCode != wantStatus {
						t.Errorf("attempt %d status=%d, want %d", attempt+1, entry.StatusCode, wantStatus)
					}
					if strings.Contains(entry.Message, privateEcho) || strings.Contains(entry.APIKeyUsed, apiKey) {
						t.Error("retry marker leaked content or credentials")
					}
					debug, err := env.store.GetDebugLogByLogID(context.Background(), entry.ID)
					if err != nil {
						t.Fatal(err)
					}
					if !debugEnabled {
						if debug != nil {
							t.Error("request content captured while Debug is disabled")
						}
						continue
					}
					if debug == nil || !bytes.Equal(debug.ReqBody, bodies[attempt]) {
						t.Fatalf("attempt %d lost its exact wire request", attempt+1)
					}
					if strings.Contains(debug.ReqHeaders, apiKey) || strings.Contains(debug.ReqHeaders, "test-api-key") {
						t.Error("debug request headers contain unmasked credentials")
					}
					assertAnthropicToolContract(t, bodies[0], debug.ReqBody)
					switch {
					case attempt < 2 || finalStatus == http.StatusBadRequest:
						if debug.RespStatus != http.StatusBadRequest || !bytes.Equal(debug.RespBody, errorBodies[attempt]) {
							t.Errorf("attempt %d lost its original rejection", attempt+1)
						}
					case finalStatus == http.StatusOK:
						if debug.RespStatus != http.StatusOK || string(debug.RespBody) != anthropicToolRetryResponse {
							t.Error("final success debug data was overwritten")
						}
					default:
						if debug.RespStatus != 0 || debug.UpstreamError == "" || len(debug.RespBody) != 0 {
							t.Error("cancelled attempt inherited the previous HTTP rejection")
						}
					}
				}
				if len(seen) != 3 {
					t.Fatal("retry evidence is duplicated or missing")
				}
			})
		}
	}
}

func TestProxy_AnthropicThinkingRetryPreservesExplicitDisable(t *testing.T) {
	for _, thinkingType := range []string{"disabled", "off", "none"} {
		for _, tc := range []struct {
			message           string
			contextManagement bool
			wantStatus        int
			wantRequests      int
		}{
			{message: "thinking disabled is not supported", wantStatus: http.StatusBadRequest, wantRequests: 1},
			{message: "thinking budget_tokens must be at least 1024 and less than max_tokens", wantStatus: http.StatusBadRequest, wantRequests: 1},
			{message: "thinking context_management is not supported", contextManagement: true, wantStatus: http.StatusOK, wantRequests: 2},
		} {
			t.Run(thinkingType+"/"+tc.message, func(t *testing.T) {
				requests := make(chan []byte, 8)
				upstream := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("read upstream request: %v", err)
					}
					requests <- body
					w.Header().Set("Content-Type", "application/json")
					reject := gjson.GetBytes(body, "thinking.type").String() == "disabled"
					if tc.contextManagement {
						reject = gjson.GetBytes(body, "context_management").Exists()
					}
					if reject {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"`+tc.message+`"}}`)
						return
					}
					_, _ = io.WriteString(w, `{"id":"msg-1","type":"message","role":"assistant","model":"deepseek-flash","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`)
				}))
				defer upstream.Close()
				env := setupProxyTestEnv(t, []testChannel{{
					name: "DeepSeek", upstreamProtocol: "anthropic", models: "deepseek-flash",
				}}, map[int]string{0: upstream.URL})
				body := map[string]any{
					"model": "deepseek-flash", "max_tokens": 160,
					"thinking": map[string]string{"type": thinkingType},
					"messages": []map[string]string{{"role": "user", "content": "Return one short JSON object."}},
				}
				if tc.contextManagement {
					body["context_management"] = map[string]any{"edits": []map[string]string{{"type": "clear_thinking_20251015"}}}
				}
				response := doProxyRequest(t, env.engine, "/v1/messages", body, nil)
				if response.Code != tc.wantStatus {
					t.Errorf("status=%d, want %d", response.Code, tc.wantStatus)
				}
				if response.Code == http.StatusBadRequest && gjson.GetBytes(response.Body.Bytes(), "error.message").String() != tc.message {
					t.Error("original upstream validation error was not retained")
				}
				if count := len(requests); count != tc.wantRequests {
					t.Errorf("upstream requests=%d, want %d", count, tc.wantRequests)
				}
				for len(requests) > 0 {
					body := <-requests
					if got := gjson.GetBytes(body, "thinking.type").String(); got != "disabled" {
						t.Errorf("upstream thinking.type=%q, want disabled", got)
					}
					if got := gjson.GetBytes(body, "max_tokens").Int(); got != 160 {
						t.Errorf("upstream max_tokens=%d, want caller budget 160", got)
					}
				}
			})
		}
	}
}
