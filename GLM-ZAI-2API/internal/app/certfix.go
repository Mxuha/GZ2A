// certfix.go — root-CA fallback so GZ2A works on Android/Termux.
//
// Background: the server ships as a static CGO_ENABLED=0 binary, so TLS
// certificate verification uses the Go standard library's trust-store
// loader, which looks for /etc/ssl/certs/ca-certificates.crt (plus a few
// other desktop-Linux paths). None of those exist on Android:
//
//      Post "https://chat.z.ai/api/v1/chats/new": tls: failed to verify
//      certificate: x509: certificate signed by unknown authority
//
// Termux cannot create /etc without root, and on Android 14+ even the
// legacy store /system/etc/security/cacerts is empty (the live store moved
// into the conscrypt APEX module, which Go does not read). The health
// check still passes — it is local-only — and only the first real chat
// completion reveals the breakage.
//
// This file builds ONE merged root pool, used by every outbound TLS client
// in the process (the browser-fingerprinted tls-client stack AND the plain
// stdlib image fetcher). Sources, in order:
//
//   1. GZ2A_CA_FILE / GZ2A_CA_DIR — explicit override, honoured first;
//   2. the Termux ca-certificates bundle ($PREFIX/etc/tls/…);
//   3. the Android system store: /system/etc/security/cacerts (Android
//      ≤ 13) and /apex/com.android.conscrypt/cacerts (Android ≥ 14);
//   4. the standard library's own system pool (SSL_CERT_FILE,
//      SSL_CERT_DIR and the usual Linux paths), always merged in;
//   5. the desktop-Linux paths are also scanned directly, but only as a
//      rescue when the stdlib loader itself came back empty.
//
// Normal Linux servers: none of the Android paths exist, the stdlib pool
// is fine and nothing is appended — verification behaves 100% as before.
package app

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	rootOnce sync.Once
	rootPool *x509.CertPool // nil = keep Go's default (system) verification
)

// rootCAPool returns the merged root pool, or nil when the stock system
// verification is already fine and nothing needs to change. Lazy: the
// scan runs once, on first outbound TLS in the process.
func rootCAPool() *x509.CertPool {
	rootOnce.Do(loadRootPool)
	return rootPool
}

// androidCAFiles / androidCADirs never exist on desktop Linux, so scanning
// them there is a no-op. Termux prefix is hardcoded because Termux is the
// only supported Android runtime (the installer targets it explicitly).
var (
	overrideCAFiles = []string{
		// explicit user override first
		"/data/data/com.termux/files/usr/etc/tls/cert.pem", // Termux ca-certificates bundle
	}
	overrideCADirs = []string{
		"/data/data/com.termux/files/usr/etc/tls/certs",
		"/system/etc/security/cacerts",        // Android ≤ 13 system store
		"/apex/com.android.conscrypt/cacerts", // Android ≥ 14 system store (APEX)
	}
	rescueCAFiles = []string{ // desktop-Linux paths, only scanned if the stdlib loader found nothing
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/pki/tls/certs/ca-bundle.crt",
		"/etc/ssl/ca-bundle.pem",
		"/etc/pki/tls/cacert.pem",
		"/etc/ssl/cert.pem",
	}
	rescueCADirs = []string{"/etc/ssl/certs"}
)

func loadRootPool() {
	// 1. What does the standard library think?
	sys, sysErr := x509.SystemCertPool()
	sysCount := 0
	if sysErr == nil && sys != nil {
		//nolint:staticcheck // Subjects() is deprecated but only used to count
		sysCount = len(sys.Subjects())
	}

	// 2. Collect certificates the stdlib does not know about.
	files := append([]string{}, overrideCAFiles...)
	dirs := append([]string{}, overrideCADirs...)
	if v := strings.TrimSpace(os.Getenv("GZ2A_CA_FILE")); v != "" {
		files = append([]string{v}, files...) // override wins
	}
	if v := strings.TrimSpace(os.Getenv("GZ2A_CA_DIR")); v != "" {
		dirs = append([]string{v}, dirs...)
	}
	if sysCount == 0 {
		// stdlib found nothing (broken SSL_CERT_FILE? stripped system?):
		// try the classic paths ourselves as a rescue.
		files = append(files, rescueCAFiles...)
		dirs = append(dirs, rescueCADirs...)
	}

	var (
		extras       []*x509.Certificate
		seen         = map[[sha256.Size]byte]bool{}
		contributors []string
	)
	for _, f := range files {
		if n := harvestCAFile(&extras, seen, f); n > 0 {
			contributors = append(contributors, fmt.Sprintf("%s: %d", shortCAName(f), n))
		}
	}
	for _, d := range dirs {
		if n := harvestCADir(&extras, seen, d); n > 0 {
			contributors = append(contributors, fmt.Sprintf("%s: %d", shortCAName(d), n))
		}
	}

	// 3. Decide.
	if len(extras) == 0 {
		if sysCount == 0 {
			log.Printf("[CA] WARNING: no root certificates found anywhere — TLS to chat.z.ai will fail")
			log.Printf("[CA] fix: 'pkg install ca-certificates' in Termux, or point GZ2A_CA_FILE at a CA bundle")
		}
		return // stock verification (fine on a normal system)
	}

	merged := x509.NewCertPool()
	if sysErr == nil && sys != nil && sysCount > 0 {
		merged = sys.Clone()
	}
	for _, c := range extras {
		merged.AddCert(c)
	}
	rootPool = merged

	if sysCount > 0 {
		log.Printf("[CA] merged %d extra roots into the system trust pool (%s)", len(extras), strings.Join(contributors, ", "))
	} else {
		log.Printf("[CA] no usable system trust store (Android/Termux?) — loaded %d roots from %s", len(extras), strings.Join(contributors, ", "))
	}
}

// harvestCAFile appends every parseable certificate of a PEM bundle to
// extras (deduped). Returns how many were added.
func harvestCAFile(extras *[]*x509.Certificate, seen map[[sha256.Size]byte]bool, path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	added := 0
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		fp := sha256.Sum256(cert.Raw)
		if seen[fp] {
			continue
		}
		seen[fp] = true
		*extras = append(*extras, cert)
		added++
	}
	return added
}

// harvestCADir scans a hashed-cert directory (the Android cacerts layout:
// one PEM cert per file named <subject-hash>.0) and returns how many
// certificates were added.
func harvestCADir(extras *[]*x509.Certificate, seen map[[sha256.Size]byte]bool, dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	added := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		added += harvestCAFile(extras, seen, filepath.Join(dir, e.Name()))
	}
	return added
}

// shortCAName keeps log lines readable: full path for user overrides,
// a short tag for the built-in Android/Termux locations.
func shortCAName(path string) string {
	switch path {
	case "/data/data/com.termux/files/usr/etc/tls/cert.pem":
		return "Termux bundle"
	case "/data/data/com.termux/files/usr/etc/tls/certs":
		return "Termux certs dir"
	case "/system/etc/security/cacerts":
		return "Android system store"
	case "/apex/com.android.conscrypt/cacerts":
		return "Android 14+ store (APEX)"
	case "/etc/ssl/certs":
		return "/etc/ssl/certs"
	}
	if strings.HasPrefix(path, "/data/data/com.termux/files/usr/etc/tls/") {
		return path // user override inside Termux — show it in full
	}
	return path
}
