package app

import (
        "net/url"
        "strings"
        "testing"
        "time"
)

// TestSignatureParamsPersona verifies that generateSignatureWithParams
// derives every identity-bearing query param from the persona: timezone,
// JS-semantics offset (DST-aware), local wall time, screen/viewport/ratio
// and the UA — with no hardcoded leftovers.
func TestSignatureParamsPersona(t *testing.T) {
        // Pin a persona: Africa/Algiers (UTC+1, no DST in November).
        saved, savedLoc := zaiPersona, personaLoc
        zaiPersona = devicePersona{
                Timezone: "Africa/Algiers", ChromeMajor: 148,
                ScreenW: 1920, ScreenH: 1080, ViewportW: 1536, ViewportH: 824, PixelRatio: "1",
        }
        personaLoc, _ = time.LoadLocation("Africa/Algiers")
        savedUA := zaiUserAgent
        zaiUserAgent = zaiPersona.UserAgent() // mirrors package-init wiring
        defer func() { zaiPersona, personaLoc, zaiUserAgent = saved, savedLoc, savedUA }()

        s := &ZaiSession{}
        _, qs := s.generateSignatureWithParams("hello", "tok", "uid", "chat123")
        q, err := url.ParseQuery(qs)
        if err != nil {
                t.Fatalf("unparseable query: %v", err)
        }
        // zaiUserAgent is computed at package init; keep it in sync like production.
        wantUA := zaiPersona.UserAgent()
        for k, want := range map[string]string{
                "timezone":         "Africa/Algiers",
                "timezone_offset":  "-60", // JS getTimezoneOffset: UTC+1 → -60
                "screen_resolution": "1920x1080",
                "viewport_size":    "1536x824",
                "pixel_ratio":      "1",
                "user_agent":       wantUA,
        } {
                if q.Get(k) != want {
                        t.Errorf("%s = %q, want %q", k, q.Get(k), want)
                }
        }
        // local_time must be the persona's wall clock, not UTC: Algeria is
        // UTC+1, so local_time must be exactly utc_time + 1h (same date).
        now := time.Now()
        gotLT := q.Get("local_time")
        wantLT := now.In(personaLoc).Format("2006-01-02T15:04:05")
        if !strings.HasPrefix(gotLT, wantLT) {
                t.Errorf("local_time = %q, want prefix %q (persona wall clock)", gotLT, wantLT)
        }
        if !strings.Contains(wantUA, "Chrome/148") {
                t.Errorf("UA not persona-driven: %q", wantUA)
        }
        // Sanity: no stale hardcoded values may survive.
        for k, stale := range map[string]string{
                "timezone":         "America/Los_Angeles",
                "timezone_offset":  "420",
                "screen_resolution": "1280x800",
                "viewport_size":    "805x685",
        } {
                if q.Get(k) == stale {
                        t.Errorf("stale hardcoded value still present: %s=%s", k, stale)
                }
        }
        if strings.Contains(q.Get("user_agent"), "Chrome/146.0.0.0") {
                t.Error("UA still the old fleet-wide constant Chrome/146")
        }
}
