#!/data/data/com.termux/files/usr/bin/bash
# install.sh — one-shot installer for GZ2A on Android (Termux).
#
# Run it from the extracted gz2a-android folder:
#   bash install.sh
#
# What it does:
#   1. verifies Termux + arm64 CPU
#   2. installs ~/gz2a/ (binary, helper, .env, proxy.txt)
#   3. copies a fresh tokens.sqlite (only on first install)
#   4. generates a random AUTH_TOKEN
#   5. links the `gz2a` command into PATH
#   6. optionally installs the boot-autostart script (needs Termux:Boot)

set -u

C_G="\033[32m"; C_R="\033[31m"; C_Y="\033[33m"; C_B="\033[36m"; C_0="\033[0m"
say() { printf "%b\n" "$*"; }
ok()  { printf "%b\n" "${C_G}✔${C_0} $*"; }
bad() { printf "%b\n" "${C_R}✘${C_0} $*" >&2; }
info(){ printf "%b\n" "${C_B}ℹ${C_0} $*"; }

SRC="$(cd "$(dirname "$0")" && pwd)"
DEST="$HOME/gz2a"

# ── 1. environment checks ─────────────────────────────────────────────
if ! case "$PREFIX" in /data/data/com.termux*) true;; *) false;; esac; then
  bad "This installer must run inside Termux."
  info "Install Termux from F-Droid (the Play Store build is outdated):"
  info "  https://f-droid.org/en/packages/com.termux/"
  exit 1
fi

case "$(uname -m)" in
  aarch64|arm64) ok "CPU: arm64 — binary matches";;
  armv7l|armv8l)
    bad "Your device is 32-bit ARM. GZ2A needs a 64-bit device (arm64)."
    exit 1;;
  *) bad "Unexpected CPU: $(uname -m)."; exit 1;;
esac

# ── 2. files ──────────────────────────────────────────────────────────
mkdir -p "$DEST"
for f in bin/glm-zai-2api bin/mktokens termux/gz2a; do
  [ -f "$SRC/$f" ] || { bad "Missing: $SRC/$f — run me from the extracted gz2a-android folder."; exit 1; }
done
install -m 0755 "$SRC/bin/glm-zai-2api" "$DEST/glm-zai-2api"
install -m 0755 "$SRC/bin/mktokens"      "$DEST/mktokens"
install -m 0755 "$SRC/termux/gz2a"       "$DEST/gz2a"
[ -f "$SRC/conf/proxy.txt" ] && install -m 0644 "$SRC/conf/proxy.txt" "$DEST/proxy.txt"

# tokens.sqlite: keep any existing DB (upgrades never wipe your tokens)
if [ -f "$DEST/tokens.sqlite" ]; then
  info "Keeping existing tokens.sqlite (delete it first if you want a fresh copy)."
elif [ -f "$SRC/data/tokens.sqlite" ]; then
  install -m 0644 "$SRC/data/tokens.sqlite" "$DEST/tokens.sqlite"
  ok "Installed bundled tokens.sqlite ($(stat -c%s "$DEST/tokens.sqlite") bytes)."
fi

# ── 3. .env ───────────────────────────────────────────────────────────
if [ -f "$DEST/.env" ]; then
  info "Keeping existing .env."
else
  NEW_TOKEN="$(od -An -tx1 -N12 /dev/urandom | tr -d ' \n')"
  sed "s/^AUTH_TOKEN=.*/AUTH_TOKEN=$NEW_TOKEN/" "$SRC/conf/env.txt" > "$DEST/.env"
  chmod 600 "$DEST/.env"
  ok "Created .env with a random API key: $NEW_TOKEN"
fi

# ── 4. PATH command ───────────────────────────────────────────────────
ln -sf "$DEST/gz2a" "$PREFIX/bin/gz2a"
ok "Command available everywhere: gz2a"

# ── 5. curl (usually preinstalled) ────────────────────────────────────
command -v curl >/dev/null 2>&1 || { info "Installing curl…"; pkg install -y curl || true; }
# Belt & suspenders for the TLS trust store: the binary self-heals from the
# Android system store on its own (certfix1), but Termux's own CA bundle
# covers exotic ROMs too. Harmless if already present.
if [ ! -f "$PREFIX/etc/tls/cert.pem" ]; then
  info "Installing ca-certificates (TLS trust store)…"
  pkg install -y ca-certificates >/dev/null 2>&1 \
    && ok "CA certificates installed." \
    || info "Skipped — the bridge will use the Android system store automatically."
fi

# ── 6. optional: storage access for 'gz2a refresh' ────────────────────
if [ ! -d "$HOME/storage/downloads" ]; then
  info "For the easiest token refresh, allow storage access when prompted…"
  termux-setup-storage 2>/dev/null || true
  [ -d "$HOME/storage/downloads" ] && ok "Downloads linked." \
    || info "Skipped. You can run 'termux-setup-storage' later."
fi

# ── 7. optional: boot autostart ───────────────────────────────────────
BOOT_DIR="$HOME/.termux/boot"
if [ -d "$BOOT_DIR" ]; then
  install -m 0755 "$SRC/termux/boot-gz2a.sh" "$BOOT_DIR/gz2a.sh"
  ok "Boot autostart installed (starts ~15s after phone boot)."
else
  info "Autostart on boot: install the 'Termux:Boot' app (F-Droid), open it once,"
  info "then re-run this installer to enable autostart."
fi

# ── done ──────────────────────────────────────────────────────────────
say ""
say "${C_B}──────────────── GZ2A installed ────────────────${C_0}"
say "  Start it:      ${C_G}gz2a start${C_0}"
say "  Test it:       ${C_G}gz2a test${C_0}"
say "  Client config: ${C_G}gz2a url${C_0}"
say ""
say "  Use it in any OpenAI-compatible app on this phone:"
say "    Base URL : http://127.0.0.1:1337/v1"
say "    API key  : (run 'gz2a token')"
say "    Model    : glm-4.7  (guest) — GLM-5 models after 'gz2a settoken <jwt>'"
say ""
say "  Device tokens are single-use. When 'gz2a tokens' hits 0 or chats fail with"
say "  'captcha verification failed' → refresh them (README §4): ~60 seconds."
say "${C_B}────────────────────────────────────────────────${C_0}"
