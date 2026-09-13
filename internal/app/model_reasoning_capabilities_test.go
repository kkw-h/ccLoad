package app

import (
	"reflect"
	"strings"
	"testing"
)

func TestModelReasoningCapabilityResolverBuiltInAndOverrides(t *testing.T) {
	resolver, err := newModelReasoningCapabilityResolver(`{
		"gpt-5.6-sol":["HIGH","low","high"],
		"no-reasoning":[]
	}`)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}

	got, known := resolver.Resolve("gpt-5.6-sol")
	assertReasoningEfforts(t, got, known, []string{"low", "high"}, true)
	got, known = resolver.Resolve(" no-reasoning ")
	assertReasoningEfforts(t, got, known, []string{}, true)
	got, known = resolver.Resolve("unknown-model")
	assertReasoningEfforts(t, got, known, nil, false)

	if err := resolver.SetOverrides(`{}`); err != nil {
		t.Fatalf("clear overrides: %v", err)
	}
	got, known = resolver.Resolve("GPT-5.6-SOL")
	assertReasoningEfforts(t, got, known, []string{"low", "medium", "high", "xhigh"}, true)
}

func TestModelReasoningCapabilityResolverReturnsIndependentSlices(t *testing.T) {
	resolver, err := newModelReasoningCapabilityResolver(`{}`)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}

	got, known := resolver.Resolve("gpt-5.6-sol")
	if !known || len(got) == 0 {
		t.Fatalf("built-in resolve = %v/%v", got, known)
	}
	got[0] = "mutated"
	got, known = resolver.Resolve("gpt-5.6-sol")
	assertReasoningEfforts(t, got, known, []string{"low", "medium", "high", "xhigh"}, true)
}

func TestModelReasoningCapabilityResolverResolveAll(t *testing.T) {
	resolver, err := newModelReasoningCapabilityResolver(`{
		"upstream-a":["low","medium","high"],
		"upstream-b":["medium","high","xhigh"],
		"no-common":["none"]
	}`)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}

	got, known := resolver.ResolveAll([]string{"upstream-a", "upstream-b"})
	assertReasoningEfforts(t, got, known, []string{"medium", "high"}, true)
	got, known = resolver.ResolveAll([]string{"upstream-a", "unknown"})
	assertReasoningEfforts(t, got, known, nil, false)
	got, known = resolver.ResolveAll([]string{"upstream-a", "no-common"})
	assertReasoningEfforts(t, got, known, []string{}, true)
	got, known = resolver.ResolveAll(nil)
	assertReasoningEfforts(t, got, known, nil, false)
}

func TestParseModelReasoningEffortOverridesRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "array top level", raw: `[]`, wantErr: "JSON object"},
		{name: "null top level", raw: `null`, wantErr: "JSON object"},
		{name: "empty model", raw: `{" ":["low"]}`, wantErr: "model name"},
		{name: "long model", raw: `{"` + strings.Repeat("m", 256) + `":["low"]}`, wantErr: "255"},
		{name: "non array", raw: `{"model":"low"}`, wantErr: "array"},
		{name: "unknown effort", raw: `{"model":["ultra"]}`, wantErr: "unknown reasoning effort"},
		{name: "exact duplicate", raw: `{"model":["low"],"model":["high"]}`, wantErr: "duplicate model"},
		{name: "normalized duplicate", raw: `{"Model":["low"],"model":["high"]}`, wantErr: "duplicate model"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseModelReasoningEffortOverrides(tt.raw)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseModelReasoningEffortOverridesCountsUnicodeCodePoints(t *testing.T) {
	modelName := strings.Repeat("模", maxReasoningModelNameLength)
	overrides, err := parseModelReasoningEffortOverrides(`{"` + modelName + `":["low"]}`)
	if err != nil {
		t.Fatalf("255-code-point model name rejected: %v", err)
	}
	if _, ok := overrides[modelName]; !ok {
		t.Fatalf("normalized overrides missing Unicode model name")
	}

	_, err = parseModelReasoningEffortOverrides(`{"` + modelName + `模":["low"]}`)
	if err == nil || !strings.Contains(err.Error(), "255") {
		t.Fatalf("256-code-point model name error = %v, want length error", err)
	}
}

func TestParseModelReasoningEffortOverridesRejectsTooManyModels(t *testing.T) {
	var raw strings.Builder
	raw.WriteByte('{')
	for i := 0; i < maxModelReasoningEffortOverrides+1; i++ {
		if i > 0 {
			raw.WriteByte(',')
		}
		raw.WriteString(`"model-`)
		raw.WriteString(strings.Repeat("x", i%5))
		raw.WriteString(`-`)
		raw.WriteString(string(rune('a' + i%26)))
		raw.WriteString(`-`)
		raw.WriteString(strings.Repeat("z", i/26))
		raw.WriteString(`":["low"]`)
	}
	raw.WriteByte('}')

	_, err := parseModelReasoningEffortOverrides(raw.String())
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("error = %v, want model limit", err)
	}
}

func assertReasoningEfforts(t testing.TB, got []string, known bool, want []string, wantKnown bool) {
	t.Helper()
	if known != wantKnown || !reflect.DeepEqual(got, want) {
		t.Fatalf("resolve = %#v, known=%v; want %#v, known=%v", got, known, want, wantKnown)
	}
}
