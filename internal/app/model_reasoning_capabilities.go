package app

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
)

const (
	modelReasoningEffortOverridesSetting = "model_reasoning_effort_overrides"
	maxModelReasoningEffortOverrides     = 500
	maxReasoningModelNameLength          = 255
)

var reasoningEffortOrder = []string{
	"none",
	"minimal",
	"low",
	"medium",
	"high",
	"xhigh",
	"max",
}

var builtInModelReasoningEfforts = map[string][]string{
	"gpt-5.6-sol": {"low", "medium", "high", "xhigh"},
}

type modelReasoningCapabilityResolver struct {
	overrides atomic.Pointer[map[string][]string]
}

func newModelReasoningCapabilityResolver(raw string) (*modelReasoningCapabilityResolver, error) {
	resolver := &modelReasoningCapabilityResolver{}
	if err := resolver.SetOverrides(raw); err != nil {
		return nil, err
	}
	return resolver, nil
}

func (r *modelReasoningCapabilityResolver) SetOverrides(raw string) error {
	overrides, err := parseModelReasoningEffortOverrides(raw)
	if err != nil {
		return err
	}
	r.overrides.Store(&overrides)
	return nil
}

func (r *modelReasoningCapabilityResolver) Resolve(originalModel string) ([]string, bool) {
	key := normalizeReasoningModelName(originalModel)
	if r != nil {
		if current := r.overrides.Load(); current != nil {
			if efforts, ok := (*current)[key]; ok {
				return slices.Clone(efforts), true
			}
		}
	}

	efforts, ok := builtInModelReasoningEfforts[key]
	return slices.Clone(efforts), ok
}

func (r *modelReasoningCapabilityResolver) ResolveAll(originalModels []string) ([]string, bool) {
	if len(originalModels) == 0 {
		return nil, false
	}

	intersection, known := r.Resolve(originalModels[0])
	if !known {
		return nil, false
	}
	for _, originalModel := range originalModels[1:] {
		efforts, ok := r.Resolve(originalModel)
		if !ok {
			return nil, false
		}
		allowed := make(map[string]struct{}, len(efforts))
		for _, effort := range efforts {
			allowed[effort] = struct{}{}
		}
		intersection = slices.DeleteFunc(intersection, func(effort string) bool {
			_, ok := allowed[effort]
			return !ok
		})
	}
	if intersection == nil {
		intersection = make([]string, 0)
	}
	return intersection, true
}

func parseModelReasoningEffortOverrides(raw string) (map[string][]string, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("must be a JSON object: %w", err)
	}
	if values == nil {
		return nil, fmt.Errorf("must be a JSON object")
	}
	if len(values) > maxModelReasoningEffortOverrides {
		return nil, fmt.Errorf("must contain at most %d model overrides", maxModelReasoningEffortOverrides)
	}

	allowedEfforts := make(map[string]struct{}, len(reasoningEffortOrder))
	for _, effort := range reasoningEffortOrder {
		allowedEfforts[effort] = struct{}{}
	}

	normalized := make(map[string][]string, len(values))
	for rawModel, encodedEfforts := range values {
		modelName := normalizeReasoningModelName(rawModel)
		if modelName == "" {
			return nil, fmt.Errorf("model name must not be blank")
		}
		if len(modelName) > maxReasoningModelNameLength {
			return nil, fmt.Errorf("model name must not exceed %d characters", maxReasoningModelNameLength)
		}
		if _, exists := normalized[modelName]; exists {
			return nil, fmt.Errorf("duplicate model after normalization: %s", modelName)
		}

		var rawEfforts []string
		if err := json.Unmarshal(encodedEfforts, &rawEfforts); err != nil || rawEfforts == nil {
			return nil, fmt.Errorf("reasoning efforts for %s must be an array of strings", modelName)
		}
		selected := make(map[string]struct{}, len(rawEfforts))
		for _, rawEffort := range rawEfforts {
			effort := strings.ToLower(strings.TrimSpace(rawEffort))
			if _, ok := allowedEfforts[effort]; !ok {
				return nil, fmt.Errorf("unknown reasoning effort %q for model %s", rawEffort, modelName)
			}
			selected[effort] = struct{}{}
		}

		efforts := make([]string, 0, len(selected))
		for _, effort := range reasoningEffortOrder {
			if _, ok := selected[effort]; ok {
				efforts = append(efforts, effort)
			}
		}
		normalized[modelName] = efforts
	}
	return normalized, nil
}

func normalizeReasoningModelName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
