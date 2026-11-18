// limiters.go — z.ai usage-limiter awareness (Nov 2026 credit system).
//
// In November 2026 z.ai moved the free tier to a credit-metered "Agent"
// product. The server now enforces several usage limiters, delivered either
// as SSE error codes inside a 200-stream or as inline error details:
//
//   FEATURE_QUOTA_EXCEEDED        — free-tier Agent credits exhausted
//                                   (the "Credits: N uses" sidebar counter)
//   CODING_PLAN_USAGE_LIMIT       — Coding Plan 5h/weekly quota hit
//   USER_CONCURRENCY_LIMIT        — per-account concurrent chat limit
//   SYSTEM_CONCURRENCY_LIMIT      — platform-wide capacity (peak hours)
//   LONG_TERM_TASK_CONCURRENCY_LIMIT — long-running agent task slots
//   FREE_AGENT_MODE_COOLDOWN      — agent-mode cooldown on free accounts
//   RISK_CONTROL_BLOCKED          — risk-engine block (rare)
//   user_limit_reached /          — legacy per-account hourly message limit
//   rate_limit_short                 ("try again 1 hour later")
//
// Key operational fact discovered 2026-11: the credit quota meters the
// agent_mode FEATURE (chatType "general_agent" + flags ["general_agent"]).
// Plain chats (chatType "default", no agent flags) — GZ2A's default request
// shape — are NOT metered against agent credits; they only face the legacy
// hourly/concurrency limiters. Keeping AGENT_MODE off therefore keeps the
// credit system out of the way entirely.
package app

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// limiterKind classifies a z.ai usage-limit rejection.
type limiterKind int

const (
	limiterNone limiterKind = iota
	limiterAgentCredits      // FEATURE_QUOTA_EXCEEDED / CODING_PLAN_USAGE_LIMIT
	limiterConcurrency       // USER_/SYSTEM_CONCURRENCY_LIMIT
	limiterHourly            // user_limit_reached / rate_limit_short
	limiterCooldown          // FREE_AGENT_MODE_COOLDOWN
	limiterRisk              // RISK_CONTROL_BLOCKED
)

// errZAILimiter is returned when z.ai rejects a request for usage-limit
// reasons. RetryAfter (when >0) hints when the same request may succeed.
type errZAILimiter struct {
	Kind       limiterKind
	Code       string
	RetryAfter time.Duration
	Detail     string
}

func (e *errZAILimiter) Error() string {
	switch e.Kind {
	case limiterAgentCredits:
		return fmt.Sprintf("z.ai agent credits exhausted (%s) — plain chat mode (default in GZ2A) is not credit-metered; credits reset on z.ai's schedule%s",
			e.Code, retrySuffix(e.RetryAfter))
	case limiterConcurrency:
		return fmt.Sprintf("z.ai concurrency limit (%s) — retry shortly%s", e.Code, retrySuffix(e.RetryAfter))
	case limiterHourly:
		return fmt.Sprintf("z.ai personal hourly usage limit — please retry later%s", retrySuffix(e.RetryAfter))
	case limiterCooldown:
		return fmt.Sprintf("z.ai agent-mode cooldown on free accounts — plain chat mode is unaffected%s", retrySuffix(e.RetryAfter))
	case limiterRisk:
		return "z.ai risk control blocked this request — slow down or rotate route"
	}
	return "z.ai usage limit: " + e.Detail
}

func retrySuffix(d time.Duration) string {
	if d > 0 {
		return fmt.Sprintf(" (resets in ~%s)", d.Round(time.Second))
	}
	return ""
}

// asZAILimiter extracts a limiter error from err, or nil.
func asZAILimiter(err error) *errZAILimiter {
	var l *errZAILimiter
	if errors.As(err, &l) {
		return l
	}
	return nil
}

// classifyLimiter maps a z.ai inline SSE error payload to a limiter error.
// detail is the human text; code is the machine code when present.
func classifyLimiter(detail, code string, nextAvailable time.Time) error {
	dl := strings.ToLower(detail)
	cl := strings.ToUpper(code)

	// match on the machine code first, fall back to text sniffing
	switch {
	case strings.Contains(cl, "FEATURE_QUOTA_EXCEEDED") || strings.Contains(cl, "CODING_PLAN_USAGE_LIMIT"),
		strings.Contains(dl, "quota") && strings.Contains(dl, "exceed"),
		strings.Contains(dl, "credit"):
		return &errZAILimiter{Kind: limiterAgentCredits, Code: firstNonEmpty(code, "FEATURE_QUOTA_EXCEEDED"), RetryAfter: until(nextAvailable), Detail: detail}
	case strings.Contains(cl, "USER_CONCURRENCY"), strings.Contains(cl, "SYSTEM_CONCURRENCY"),
		strings.Contains(dl, "concurrent"):
		return &errZAILimiter{Kind: limiterConcurrency, Code: firstNonEmpty(code, "CONCURRENCY_LIMIT"), RetryAfter: until(nextAvailable), Detail: detail}
	case strings.Contains(cl, "LONG_TERM_TASK"),
		strings.Contains(dl, "long_term") || strings.Contains(dl, "long-term"):
		return &errZAILimiter{Kind: limiterConcurrency, Code: firstNonEmpty(code, "LONG_TERM_TASK_CONCURRENCY_LIMIT"), RetryAfter: until(nextAvailable), Detail: detail}
	case strings.Contains(cl, "FREE_AGENT_MODE_COOLDOWN"):
		return &errZAILimiter{Kind: limiterCooldown, Code: "FREE_AGENT_MODE_COOLDOWN", RetryAfter: until(nextAvailable), Detail: detail}
	case strings.Contains(cl, "RISK_CONTROL"):
		return &errZAILimiter{Kind: limiterRisk, Code: "RISK_CONTROL_BLOCKED", Detail: detail}
	case strings.Contains(dl, "user_limit_reached") || strings.Contains(dl, "rate_limit_short"),
		strings.Contains(dl, "personal limit") || strings.Contains(dl, "1 hour later"):
		return &errZAILimiter{Kind: limiterHourly, Code: "USER_LIMIT", RetryAfter: until(nextAvailable), Detail: detail}
	}
	return nil
}

func until(t time.Time) time.Duration {
	if t.IsZero() {
		return 0
	}
	d := time.Until(t)
	if d < 0 {
		return 0
	}
	return d
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
