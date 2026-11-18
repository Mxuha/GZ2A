# Privacy Fixes — GZ2A (findings 1–4)

This tree is the GZ2A source with the four actionable findings from the
privacy audit fixed. Findings 5 (disk/log leakage of debug request bodies
and uid/name log lines) and 6 (server-side residue: background
`title_generation`/`tags_generation` tasks and no chat deletion) were
deliberately **skipped** per operator request — see "Not fixed" below.

Everything below is in `internal/app/`.

---

## 1. Timezone / location contradiction — FIXED

**Before** — three mutually contradictory signals, all cross-checkable
against the GeoIP of your exit IP (a classic risk-control signal, and the
profile of a `RISK_CONTROL_BLOCKED` trigger):

| field | old value | problem |
|---|---|---|
| signed `timezone` | `America/Los_Angeles` | hardcoded, wrong continent |
| signed `timezone_offset` | `420` | stale PDT value (PST is 480), never DST-aware |
| signed `local_time` | `time.Now().UTC()` | identical to `utc_time` — impossible for a real browser |
| `{{CURRENT_*}}` variables | device's real local time | leaked the true device timezone |
| `{{CURRENT_TIMEZONE}}` | `"Africa/Algiers"` hardcoded | contradicted the signed `America/Los_Angeles` |

**After** (`persona.go` + `zai.go` + `probe_run.go`) — one source of truth:
a device persona whose timezone drives **all five** places.

* Resolution order: `GZ2A_TIMEZONE` → `TZ` → `/etc/localtime` symlink → `UTC`.
  Default on a normally-configured phone is the device's own zone, which is
  exactly what matches your exit IP's GeoIP.
* `timezone_offset` now uses JS `getTimezoneOffset()` semantics and is
  computed per request (DST-aware; e.g. Africa/Algiers → `-60`).
* `local_time` is the persona's wall clock (so `local_time` − `utc_time` =
  the declared offset), not a second copy of UTC.
* `{{CURRENT_DATETIME/DATE/TIME/WEEKDAY}}` and `{{CURRENT_TIMEZONE}}` are
  rendered from the persona clock — the device's real timezone never leaves
  the process.
* The IANA tz database is embedded (`import _ "time/tzdata"`), so
  `LoadLocation("Africa/Algiers")` works on Android/Termux, which has no
  `/usr/share/zoneinfo`.

If you route traffic through a proxy in another country, set
`GZ2A_TIMEZONE` to a zone consistent with the **exit IP**, e.g.:

```
export GZ2A_TIMEZONE=Europe/Amsterdam
```

## 2. Static shared fingerprint — FIXED

**Before** — every install sent the identical identity: `Chrome/146`,
`1280x800`, `805x685`, pixel ratio 1. One fingerprint shared by the whole
user base; trivially correlatable server-side.

**After** (`persona.go` + `tlsclient.go` + `zai.go`) — a per-install persona
generated once from `crypto/rand`, persisted to `persona.json` (mode 0600)
and reused for every request:

* Chrome major version, screen resolution (from a table of real-world
  Windows desktop resolutions incl. DPI-scaled ones like 1536x864@1.25),
  viewport and pixel ratio are randomized per install.
* The persona **survives restarts and binary upgrades** (stable identity —
  a fingerprint that changes per request is itself a bot signal).
* The UA string used in the signed query params and the UA actually sent in
  HTTP headers are the same variable (`zaiUserAgent`), so they can never
  diverge. The `Chrome_146` TLS profile is version-agnostic at the JA3
  level, so a Chrome/147–151 UA is indistinguishable on the wire.

Knobs: `GZ2A_PERSONA_FILE` (default `persona.json` next to the binary).
Delete the file to roll a new identity. The file contains no secrets.

## 3. Plaintext DNS to third parties — FIXED (DoH)

**Before** (`dnsfix.go`) — on Android/Termux (no usable
`/etc/resolv.conf`) every lookup went out as **plaintext UDP/TCP 53** to
1.1.1.1/8.8.8.8/9.9.9.9: the ISP (and anyone on-path) could read and log
the `chat.z.ai` / Aliyun captcha lookup pattern.

**After** — DNS-over-HTTPS (RFC 8484):

* Default endpoints are **IP literals**
  (`https://1.1.1.1/dns-query`, `https://8.8.8.8/dns-query`,
  `https://9.9.9.9/dns-query`) so the DoH bootstrap itself needs no DNS
  lookup — no recursion, no chicken-and-egg.
* Mode selection (`GZ2A_DNS_MODE`):
  * `auto` (default): DoH is used on exactly the path that used to leak —
    no usable resolv.conf (Android/Termux). A normal Linux resolv.conf is
    left 100% untouched.
  * `doh`: force DoH even with a working resolv.conf.
  * `plain`: old plaintext behavior.
  * `system` (or `GZ2A_DNS=system`): stock Go resolver, hook disabled.
* `GZ2A_DOH` overrides the endpoint list (https only).
* Graceful degradation: if DoH is unreachable (captive portal, blocked
  443), the relay falls back to one plaintext UDP round trip rather than
  failing hard.
* Implementation note: Go's resolver can only be retargeted at the
  connection level, so DoH is grafted on via a `net.Pipe` relay. The
  resolver-facing end is wrapped to satisfy `net.PacketConn` — without
  that, the resolver silently falls back to TCP framing and every DoH
  query times out (this was caught and fixed; see
  `doh_test.go`/`doh_conn_test.go`).

Verified live: `chat.z.ai` resolves through the DoH shim in ~60–80 ms and
the full guest-session bootstrap (config scrape → `fe_version
prod-fe-1.2.18` → connected) works with `GZ2A_DNS_MODE=doh`.

## 4. Local network attack surface — FIXED

**Before** (`main.go` + `dashboard.go`):

* bound `0.0.0.0` by default (any device on LAN/Wi-Fi could reach it);
* `Access-Control-Allow-Origin: *` on every response;
* `/status` was unauthenticated and returned `userName`, `userId`,
  `feVersion`, token/account-pool counts;
* `/admin/stats`, `/admin/health`, `/admin/clients` were unauthenticated;
* **the auth token was embedded in the dashboard HTML source**
  (`__AUTH_TOKEN__` → literal token), so anyone who could load the page
  could read the API token from view-source.

**After**:

* Default bind is **`127.0.0.1`**. To expose on the network you must now
  opt in explicitly: `HOST=0.0.0.0`. (Bridge clients on the same phone
  keep working unchanged.)
* CORS: the wildcard is gone. No `Access-Control-Allow-Origin` header is
  emitted unless you set `GZ2A_CORS_ORIGIN` (single explicit origin). The
  dashboard is same-origin and needs no CORS at all.
* `/status`, `/admin/stats`, `/admin/health`, `/admin/clients` now require
  the Bearer token like the rest of the API.
* The dashboard no longer receives the token from the server. On first
  load it prompts for it once ("API token" button), stores it in
  `localStorage` only, and sends it as a header. The served HTML contains
  no secrets.

## Not fixed (skipped by request)

* **Finding 5 — local disk/log leakage:** `LOG_LEVEL=debug` still prints
  full request bodies; some log lines include uid/name. Mitigation if you
  care: keep `LOG_LEVEL=info` and run in a private directory.
* **Finding 6 — server-side residue:** chats are still not deleted after
  use (workspace cleanup exists, chat deletion does not), and
  `background_tasks` still requests `title_generation`/`tags_generation`.

## New environment variables

| variable | default | meaning |
|---|---|---|
| `GZ2A_TIMEZONE` | device zone | IANA timezone for the persona (match your exit IP) |
| `GZ2A_PERSONA_FILE` | `persona.json` | where the per-install fingerprint is persisted |
| `GZ2A_DNS_MODE` | `auto` | `auto` / `doh` / `plain` / `system` |
| `GZ2A_DOH` | Cloudflare/Google/Quad9 | comma-separated `https://…/dns-query` endpoints |
| `GZ2A_CORS_ORIGIN` | *(unset = none)* | single allowed CORS origin, opt-in |

Changed defaults to be aware of: `HOST` is now `127.0.0.1` (was
`0.0.0.0`), `/status` and `/admin/*` now require auth, no wildcard CORS.

## Building

```
# host
go build ./cmd/server

# Android/Termux (static, no NDK needed)
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o glm-zai-2api ./cmd/server

# tests (DoH + persona + signature wiring; some need network)
go test ./internal/app/
```

## Files touched

```
internal/app/persona.go      NEW — persona: timezone + fingerprint, persistence, tzdata
internal/app/tlsclient.go    zaiUserAgent: const → persona-driven var
internal/app/zai.go          signed params (screen/viewport/ratio, timezone/offset/local_time) + {{CURRENT_*}} from persona
internal/app/probe_run.go    {{CURRENT_*}} from persona
internal/app/dnsfix.go       DoH relay (PacketConn pipe), mode selection, plain fallback
internal/app/main.go         HOST 127.0.0.1, CORS opt-in, auth on /status + /admin/*, no token in HTML
internal/app/dashboard.go    token prompt (localStorage), no embedded token, updated endpoint docs
internal/app/doh_test.go           NEW — DoH exchange + full-resolver tests
internal/app/doh_conn_test.go      NEW — pipe framing tests (udp/tcp)
internal/app/signature_test.go     NEW — persona → signed params wiring test
internal/app/serverstyle_test.go   NEW — server-wiring reproduction test
```
