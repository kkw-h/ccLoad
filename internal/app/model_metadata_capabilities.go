package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"ccLoad/internal/protocol/cliproxy/registry"
)

const (
	modelMetadataOverridesSetting = "model_metadata_overrides"
	maxModelMetadataOverrides     = 500
	maxMetadataModelNameLength    = 255
)

var builtInModelInputTypes = map[string][]string{
	"gpt-5.6-sol": {"text"},
}

type modelMetadata struct {
	Provider      *string
	ContextWindow *int64
	MaxTokens     *int64
	InputTypes    *[]string
}

type modelMetadataResolver struct {
	overrides atomic.Pointer[map[string]modelMetadata]
}

func newModelMetadataResolver(raw string) (*modelMetadataResolver, error) {
	resolver := &modelMetadataResolver{}
	if err := resolver.SetOverrides(raw); err != nil {
		return nil, err
	}
	return resolver, nil
}

func (r *modelMetadataResolver) SetOverrides(raw string) error {
	overrides, err := parseModelMetadataOverrides(raw)
	if err != nil {
		return err
	}
	r.overrides.Store(&overrides)
	return nil
}

func (r *modelMetadataResolver) Resolve(originalModel string) modelMetadata {
	key := normalizeMetadataModelName(originalModel)
	result := builtInModelMetadata(key)
	if r == nil {
		return result
	}
	current := r.overrides.Load()
	if current == nil {
		return result
	}
	override, ok := (*current)[key]
	if !ok {
		return result
	}
	if override.Provider != nil {
		result.Provider = cloneStringPointer(override.Provider)
	}
	if override.ContextWindow != nil {
		result.ContextWindow = cloneInt64Pointer(override.ContextWindow)
	}
	if override.MaxTokens != nil {
		result.MaxTokens = cloneInt64Pointer(override.MaxTokens)
	}
	if override.InputTypes != nil {
		result.InputTypes = cloneStringSlicePointer(override.InputTypes)
	}
	return result
}

func (r *modelMetadataResolver) ResolveAll(originalModels []string) modelMetadata {
	if len(originalModels) == 0 {
		return modelMetadata{}
	}

	resolved := make([]modelMetadata, 0, len(originalModels))
	for _, originalModel := range originalModels {
		resolved = append(resolved, r.Resolve(originalModel))
	}

	result := modelMetadata{}
	if allMetadataStringsKnown(resolved, func(value modelMetadata) *string { return value.Provider }) {
		provider := *resolved[0].Provider
		for _, value := range resolved[1:] {
			if *value.Provider != provider {
				provider = "mixed"
				break
			}
		}
		result.Provider = &provider
	}
	if allMetadataIntsKnown(resolved, func(value modelMetadata) *int64 { return value.ContextWindow }) {
		minimum := *resolved[0].ContextWindow
		for _, value := range resolved[1:] {
			minimum = min(minimum, *value.ContextWindow)
		}
		result.ContextWindow = &minimum
	}
	if allMetadataIntsKnown(resolved, func(value modelMetadata) *int64 { return value.MaxTokens }) {
		minimum := *resolved[0].MaxTokens
		for _, value := range resolved[1:] {
			minimum = min(minimum, *value.MaxTokens)
		}
		result.MaxTokens = &minimum
	}
	if allMetadataStringSlicesKnown(resolved, func(value modelMetadata) *[]string { return value.InputTypes }) {
		intersection := slices.Clone(*resolved[0].InputTypes)
		for _, value := range resolved[1:] {
			allowed := make(map[string]struct{}, len(*value.InputTypes))
			for _, inputType := range *value.InputTypes {
				allowed[inputType] = struct{}{}
			}
			intersection = slices.DeleteFunc(intersection, func(inputType string) bool {
				_, ok := allowed[inputType]
				return !ok
			})
		}
		if intersection == nil {
			intersection = make([]string, 0)
		}
		result.InputTypes = &intersection
	}
	return result
}

func builtInModelMetadata(modelName string) modelMetadata {
	result := modelMetadata{}
	if info := registry.LookupModelInfo(modelName); info != nil {
		if provider := displayProviderName(info.OwnedBy); provider != "" {
			result.Provider = &provider
		}
		if info.ContextLength > 0 {
			contextWindow := info.ContextLength
			result.ContextWindow = &contextWindow
		}
		if info.MaxCompletionTokens > 0 {
			maxTokens := info.MaxCompletionTokens
			result.MaxTokens = &maxTokens
		}
	}
	if inputTypes, ok := builtInModelInputTypes[modelName]; ok {
		cloned := slices.Clone(inputTypes)
		result.InputTypes = &cloned
	}
	return result
}

func displayProviderName(value string) string {
	trimmed := strings.TrimSpace(value)
	switch strings.ToLower(trimmed) {
	case "openai":
		return "OpenAI"
	case "anthropic":
		return "Anthropic"
	case "google":
		return "Google"
	case "xai":
		return "xAI"
	default:
		return trimmed
	}
}

func parseModelMetadataOverrides(raw string) (map[string]modelMetadata, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("must be a JSON object: %w", err)
	}
	openingDelimiter, ok := opening.(json.Delim)
	if !ok || openingDelimiter != '{' {
		return nil, fmt.Errorf("must be a JSON object")
	}

	normalized := make(map[string]modelMetadata)
	for decoder.More() {
		modelToken, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("must be a JSON object: %w", err)
		}
		modelName, ok := modelToken.(string)
		if !ok {
			return nil, fmt.Errorf("must be a JSON object")
		}
		key := normalizeMetadataModelName(modelName)
		if key == "" {
			return nil, fmt.Errorf("model name must not be blank")
		}
		if utf8.RuneCountInString(key) > maxMetadataModelNameLength {
			return nil, fmt.Errorf("model name must not exceed %d characters", maxMetadataModelNameLength)
		}
		if _, exists := normalized[key]; exists {
			return nil, fmt.Errorf("duplicate model after normalization: %s", key)
		}

		var encoded json.RawMessage
		if err := decoder.Decode(&encoded); err != nil {
			return nil, fmt.Errorf("metadata for %s must be an object: %w", key, err)
		}
		metadata, err := parseModelMetadataValue(key, encoded)
		if err != nil {
			return nil, err
		}
		normalized[key] = metadata
		if len(normalized) > maxModelMetadataOverrides {
			return nil, fmt.Errorf("must contain at most %d model overrides", maxModelMetadataOverrides)
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, fmt.Errorf("must be a JSON object")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("must contain exactly one JSON object")
		}
		return nil, fmt.Errorf("must be a JSON object: %w", err)
	}
	return normalized, nil
}

func parseModelMetadataValue(modelName string, raw json.RawMessage) (modelMetadata, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil {
		return modelMetadata{}, fmt.Errorf("metadata for %s must be an object: %w", modelName, err)
	}
	openingDelimiter, ok := opening.(json.Delim)
	if !ok || openingDelimiter != '{' {
		return modelMetadata{}, fmt.Errorf("metadata for %s must be an object", modelName)
	}

	result := modelMetadata{}
	seen := make(map[string]struct{})
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return modelMetadata{}, fmt.Errorf("metadata for %s must be an object: %w", modelName, err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return modelMetadata{}, fmt.Errorf("metadata for %s must be an object", modelName)
		}
		if _, duplicate := seen[field]; duplicate {
			return modelMetadata{}, fmt.Errorf("duplicate metadata field %q for %s", field, modelName)
		}
		seen[field] = struct{}{}

		var encoded json.RawMessage
		if err := decoder.Decode(&encoded); err != nil {
			return modelMetadata{}, fmt.Errorf("invalid %s for %s: %w", field, modelName, err)
		}
		switch field {
		case "provider":
			var value string
			if err := json.Unmarshal(encoded, &value); err != nil {
				return modelMetadata{}, fmt.Errorf("provider for %s must be a string", modelName)
			}
			value = strings.TrimSpace(value)
			if value == "" {
				return modelMetadata{}, fmt.Errorf("provider for %s must not be blank", modelName)
			}
			result.Provider = &value
		case "contextWindow":
			value, err := decodePositiveMetadataInt(encoded, "contextWindow", modelName)
			if err != nil {
				return modelMetadata{}, err
			}
			result.ContextWindow = &value
		case "maxTokens":
			value, err := decodePositiveMetadataInt(encoded, "maxTokens", modelName)
			if err != nil {
				return modelMetadata{}, err
			}
			result.MaxTokens = &value
		case "inputTypes":
			if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
				return modelMetadata{}, fmt.Errorf("inputTypes for %s must be an array of strings", modelName)
			}
			var values []string
			if err := json.Unmarshal(encoded, &values); err != nil || values == nil {
				return modelMetadata{}, fmt.Errorf("inputTypes for %s must be an array of strings", modelName)
			}
			normalized, err := normalizeMetadataInputTypes(modelName, values)
			if err != nil {
				return modelMetadata{}, err
			}
			result.InputTypes = &normalized
		default:
			return modelMetadata{}, fmt.Errorf("unknown metadata field %q for %s", field, modelName)
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return modelMetadata{}, fmt.Errorf("metadata for %s must be an object", modelName)
	}
	if len(seen) == 0 {
		return modelMetadata{}, fmt.Errorf("metadata for %s must contain at least one field", modelName)
	}
	return result, nil
}

func decodePositiveMetadataInt(raw json.RawMessage, field, modelName string) (int64, error) {
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil || value <= 0 {
		return 0, fmt.Errorf("%s for %s must be a positive integer", field, modelName)
	}
	return value, nil
}

func normalizeMetadataInputTypes(modelName string, values []string) ([]string, error) {
	selected := make(map[string]struct{}, len(values))
	for _, rawValue := range values {
		value := strings.ToLower(strings.TrimSpace(rawValue))
		if value == "" {
			return nil, fmt.Errorf("inputTypes for %s must not contain blank values", modelName)
		}
		selected[value] = struct{}{}
	}
	normalized := make([]string, 0, len(selected))
	for value := range selected {
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func normalizeMetadataModelName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func allMetadataStringsKnown(values []modelMetadata, field func(modelMetadata) *string) bool {
	return !slices.ContainsFunc(values, func(value modelMetadata) bool { return field(value) == nil })
}

func allMetadataIntsKnown(values []modelMetadata, field func(modelMetadata) *int64) bool {
	return !slices.ContainsFunc(values, func(value modelMetadata) bool { return field(value) == nil })
}

func allMetadataStringSlicesKnown(values []modelMetadata, field func(modelMetadata) *[]string) bool {
	return !slices.ContainsFunc(values, func(value modelMetadata) bool { return field(value) == nil })
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneStringSlicePointer(value *[]string) *[]string {
	if value == nil {
		return nil
	}
	cloned := slices.Clone(*value)
	if cloned == nil {
		cloned = make([]string, 0)
	}
	return &cloned
}
