package app

import "strings"

const (
	usageEventEnvironmentPrefix  = "sedna-"
	usageEventEnvironmentSuffix  = "-user-"
	legacyDevelopmentTokenPrefix = "sedna-development-user-"
)

func normalizeUsageEventTokenDescription(description string) string {
	if !strings.HasPrefix(description, legacyDevelopmentTokenPrefix) {
		return description
	}
	return usageEventEnvironmentPrefix + "dev" + usageEventEnvironmentSuffix +
		strings.TrimPrefix(description, legacyDevelopmentTokenPrefix)
}

// extractUsageEventEnvironment 从 medge 注册到 ccLoad 的 API 令牌描述中提取环境标识。
//
// medge 侧约定描述形如：sedna-<env>-user-<uid>。
// 不符合约定的旧令牌返回空字符串，后续事件继续落到默认 stream，保持兼容。
func extractUsageEventEnvironment(description string) string {
	description = strings.TrimSpace(description)
	if !strings.HasPrefix(description, usageEventEnvironmentPrefix) {
		return ""
	}
	rest := strings.TrimPrefix(description, usageEventEnvironmentPrefix)
	env, _, ok := strings.Cut(rest, usageEventEnvironmentSuffix)
	if !ok {
		return ""
	}
	env = strings.TrimSpace(env)
	if env == "" || strings.ContainsAny(env, ":{} \t\r\n") {
		return ""
	}
	if env == "development" {
		return "dev"
	}
	return env
}
