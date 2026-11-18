// quota.go — GET /v1/quota: z.ai account usage visibility.
//
// Reads the November-2026 usage endpoints the z.ai web app itself uses:
//
//   GET /api/v1/dashboard/feature-quota/summary  — per-feature quotas
//       (agent_mode is the "Credits: N uses" counter on free accounts)
//   GET /api/v1/users/user/plan-usage            — plan 5h/weekly windows
//   GET /api/v1/users/user/subscription          — subscription state
//
// These routes answer 401 for guest tokens; with an account JWT they return
// the live quotas, letting clients see remaining credits BEFORE a request
// hits FEATURE_QUOTA_EXCEEDED.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

type quotaHandlerResponse struct {
	OK     bool                   `json:"ok"`
	Source string                 `json:"source,omitempty"`
	Data   map[string]interface{} `json:"data,omitempty"`
	Error  string                 `json:"error,omitempty"`
}

// fetchZAIQuota GETs one account-scoped endpoint with browser-shaped headers.
func (s *Server) fetchZAIQuota(path string) (int, []byte, error) {
	s.zaiSession.mu.Lock()
	token := s.zaiSession.token
	s.zaiSession.mu.Unlock()
	if token == "" {
		return 0, nil, fmt.Errorf("no z.ai session token configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := zaiHTTP(nil)
	if err != nil {
		return 0, nil, err
	}
	req, _ := fhttp.NewRequestWithContext(ctx, "GET", baseURL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", zaiUserAgent)
	req.Header.Set("Origin", baseURL)
	req.Header.Set("Referer", baseURL+"/")
	req.Header.Set("x-region", "overseas")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, nil
}

// handleV1Quota serves GET /v1/quota — the account's live usage state.
func (s *Server) handleV1Quota(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	out := map[string]interface{}{
		"note": "agent_mode remaining = the free-tier 'Credits: N uses' counter; plain chats (GZ2A default) are not credit-metered",
	}
	anyOK := false

	for name, path := range map[string]string{
		"feature_quota": "/api/v1/dashboard/feature-quota/summary",
		"plan_usage":    "/api/v1/users/user/plan-usage",
		"subscription":  "/api/v1/users/user/subscription",
	} {
		code, body, err := s.fetchZAIQuota(path)
		entry := map[string]interface{}{}
		if err != nil {
			entry["error"] = err.Error()
		} else if code != 200 {
			entry["status"] = code
			// 401/403 on these routes usually means guest token or expired JWT
			if code == 401 || code == 403 {
				entry["error"] = "not available for this token (guest tokens have no quotas; check your account JWT)"
			} else {
				entry["body"] = truncateBytes(body, 300)
			}
		} else {
			var parsed interface{}
			if json.Unmarshal(body, &parsed) == nil {
				entry["data"] = parsed
				anyOK = true
			} else {
				entry["body"] = truncateBytes(body, 300)
			}
		}
		out[name] = entry
	}

	resp := quotaHandlerResponse{OK: anyOK, Data: out}
	if !anyOK {
		resp.Source = "all endpoints failed — is ZAI_TOKEN an account JWT (not a guest token)?"
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
