package app

import (
	"fmt"
	"strings"
	"testing"
)

func TestModelMetadataResolverMergesOverridesWithBuiltInCatalog(t *testing.T) {
	resolver, err := newModelMetadataResolver(`{
		"gpt-5.6-sol": {
			"provider": "OpenAI Custom",
			"contextWindow": 300000,
			"maxTokens": 64000,
			"inputTypes": [" IMAGE ", "text", "image"]
		}
	}`)
	if err != nil {
		t.Fatalf("newModelMetadataResolver: %v", err)
	}

	got := resolver.Resolve(" GPT-5.6-SOL ")
	assertMetadataString(t, "provider", got.Provider, "OpenAI Custom")
	assertMetadataInt64(t, "contextWindow", got.ContextWindow, 300000)
	assertMetadataInt64(t, "maxTokens", got.MaxTokens, 64000)
	assertMetadataStrings(t, "inputTypes", got.InputTypes, []string{"image", "text"})
}

func TestModelMetadataResolverUsesBuiltInCatalogAndKnownInputTypes(t *testing.T) {
	resolver, err := newModelMetadataResolver(`{}`)
	if err != nil {
		t.Fatalf("newModelMetadataResolver: %v", err)
	}

	got := resolver.Resolve("gpt-5.6-sol")
	assertMetadataString(t, "provider", got.Provider, "OpenAI")
	assertMetadataInt64(t, "contextWindow", got.ContextWindow, 372000)
	assertMetadataInt64(t, "maxTokens", got.MaxTokens, 128000)
	assertMetadataStrings(t, "inputTypes", got.InputTypes, []string{"text"})
}

func TestModelMetadataOverridesValidation(t *testing.T) {
	tooMany := make([]string, 0, maxModelMetadataOverrides+1)
	for index := 0; index <= maxModelMetadataOverrides; index++ {
		tooMany = append(tooMany, fmt.Sprintf(`"model-%d":{"provider":"OpenAI"}`, index))
	}

	tests := []struct {
		name string
		raw  string
	}{
		{name: "top level array", raw: `[]`},
		{name: "too many", raw: `{` + strings.Join(tooMany, ",") + `}`},
		{name: "blank model", raw: `{" ":{"provider":"OpenAI"}}`},
		{name: "long model", raw: `{"` + strings.Repeat("m", maxMetadataModelNameLength+1) + `":{"provider":"OpenAI"}}`},
		{name: "duplicate normalized model", raw: `{"MODEL":{"provider":"OpenAI"}," model ":{"provider":"OpenAI"}}`},
		{name: "unknown field", raw: `{"model":{"displayName":"Model"}}`},
		{name: "blank provider", raw: `{"model":{"provider":"  "}}`},
		{name: "zero context", raw: `{"model":{"contextWindow":0}}`},
		{name: "negative max tokens", raw: `{"model":{"maxTokens":-1}}`},
		{name: "input types not array", raw: `{"model":{"inputTypes":"text"}}`},
		{name: "input types null", raw: `{"model":{"inputTypes":null}}`},
		{name: "blank input type", raw: `{"model":{"inputTypes":["text"," "]}}`},
		{name: "empty metadata object", raw: `{"model":{}}`},
		{name: "trailing json", raw: `{} {}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := newModelMetadataResolver(tt.raw); err == nil {
				t.Fatalf("newModelMetadataResolver(%s) succeeded", tt.raw)
			}
		})
	}
}

func TestModelMetadataResolveAllAggregatesEachFieldIndependently(t *testing.T) {
	resolver, err := newModelMetadataResolver(`{
		"model-a": {
			"provider": "OpenAI",
			"contextWindow": 200000,
			"maxTokens": 64000,
			"inputTypes": ["text", "image"]
		},
		"model-b": {
			"provider": "Anthropic",
			"contextWindow": 100000,
			"maxTokens": 32000,
			"inputTypes": ["text", "audio"]
		}
	}`)
	if err != nil {
		t.Fatalf("newModelMetadataResolver: %v", err)
	}

	got := resolver.ResolveAll([]string{"model-a", "model-b"})
	assertMetadataString(t, "provider", got.Provider, "mixed")
	assertMetadataInt64(t, "contextWindow", got.ContextWindow, 100000)
	assertMetadataInt64(t, "maxTokens", got.MaxTokens, 32000)
	assertMetadataStrings(t, "inputTypes", got.InputTypes, []string{"text"})

	got = resolver.ResolveAll([]string{"model-a", "unknown-model"})
	if got.Provider != nil || got.ContextWindow != nil || got.MaxTokens != nil || got.InputTypes != nil {
		t.Fatalf("unknown original must make each missing aggregate field unknown: %+v", got)
	}
}

func TestModelMetadataResolveAllPreservesExplicitEmptyInputTypes(t *testing.T) {
	resolver, err := newModelMetadataResolver(`{
		"model-a": {"inputTypes": []},
		"model-b": {"inputTypes": ["text"]}
	}`)
	if err != nil {
		t.Fatalf("newModelMetadataResolver: %v", err)
	}

	got := resolver.ResolveAll([]string{"model-a", "model-b"})
	assertMetadataStrings(t, "inputTypes", got.InputTypes, []string{})
}

func TestModelMetadataResolverSetOverridesReplacesSnapshot(t *testing.T) {
	resolver, err := newModelMetadataResolver(`{"model":{"provider":"OpenAI"}}`)
	if err != nil {
		t.Fatalf("newModelMetadataResolver: %v", err)
	}
	if err := resolver.SetOverrides(`{"model":{"provider":"Anthropic"}}`); err != nil {
		t.Fatalf("SetOverrides: %v", err)
	}

	got := resolver.Resolve("model")
	assertMetadataString(t, "provider", got.Provider, "Anthropic")
}

func assertMetadataString(t testing.TB, field string, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s=%v, want %q", field, got, want)
	}
}

func assertMetadataInt64(t testing.TB, field string, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s=%v, want %d", field, got, want)
	}
}

func assertMetadataStrings(t testing.TB, field string, got *[]string, want []string) {
	t.Helper()
	if got == nil || strings.Join(*got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("%s=%v, want %v", field, got, want)
	}
}
