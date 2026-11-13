# GZ2A on Android — GLM models as a local OpenAI-compatible API on your phone

This package runs [GLM-ZAI-2API](https://github.com/Mxuha/GZ2A)
(v1.1.1-**certfix1**) as a **native ARM64 binary inside Termux** — no root,
no PC needed after setup. This build also fixes the two Android issues
that break stock builds automatically — the DNS issue
(`lookup … [::1]:53: connection refused`) and the TLS issue
(`x509: certificate signed by unknown authority`) — see §7.

Your phone becomes a small API server:

```
┌─────────────┐   OpenAI / Anthropic / Responses API   ┌──────────────────┐
│  Any app    │ ──────────────────────────────────────▶ │  GZ2A bridge     │
│  on the     │   http://127.0.0.1:5082/v1              │  (Termux, arm64) │
│  phone      │                                          │  ▲          │   │
└─────────────┘                                          │  │ captcha   │   │
  ChatterUI, OpenCode,                                   │  ▼          ▼   │
  Claude-code clients, …                          ┌────────────┐ ┌────────┐
                                                  │ tokens.db  │ │ chat.  │
                                                  │ (z_um dev. │ │ z.ai   │
                                                  │  tokens)   │ │        │
                                                  └────────────┘ └────────┘
```

- **3 protocols**: OpenAI `/v1/chat/completions`, Anthropic `/v1/messages`,
  Responses `/v1/responses` — plus tool calling, streaming, vision.
- **Guest mode works out of the box** with `glm-4.7`. Add your Z.AI login
  token to unlock GLM-5 models + vision (§5).
- Everything is managed with one command: **`gz2a`**.

---

## 1. Requirements

| What | Notes |
|---|---|
| Android 7+ phone, **64-bit (arm64)** | virtually every phone since ~2017 |
| **Termux** | install from **F-Droid** — the Play Store build is outdated and breaks `pkg` |
| ~60 MB free space | binary 16 MB + tokens DB + Termux itself |
| Internet | Wi-Fi or mobile data; direct connection to chat.z.ai |

Optional but recommended (all free, from F-Droid):

| App | Why |
|---|---|
| **Termux:Boot** | auto-start the bridge after phone reboots |
| **Termux:API** | lets `gz2a refresh` read the clipboard (file-based refresh works without it) |
| **Kiwi or Lemur browser** + Tampermonkey | nicest token-refresh experience (§4) |

---

## 2. Install (one time, ~5 minutes)

1. **Install Termux from F-Droid**: <https://f-droid.org/en/packages/com.termux/>
   (Play Store version is unmaintained — do not use it.)

2. **Copy this `gz2a-android` folder to the phone** (download the zip and
   extract with any file manager, e.g. to `Download/gz2a-android/`).

3. **In Termux**, allow storage access (needed to read the folder and for
   token refresh later):
   ```bash
   termux-setup-storage     # tap "Allow"
   ```

4. **Run the installer**:
   ```bash
   bash /sdcard/Download/gz2a-android/install.sh
   ```
   It verifies your CPU, installs everything to `~/gz2a/`, generates a
   random API key, and (if Termux:Boot is present) enables autostart.

5. **Start & verify**:
   ```bash
   gz2a start
   gz2a test
   ```
   `gz2a test` sends a real chat completion. If it replies — you're live.

> **Important — device tokens are short-lived.** Measured lifetime is
> **≈ 30 minutes** (Aliyun captcha tokens). The bundled `tokens.sqlite` is a
> best-effort convenience that only works if you install *immediately* after
> downloading this package. The normal workflow is the 60-second refresh in
> §4 — do it right before each session and you'll never think about it again.

---

## 3. Daily use

```bash
gz2a start      # start the bridge (takes a wake-lock so Android keeps it)
gz2a status     # session + token count
gz2a url        # shows the exact client settings
gz2a stop       # stop (releases the wake-lock → battery friendly)
```

**Connect any OpenAI-compatible app on the phone** (ChatterUI, OpenCode,
Slide, your own scripts…):

| Setting | Value |
|---|---|
| Base URL | `http://127.0.0.1:5082/v1` |
| API key | run `gz2a token` |
| Model | `glm-4.7` (guest) — see §5 for GLM-5 |

Anthropic-protocol clients: same URL, header-based auth, endpoint
`/v1/messages`. Codex-style clients: `/v1/responses`.

Quick manual check:
```bash
curl http://127.0.0.1:5082/v1/chat/completions \
  -H "Content-Type: application/json" -H "Authorization: Bearer $(gz2a token)" \
  -d '{"model":"glm-4.7","messages":[{"role":"user","content":"Hello!"}]}'
```

The built-in dashboard is at `http://127.0.0.1:5082` in your phone browser.

### Using other devices (tablet / laptop on the same Wi-Fi or hotspot)
```bash
gz2a expose on     # bind 0.0.0.0 (gz2a url now shows the LAN address)
gz2a expose off    # back to phone-only (recommended default)
```

---

## 4. Refreshing device tokens (the one routine you need)

Each chat burns **one single-use device token** (the Aliyun captcha the
bridge solves for you), and tokens **expire ≈ 30 minutes after they are
minted**. So the rhythm is: refresh right before a session, then chat away.
A batch of 100 tokens ≈ 100 chats inside that window. When you see
`captcha verification failed` or `gz2a tokens` reports 0:

### The 60-second refresh

1. Open **https://chat.z.ai** in your phone browser.

2. **Collect a batch** — either way works:
   - **Kiwi/Lemur + Tampermonkey** (installed once from
     `browser/gz2a-token-collector.user.js`): a small GZ2A panel appears on
     chat.z.ai → **Collect tokens** → it auto-triggers the SDK, collects
     ~250 tokens, saves `zum-tokens-….txt` to Downloads *and* clipboard.
   - **Any browser**: use the bookmarklet from `browser/bookmarklet.txt`
     (create a bookmark with that `javascript:` URL; send a chat message
     first, then invoke it by typing the bookmark's name in the address bar).

3. **In Termux**:
   ```bash
   gz2a refresh          # auto-finds the newest zum-tokens*.txt in Downloads
   gz2a test             # confirm
   ```

`gz2a refresh` also accepts an explicit file (`gz2a refresh /sdcard/Download/zum-tokens-….txt`)
or reads the clipboard if Termux:API is installed.

> ⏱ **Collect fresh, use immediately.** Tokens die ≈ 30 min after minting —
> don't stockpile. 100 tokens per refresh is the sweet spot (the userscript
> count is adjustable up to 1000; collecting takes seconds either way).
>
> Have a PC with Chrome handy instead? The upstream token-collector works
> there too: `go build -o token-collector ./cmd/token-collector` in the
> GZ2A source, then `token-collector --count 750 --out tokens.sqlite` and
> copy the file to `~/gz2a/tokens.sqlite` — same 30-minute clock applies.

---

## 5. Unlock GLM-5 models + vision (optional)

Guest mode gives you `glm-4.7`. To use `GLM-5.x`, `GLM-5v-Turbo` (vision)
etc., give the bridge your Z.AI login token:

1. Log in at <https://chat.z.ai> in your phone browser.
2. If you use the userscript (§4): press **Copy Z.AI token**.
   Otherwise run this in the page console / a `javascript:` bookmarklet:
   `javascript:prompt('Your Z.AI token:',localStorage.getItem('token'))`
3. In Termux:
   ```bash
   gz2a settoken eyJ...your.jwt...
   gz2a test
   curl -H "Authorization: Bearer $(gz2a token)" http://127.0.0.1:5082/v1/models
   ```
   `gz2a cleartoken` returns to guest mode.

Vision example (requires login token + `GLM-5v-Turbo`):
```bash
curl http://127.0.0.1:5082/v1/chat/completions \
  -H "Content-Type: application/json" -H "Authorization: Bearer $(gz2a token)" \
  -d '{"model":"GLM-5v-Turbo","messages":[{"role":"user","content":[
    {"type":"text","text":"What is in this image?"},
    {"type":"image_url","image_url":{"url":"https://example.com/cat.jpg"}}]}]}'
```

---

## 6. Keep it running reliably

Android loves killing background apps. Three settings fix that:

1. **`gz2a start` already holds a Termux wake-lock** (partial). For
   long-running use, also disable battery optimization for Termux:
   *Android Settings → Apps → Termux → Battery → Unrestricted*
   (exact wording varies by vendor).

2. **Auto-start after reboot** — install **Termux:Boot** (F-Droid), open it
   once, then re-run `bash install.sh` (or copy
   `termux/boot-gz2a.sh` to `~/.termux/boot/gz2a.sh` manually).

3. If you use the phone as a server for other devices, keep it plugged in
   or enable "Keep device awake while charging" in Termux settings
   (`termux-wake-lock` full version: tap the *Acquire wakelock* notification
   action).

If the bridge ever dies, just `gz2a start` again — state is only the
token DB, nothing else is lost.

---

## 7. Troubleshooting

| Symptom | Fix |
|---|---|
| `captcha verification failed` | Device tokens empty or older than ~30 min → §4 refresh (then chat immediately) |
| `No device tokens available` | Same — 0 tokens left |
| `Model not available for current user level` | Guest mode + GLM-5 model → §5 |
| `file upload unauthorized (401)` on images | Vision needs the login token → §5 |
| `401` from the bridge itself | Wrong API key in the client — check `gz2a token` |
| Chats suddenly stop, log shows WAF / 405 | Your IP got rate-limited by Aliyun. Wait it out, or configure a proxy pool (`.env` → `USE_PROXY=true`, `PROXY_FILE=proxy.txt`) |
| `Connection refused` | Bridge not running → `gz2a start`; if start fails, `gz2a doctor` and `gz2a logs` |
| Died overnight | Battery optimization → §6 |
| `Binary will not execute` in doctor | Not arm64, or Termux from Play Store — reinstall from F-Droid |
| DNS errors in logs / `lookup … [::1]:53` | **Fixed in this build (dnsfix1)** — on Android the bridge now resolves via public resolvers automatically. If you still see `[::1]:53`, you are running the old binary: re-run `install.sh` from this package, then `gz2a restart` |
| `x509: certificate signed by unknown authority` | **Fixed in this build (certfix1)** — Android has no `/etc/ssl`, so stock builds cannot verify chat.z.ai's TLS certificate. The bridge now loads root CAs from the Termux bundle + the Android system store automatically. If you still see x509 errors, you are running the old binary: re-run `install.sh`, then `gz2a restart`. If a NEW install still shows x509, your network does TLS interception (corporate/VPN) → set `GZ2A_CA_FILE` in `~/gz2a/.env` to your proxy's CA bundle |

**How the DNS fix works.** Android ships no `/etc/resolv.conf`, and a
static Go binary cannot use Android's system resolver — so plain builds
fail every outbound request with `lookup chat.z.ai on [::1]:53: …
connection refused` (health can still say *healthy*, which made this
extra confusing). dnsfix1 detects the missing/unusable resolv.conf at
startup (log line `[DNS] …`) and transparently queries public resolvers
instead (1.1.1.1, 8.8.8.8, 9.9.9.9 and their IPv6 twins, plain UDP/TCP 53).
On normal Linux servers the stock behaviour is unchanged. On some exotic
network that blocks outbound UDP/53, point it anywhere you like with
`GZ2A_DNS=host[:port],…` in `~/gz2a/.env` (`GZ2A_DNS=system` disables the
fallback).

**How the TLS fix works.** Android ships no `/etc/ssl` — Termux cannot
create it without root — and on Android 14+ even the legacy system store
`/system/etc/security/cacerts` is empty (the real one moved into an APEX
module Go knows nothing about). Stock builds therefore fail *every*
outbound HTTPS call to chat.z.ai with `tls: failed to verify certificate:
x509: certificate signed by unknown authority` — the health check still
says *healthy* right up to the moment a chat is sent, which makes this
extra confusing. certfix1 builds one merged root pool at startup (log line
`[CA] …`) from the Termux `ca-certificates` bundle, the Android system
store (legacy **and** APEX locations) and Go's own store — used by every
outbound connection, including image downloads for vision. On normal
Linux servers nothing changes. On networks that intercept TLS (corporate
proxies, some VPNs) point `GZ2A_CA_FILE=/path/to/ca-bundle.pem` (or
`GZ2A_CA_DIR=/path/to/certs/`) at the interceptor's CA in `~/gz2a/.env`.

`gz2a doctor` runs all checks in one go. `gz2a logs -f` follows the live log.

---

## 8. Security & fairness

- The bridge binds to `127.0.0.1` by default — only apps **on your phone**
  can reach it. `gz2a expose on` opens it to your local network; the API
  key still applies, but use it only on networks you trust.
- Your `ZAI_TOKEN` (if set) sits in `~/gz2a/.env` — Termux's app-private
  storage, not readable by other apps. Still: don't hand your
  `~/gz2a` folder around while logged in.
- This is a community reverse-engineered bridge around Z.AI's web chat,
  provided by its author for interoperability/educational use. Use it
  reasonably: heavy automated traffic can rate-limit or block your account
  or IP, and it may break whenever Z.AI changes its site. Be a good
  citizen.

---

## 9. Package contents & housekeeping

```
gz2a-android/
├── install.sh                     ← run this (§2)
├── bin/
│   ├── glm-zai-2api               ← static linux/arm64 server (v1.1.0, Go)
│   └── mktokens                   ← rebuilds tokens.sqlite from token text
├── conf/
│   ├── env.txt                    ← .env template (auto-installed)
│   └── proxy.txt                  ← proxy pool template (optional)
├── data/
│   └── tokens.sqlite              ← fresh device-token batch (best-effort)
├── termux/
│   ├── gz2a                       ← the CLI helper (§3)
│   └── boot-gz2a.sh               ← Termux:Boot autostart hook
├── browser/
│   ├── gz2a-token-collector.user.js
│   └── bookmarklet.txt
└── README-ANDROID.md              ← this file
```

**Update to a newer GZ2A version**: replace `bin/glm-zai-2api`
(build from source with `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build
-trimpath -ldflags="-s -w" -o glm-zai-2api ./cmd/server`) and
`gz2a restart`. Your `.env`, tokens and scripts are kept.

**Uninstall**: `gz2a stop && rm -rf ~/gz2a $PREFIX/bin/gz2a
~/.termux/boot/gz2a.sh`.
