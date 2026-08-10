package registry

import "testing"

func TestLookupModelInfoIncludesPublicMetadata(t *testing.T) {
	info := LookupModelInfo("gpt-5.6-sol", "openai")
	if info == nil {
		t.Fatal("model not found")
	}
	if info.OwnedBy != "openai" || info.DisplayName != "GPT 5.6 Sol" {
		t.Fatalf("identity metadata=%+v", info)
	}
	if info.ContextLength != 372000 || info.MaxCompletionTokens != 128000 {
		t.Fatalf("token metadata=%+v", info)
	}
	if len(info.SupportedParameters) != 1 || info.SupportedParameters[0] != "tools" {
		t.Fatalf("supported parameters=%v", info.SupportedParameters)
	}

	info.SupportedParameters[0] = "mutated"
	fresh := LookupModelInfo("gpt-5.6-sol", "openai")
	if fresh.SupportedParameters[0] != "tools" {
		t.Fatal("lookup leaked mutable slice")
	}
}
