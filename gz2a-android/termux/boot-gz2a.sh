#!/data/data/com.termux/files/usr/bin/bash
# Termux:Boot script — starts the GZ2A bridge after the phone boots.
# Installed to ~/.termux/boot/gz2a.sh by install.sh (requires Termux:Boot app).

# Let the network settle before the guest session initializes.
sleep 15

# Hold a partial wake-lock so Android does not freeze the process
# while the phone screen is off.
command -v termux-wake-lock >/dev/null 2>&1 && termux-wake-lock >/dev/null 2>&1

"$HOME/gz2a/gz2a" start >> "$HOME/gz2a/gz2a.log" 2>&1
