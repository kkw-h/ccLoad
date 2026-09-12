package app

import (
	"io"
	"net/http"
	"testing"

	"github.com/tidwall/gjson"
)

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
