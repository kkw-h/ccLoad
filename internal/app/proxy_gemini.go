package app

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ccLoad/internal/model"
)

// ============================================================================
// Gemini API 特殊处理
// ============================================================================

func (s *Server) filterVisibleModelsForRequest(c *gin.Context, _ string, models []string) []string {
	if s.authService == nil {
		return models
	}

	tokenHash, _ := c.Get("token_hash")
	tokenHashStr, _ := tokenHash.(string)
	if tokenHashStr == "" {
		return models
	}

	if restriction, hasRestriction := s.authService.getChannelRestriction(tokenHashStr); hasRestriction {
		channels, err := s.GetEnabledChannelsByModel(c.Request.Context(), "*")
		if err != nil {
			return nil
		}
		modelSet := make(map[string]struct{})
		for _, cfg := range channels {
			if cfg == nil || !restriction.Allows(cfg.ID) {
				continue
			}
			for _, modelName := range cfg.GetModels() {
				modelSet[modelName] = struct{}{}
			}
		}
		models = make([]string, 0, len(modelSet))
		for modelName := range modelSet {
			models = append(models, modelName)
		}
	}

	return s.authService.FilterAllowedModels(tokenHashStr, models)
}

func (s *Server) visibleModelsAndChannelsForRequest(c *gin.Context, channels []*model.Config) ([]string, []*model.Config) {
	visibleChannels := channels
	tokenHash, _ := c.Get("token_hash")
	tokenHashStr, _ := tokenHash.(string)
	if s.authService != nil && tokenHashStr != "" {
		if filtered, restricted := s.authService.FilterAllowedChannels(tokenHashStr, channels); restricted {
			visibleChannels = filtered
		}
	}

	models := modelNamesFromChannels(visibleChannels)
	if s.authService != nil && tokenHashStr != "" {
		models = s.authService.FilterAllowedModels(tokenHashStr, models)
	}
	return models, visibleChannels
}

type visibleModelCapabilities struct {
	SupportedReasoningEfforts *[]string
	ThinkingLevels            *[]string
	Metadata                  modelMetadata
}

func originalModelsForVisibleModels(channels []*model.Config, visibleModels []string) map[string][]string {
	visible := make(map[string]struct{}, len(visibleModels))
	for _, modelName := range visibleModels {
		visible[modelName] = struct{}{}
	}
	originalsByModel := make(map[string]map[string]struct{}, len(visibleModels))
	for _, cfg := range channels {
		if cfg == nil {
			continue
		}
		for _, entry := range cfg.ModelEntries {
			if entry.Disabled {
				continue
			}
			if _, ok := visible[entry.Model]; !ok {
				continue
			}
			originalModel := strings.TrimSpace(entry.RedirectModel)
			if originalModel == "" {
				originalModel = entry.Model
			}
			if originalsByModel[entry.Model] == nil {
				originalsByModel[entry.Model] = make(map[string]struct{})
			}
			originalsByModel[entry.Model][normalizeReasoningModelName(originalModel)] = struct{}{}
		}
	}

	result := make(map[string][]string, len(originalsByModel))
	for modelName, originalsSet := range originalsByModel {
		originals := make([]string, 0, len(originalsSet))
		for original := range originalsSet {
			originals = append(originals, original)
		}
		sort.Strings(originals)
		result[modelName] = originals
	}
	return result
}

func (s *Server) capabilitiesForVisibleModels(channels []*model.Config, visibleModels []string) map[string]visibleModelCapabilities {
	capabilities := make(map[string]visibleModelCapabilities, len(visibleModels))
	for modelName, originals := range originalModelsForVisibleModels(channels, visibleModels) {
		capability := visibleModelCapabilities{}
		if s.modelReasoningCapabilities != nil {
			efforts, known := s.modelReasoningCapabilities.ResolveAll(originals)
			if known {
				effortsCopy := append([]string(nil), efforts...)
				if effortsCopy == nil {
					effortsCopy = make([]string, 0)
				}
				capability.SupportedReasoningEfforts = &effortsCopy
				capability.ThinkingLevels = thinkingLevelsFromReasoningEfforts(effortsCopy)
			}
		}
		if s.modelMetadataCapabilities != nil {
			capability.Metadata = s.modelMetadataCapabilities.ResolveAll(originals)
		}
		capabilities[modelName] = capability
	}
	return capabilities
}

func thinkingLevelsFromReasoningEfforts(efforts []string) *[]string {
	levels := append([]string(nil), efforts...)
	if levels == nil {
		levels = make([]string, 0)
	}
	for index, effort := range levels {
		if effort == "none" {
			levels[index] = "off"
		}
	}
	return &levels
}

// handleListGeminiModels 处理 GET /v1beta/models 请求，返回本地 Gemini 模型列表
// 从proxy.go提取，遵循SRP原则
func (s *Server) handleListGeminiModels(c *gin.Context) {
	ctx := c.Request.Context()

	models, err := s.getAllEnabledModels(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load models"})
		return
	}
	models = s.filterVisibleModelsForRequest(c, "gemini", models)
	sort.Strings(models)

	// 构造 Gemini API 响应格式
	type ModelInfo struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	}

	modelList := make([]ModelInfo, 0, len(models))
	for _, model := range models {
		modelList = append(modelList, ModelInfo{
			Name:        "models/" + model,
			DisplayName: formatModelDisplayName(model),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"models": modelList,
	})
}

// detectModelsClientProtocol 根据请求头判断 /v1/models 的客户端协议。
// anthropic-version 头存在 → anthropic 渠道；否则 → openai 渠道
func detectModelsClientProtocol(c *gin.Context) string {
	if c.GetHeader("anthropic-version") != "" {
		return "anthropic"
	}
	if strings.HasPrefix(strings.ToLower(c.GetHeader("User-Agent")), "claude-cli") {
		return "anthropic"
	}
	if strings.Contains(strings.ToLower(c.GetHeader("User-Agent")), "codex") {
		return "codex"
	}
	return "openai"
}

// handleListOpenAIModels 处理 GET /v1/models 请求，根据请求类型返回对应渠道的模型列表
func (s *Server) handleListOpenAIModels(c *gin.Context) {
	ctx := c.Request.Context()

	clientProtocol := detectModelsClientProtocol(c)
	channels, err := s.GetEnabledChannelsByModel(ctx, "*")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load models"})
		return
	}
	models, visibleChannels := s.visibleModelsAndChannelsForRequest(c, channels)
	sort.Strings(models)
	capabilities := s.capabilitiesForVisibleModels(visibleChannels, models)

	if clientProtocol == "anthropic" {
		type ModelInfo struct {
			ID                        string    `json:"id"`
			DisplayName               string    `json:"display_name"`
			CamelCaseDisplayName      string    `json:"displayName"`
			Type                      string    `json:"type"`
			CreatedAt                 string    `json:"created_at"`
			SupportedReasoningEfforts *[]string `json:"supported_reasoning_efforts,omitempty"`
			Provider                  *string   `json:"provider,omitempty"`
			ThinkingLevels            *[]string `json:"thinkingLevels,omitempty"`
			ContextWindow             *int64    `json:"contextWindow,omitempty"`
			MaxTokens                 *int64    `json:"maxTokens,omitempty"`
			InputTypes                *[]string `json:"inputTypes,omitempty"`
		}
		modelList := make([]ModelInfo, 0, len(models))
		for _, modelName := range models {
			capability := capabilities[modelName]
			displayName := formatModelDisplayName(modelName)
			modelList = append(modelList, ModelInfo{
				ID:                        modelName,
				DisplayName:               displayName,
				CamelCaseDisplayName:      displayName,
				Type:                      "model",
				CreatedAt:                 time.Unix(0, 0).UTC().Format(time.RFC3339),
				SupportedReasoningEfforts: capability.SupportedReasoningEfforts,
				Provider:                  capability.Metadata.Provider,
				ThinkingLevels:            capability.ThinkingLevels,
				ContextWindow:             capability.Metadata.ContextWindow,
				MaxTokens:                 capability.Metadata.MaxTokens,
				InputTypes:                capability.Metadata.InputTypes,
			})
		}

		resp := gin.H{
			"data":     modelList,
			"has_more": false,
		}
		if len(modelList) > 0 {
			resp["first_id"] = modelList[0].ID
			resp["last_id"] = modelList[len(modelList)-1].ID
		}
		c.JSON(http.StatusOK, resp)
		return
	}

	// 构造 OpenAI API 响应格式
	type ModelInfo struct {
		ID                        string    `json:"id"`
		Object                    string    `json:"object"`
		Created                   int64     `json:"created"`
		OwnedBy                   string    `json:"owned_by"`
		MultiAgentVersion         string    `json:"multi_agent_version,omitempty"`
		SupportedReasoningEfforts *[]string `json:"supported_reasoning_efforts,omitempty"`
		DisplayName               string    `json:"displayName"`
		Provider                  *string   `json:"provider,omitempty"`
		ThinkingLevels            *[]string `json:"thinkingLevels,omitempty"`
		ContextWindow             *int64    `json:"contextWindow,omitempty"`
		MaxTokens                 *int64    `json:"maxTokens,omitempty"`
		InputTypes                *[]string `json:"inputTypes,omitempty"`
	}

	modelList := make([]ModelInfo, 0, len(models))
	for _, modelName := range models {
		capability := capabilities[modelName]
		modelList = append(modelList, ModelInfo{
			ID:                        modelName,
			Object:                    "model",
			Created:                   0,
			OwnedBy:                   "system",
			SupportedReasoningEfforts: capability.SupportedReasoningEfforts,
			DisplayName:               formatModelDisplayName(modelName),
			Provider:                  capability.Metadata.Provider,
			ThinkingLevels:            capability.ThinkingLevels,
			ContextWindow:             capability.Metadata.ContextWindow,
			MaxTokens:                 capability.Metadata.MaxTokens,
			InputTypes:                capability.Metadata.InputTypes,
			MultiAgentVersion: func() string {
				if clientProtocol == "codex" {
					return "v2"
				}
				return ""
			}(),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   modelList,
	})
}
