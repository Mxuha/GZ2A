// persona.go — coherent device persona: timezone + browser fingerprint.
//
// WHY THIS EXISTS (privacy audit findings 1 & 2):
//
//  1. Timezone/geo contradiction. The signed query params claimed
//     America/Los_Angeles with a stale offset of 420, `local_time` was just
//     UTC (identical to utc_time — impossible for a real browser), and the
//     {{CURRENT_*}} template variables leaked the device's true local time
//     alongside a hardcoded "Africa/Algiers". Cross-checking the declared
//     timezone against the GeoIP of the exit IP is a classic risk-control
//     signal, and this triple contradiction was exactly the kind of thing
//     that triggers it.
//
//  2. Static shared fingerprint. Every install shipped the identical
//     identity (Chrome/146, 1280x800 screen, 805x685 viewport) — one static
//     fingerprint shared by the whole user base and trivially correlatable.
//
// The persona is generated ONCE per install from crypto/rand, persisted to
// persona.json (mode 0600; path overridable via GZ2A_PERSONA_FILE) and
// reused for every request. A given install therefore looks like one
// stable, ordinary desktop browser — not everyone's-identical, and not a
// shape-shifter that changes identity per request (itself a bot signal).
//
// One source of truth drives the signed `timezone`, `timezone_offset`
// (JS getTimezoneOffset semantics, DST-aware), `local_time` params, the
// {{CURRENT_*}} variables and the User-Agent used on the wire.
//
// Timezone resolution order:
//     GZ2A_TIMEZONE  >  TZ  >  /etc/localtime symlink  >  UTC
// Point GZ2A_TIMEZONE at the IANA zone matching your exit IP's
// geolocation (default: the device's own zone, which is usually right).
package app

import (
	_ "time/tzdata" // embed the IANA database — LoadLocation works on Android/Termux

	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"
	"time"
)

type devicePersona struct {
	Timezone    string `json:"timezone"`
	ChromeMajor int    `json:"chrome_major"`
	ScreenW     int    `json:"screen_w"`
	ScreenH     int    `json:"screen_h"`
	ViewportW   int    `json:"viewport_w"`
	ViewportH   int    `json:"viewport_h"`
	PixelRatio  string `json:"pixel_ratio"`
}

// UserAgent returns the persona's Chrome-on-Windows UA. The TLS profile
// (Chrome_146 in tlsclient.go) is version-agnostic for JA3 purposes, so a
// nearby major version is indistinguishable on the wire.
func (p devicePersona) UserAgent() string {
	return fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", p.ChromeMajor)
}

// zaiPersona is loaded once at package init; an existing persona.json wins,
// so the fingerprint survives restarts and binary upgrades.
var zaiPersona = loadOrCreatePersona()

var personaLoc = loadPersonaLoc()

func loadPersonaLoc() *time.Location {
	if loc, err := time.LoadLocation(zaiPersona.Timezone); err == nil {
		return loc
	}
	log.Printf("[Persona] WARNING: timezone %q not loadable — falling back to UTC", zaiPersona.Timezone)
	return time.UTC
}

// personaNow returns the current time in the persona's timezone.
func personaNow() time.Time { return time.Now().In(personaLoc) }

// personaJSOffsetMinutes mirrors JS Date#getTimezoneOffset(): minutes to
// add to local time to reach UTC (west of UTC positive), DST-aware.
func personaJSOffsetMinutes(t time.Time) int {
	_, offSec := t.In(personaLoc).Zone()
	return -(offSec / 60)
}

// personaScreen renders "WxH".
func (p devicePersona) personaScreen() string {
	return fmt.Sprintf("%dx%d", p.ScreenW, p.ScreenH)
}

func (p devicePersona) personaViewport() string {
	return fmt.Sprintf("%dx%d", p.ViewportW, p.ViewportH)
}

func loadOrCreatePersona() devicePersona {
	path := envOr("GZ2A_PERSONA_FILE", "persona.json")
	if data, err := os.ReadFile(path); err == nil {
		var p devicePersona
		if json.Unmarshal(data, &p) == nil && p.valid() {
			log.Printf("[Persona] loaded %s (tz=%s ua=Chrome/%d screen=%s viewport=%s)",
				path, p.Timezone, p.ChromeMajor, p.personaScreen(), p.personaViewport())
			return p
		}
		log.Printf("[Persona] %s unreadable — regenerating", path)
	}
	p := generatePersona()
	if data, err := json.MarshalIndent(p, "", "  "); err == nil {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			log.Printf("[Persona] could not persist %s (%v) — fingerprint will change per restart", path, err)
		}
	}
	log.Printf("[Persona] generated new identity (tz=%s ua=Chrome/%d screen=%s viewport=%s) → %s",
		p.Timezone, p.ChromeMajor, p.personaScreen(), p.personaViewport(), path)
	return p
}

func (p devicePersona) valid() bool {
	if p.ChromeMajor < 100 || p.ChromeMajor > 200 {
		return false
	}
	if p.ScreenW < 640 || p.ScreenH < 480 || p.ScreenW > 7680 || p.ScreenH > 4320 {
		return false
	}
	if p.ViewportW <= 0 || p.ViewportH <= 0 || p.ViewportW >= p.ScreenW || p.ViewportH >= p.ScreenH {
		return false
	}
	if p.PixelRatio == "" {
		return false
	}
	_, err := time.LoadLocation(p.Timezone)
	return err == nil
}

// personaScreens — common Windows desktop resolutions in CSS pixels
// (devicePixelRatio applied), paired with the scaling factor that produces
// them. Browsers report screen.* in CSS px, so 1536x864 is what a
// 1920x1080 panel at Windows' default 125% scaling actually reports.
var personaScreens = []struct {
	w, h  int
	ratio string
}{
	{1920, 1080, "1"},
	{1536, 864, "1.25"},
	{2560, 1440, "1"},
	{2560, 1440, "1.5"}, // 4K panel at 150%
	{1600, 900, "1"},
	{1440, 900, "1"},
	{1366, 768, "1"},
	{1280, 720, "1"},
	{1280, 800, "1"},
}

func generatePersona() devicePersona {
	scr := personaScreens[randInt(0, len(personaScreens)-1)]
	p := devicePersona{
		Timezone:    detectPersonaTimezone(),
		ChromeMajor: randInt(146, 151),
		ScreenW:     scr.w,
		ScreenH:     scr.h,
		PixelRatio:  scr.ratio,
	}
	// Viewport: a plausible browser content area — strictly smaller than
	// the screen, roughly proportional (window + toolbars eat the rest).
	p.ViewportW = clampInt(randInt(p.ScreenW*62/100, p.ScreenW*95/100), 480, p.ScreenW-1)
	p.ViewportH = clampInt(randInt(p.ScreenH*55/100, p.ScreenH*88/100), 320, p.ScreenH-1)
	return p
}

// detectPersonaTimezone resolves the persona timezone (see file header for
// the order). The default of "whatever zone this device is actually in"
// keeps the declared timezone consistent with the GeoIP of the exit IP.
func detectPersonaTimezone() string {
	for _, cand := range []string{
		strings.TrimSpace(os.Getenv("GZ2A_TIMEZONE")),
		strings.TrimSpace(os.Getenv("TZ")),
	} {
		if cand == "" {
			continue
		}
		if _, err := time.LoadLocation(cand); err == nil {
			return cand
		}
		log.Printf("[Persona] timezone %q not an IANA zone — ignoring", cand)
	}
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.LastIndex(link, "zoneinfo/"); i >= 0 {
			name := strings.Trim(strings.TrimSpace(link[i+len("zoneinfo/"):]), "/")
			if name != "" {
				if _, err := time.LoadLocation(name); err == nil {
					return name
				}
			}
		}
	}
	return "UTC"
}

// randInt returns a cryptographically random int in [min, max].
func randInt(min, max int) int {
	if max <= min {
		return min
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	if err != nil {
		return min // fail closed
	}
	return min + int(n.Int64())
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
