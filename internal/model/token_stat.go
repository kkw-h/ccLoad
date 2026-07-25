package model

// TokenStatOutcome 描述一次请求对 auth_token 统计的记账语义。
//
// 解耦「usage/费用累加」与「成功/失败健康度计数」，以支持客户端取消(499)
// 按实际 usage 计费、但不计入成功率的场景（Bill=true 而 CountSuccess/CountFailure 均 false）。
type TokenStatOutcome struct {
	CountSuccess bool // success_count +1（2xx 成功）
	CountFailure bool // failure_count +1（普通失败）
	Bill         bool // 累加 tokens/费用/cost_used（计费）
}

// TokenStatSuccess 成功请求：计成功 + 计费。
func TokenStatSuccess() TokenStatOutcome {
	return TokenStatOutcome{CountSuccess: true, Bill: true}
}

// TokenStatFailure 普通失败：计失败，不计费。
func TokenStatFailure() TokenStatOutcome {
	return TokenStatOutcome{CountFailure: true}
}

// TokenStatBilledNeutral 中性计费：计费但不计成功/失败
// （如 499 客户端取消但上游已产 usage）。
func TokenStatBilledNeutral() TokenStatOutcome {
	return TokenStatOutcome{Bill: true}
}
