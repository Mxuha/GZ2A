// dnsfix.go — process-wide DNS fallback so GZ2A works on Android/Termux.
//
// Background: the server ships as a static CGO_ENABLED=0 binary, so DNS
// always uses Go's internal resolver, which reads /etc/resolv.conf.
// Android has no /etc/resolv.conf (Termux cannot create /etc without
// root), and Go then falls back to a resolver on 127.0.0.1:53 / [::1]:53
// where nothing listens:
//
//      lookup chat.z.ai on [::1]:53: read udp [::1]:xxx->[::1]:53:
//      read: connection refused
//
// This file installs a replacement net.DefaultResolver — used by every
// dialer in the process, including the tls-client/fhttp stack:
//
//   - GZ2A_DNS="host[:port][,host…]"  → always use exactly these servers
//     (plaintext; GZ2A_DNS=system disables this hook entirely);
//   - /etc/resolv.conf missing, empty or without usable servers (the
//     Android case) → DNS-over-HTTPS (RFC 8484) by default: the ISP can no
//     longer read the z.ai lookup traffic. If DoH is unreachable (captive
//     portal, blocked port 443) it degrades to plaintext public resolvers
//     (1.1.1.1, 8.8.8.8, 9.9.9.9 and their IPv6 twins) rather than fail;
//   - /etc/resolv.conf lists only loopback servers (rooted phones with
//     a local DNS) → try them first, fall back to public resolvers if
//     they are dead;
//   - a normal resolv.conf with real servers → behaviour 100% unchanged
//     (set GZ2A_DNS_MODE=doh to force DoH anyway).
//
// Knobs:
//   GZ2A_DNS_MODE=auto|doh|plain|system   (default auto — see above)
//   GZ2A_DOH="https://ip/dns-query,…"     (default Cloudflare/Google/Quad9
//                                          IP-literal endpoints, so the DoH
//                                          bootstrap itself needs no DNS)
package app

import (
        "bytes"
        "context"
        "fmt"
        "io"
        "log"
        "net"
        "net/http"
        "os"
        "strings"
        "sync"
        "time"
)

var (
        dnsPublicV4 = []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53"}
        dnsPublicV6 = []string{"[2606:4700:4700::1111]:53", "[2001:4860:4860::8888]:53", "[2620:fe::fe]:53"}

        dnsForced    []string // from GZ2A_DNS, nil = not set
        dnsSysUsable bool     // resolv.conf has ≥1 non-loopback server
        dnsOnlyLoop  bool     // resolv.conf servers are all loopback

        dnsUseDoH       bool     // route resolver traffic through DoH
        dnsDoHEndpoints []string // https://…/dns-query URLs (IP-literal hosts preferred)

        dnsMu    sync.Mutex
        dnsTries = map[string]int{} // server address → times dialed (rotation)
)

// dohDefaultEndpoints — IP literals on purpose: resolving them needs no DNS
// at all, so there is no bootstrap lookups and no recursion in the DoH path.
const dohDefaultEndpoints = "https://1.1.1.1/dns-query,https://8.8.8.8/dns-query,https://9.9.9.9/dns-query"

func init() {
        mode := strings.ToLower(strings.TrimSpace(envOr("GZ2A_DNS_MODE", "auto")))
        if strings.TrimSpace(os.Getenv("GZ2A_DNS")) == "system" || mode == "system" {
                return // escape hatch: keep Go's stock behaviour
        }
        dnsForced = parseDNSList(os.Getenv("GZ2A_DNS"))
        dnsSysUsable, dnsOnlyLoop = inspectResolvConf()
        dnsDoHEndpoints = parseDoHList(envOr("GZ2A_DOH", dohDefaultEndpoints))

        switch mode {
        case "doh":
                dnsUseDoH = true
        case "plain":
                dnsUseDoH = false
        default: // auto: encrypt exactly the traffic that used to leak —
                 // the no-resolv.conf (Android/Termux) path.
                dnsUseDoH = len(dnsForced) == 0 && !dnsSysUsable
        }
        if dnsUseDoH && len(dnsDoHEndpoints) == 0 {
                log.Printf("[DNS] GZ2A_DOH produced no usable https endpoints — falling back to plaintext")
                dnsUseDoH = false
        }

        switch {
        case dnsUseDoH:
                log.Printf("[DNS] encrypted DNS (DoH) via %s — z.ai lookups are no longer readable by the ISP", strings.Join(dnsDoHEndpoints, ", "))
        case len(dnsForced) > 0:
                log.Printf("[DNS] using override from GZ2A_DNS: %s", strings.Join(dnsForced, ", "))
        case !dnsSysUsable:
                all := append(append([]string{}, dnsPublicV4...), dnsPublicV6...)
                log.Printf("[DNS] /etc/resolv.conf unusable (Android/Termux?) — using public resolvers %s", strings.Join(all, ", "))
        case dnsOnlyLoop:
                log.Printf("[DNS] /etc/resolv.conf lists only loopback servers — public resolvers on standby")
        }

        net.DefaultResolver = &net.Resolver{
                PreferGo: true, // the binary is CGO-free; keeps resolution deterministic
                Dial:     dnsDial,
        }
}

// dnsDial is called by the resolver for every DNS server connection.
func dnsDial(ctx context.Context, network, address string) (net.Conn, error) {
        if dnsUseDoH {
                // Signed/encrypted path: hand back a pipe whose far end
                // relays the wire-format query over DoH (see dohPump).
                return newDoHConn(network)
        }
        switch {
        case len(dnsForced) > 0:
                return dialAddr(ctx, network, dnsForced[nextDNSIndex(address, len(dnsForced))])
        case dnsSysUsable && !dnsOnlyLoop:
                return dialAddr(ctx, network, address) // sane system config → untouched
        case dnsSysUsable && dnsOnlyLoop:
                if conn, err := dialAddr(ctx, network, address); err == nil {
                        return conn, nil // local resolver alive → use it
                }
                return dialAddr(ctx, network, publicServerFor(address)) // dead → fallback
        default:
                // No usable resolv.conf (Android/Termux): address is one of Go's
                // loopback fallbacks — reroute to a public server of the same
                // address family, rotating through the pool on retries.
                return dialAddr(ctx, network, publicServerFor(address))
        }
}

// publicServerFor maps a (loopback) DNS server address to the next public
// server of the same address family, rotating through the pool on retries.
func publicServerFor(address string) string {
        pool := dnsPublicV4
        if strings.HasPrefix(address, "[") { // IPv6 dial address, e.g. "[::1]:53"
                pool = dnsPublicV6
        }
        dnsMu.Lock()
        dnsTries[address]++
        idx := (dnsTries[address] - 1) % len(pool)
        dnsMu.Unlock()
        return pool[idx]
}

// nextDNSIndex rotates through the forced GZ2A_DNS list.
func nextDNSIndex(address string, n int) int {
        dnsMu.Lock()
        dnsTries[address]++
        i := (dnsTries[address] - 1) % n
        dnsMu.Unlock()
        return i
}

func dialAddr(ctx context.Context, network, address string) (net.Conn, error) {
        var d net.Dialer
        return d.DialContext(ctx, network, address)
}

// ── DNS-over-HTTPS (RFC 8484) ─────────────────────────────────────────────
//
// Go's resolver can only be retargeted at the connection level, so we give
// it a net.Pipe and translate classic DNS traffic on the near end into
// application/dns-message POSTs on the far end. IP-literal DoH endpoints
// mean the HTTPS connection itself needs no DNS lookup — no bootstrap, no
// recursion.

func newDoHConn(network string) (net.Conn, error) {
        local, remote := net.Pipe()
        go dohPump(local, strings.HasPrefix(network, "tcp"))
        if !strings.HasPrefix(network, "tcp") {
                // The Go resolver dials "udp" but then dispatches on the
                // net.PacketConn type assertion: a plain pipe fails it and
                // the resolver silently switches to TCP framing (2-byte
                // length prefix), which this relay would forward as a
                // malformed query. Wrapping the resolver-facing end makes
                // the assertion succeed, so raw datagram framing is kept.
                return packetPipeConn{Conn: remote}, nil
        }
        return remote, nil
}

// packetPipeConn makes a net.Pipe end satisfy net.PacketConn (ReadFrom/
// WriteTo forward to the stream; the resolver never uses their addr args).
type packetPipeConn struct {
        net.Conn
}

func (p packetPipeConn) ReadFrom(b []byte) (int, net.Addr, error) {
        n, err := p.Conn.Read(b)
        return n, nil, err
}

func (p packetPipeConn) WriteTo(b []byte, _ net.Addr) (int, error) {
        return p.Conn.Write(b)
}

// dohPump relays the resolver's classic DNS-over-conn traffic to DoH.
// net.Pipe preserves Write boundaries, and the resolver writes each query
// message with a single Write, so one Read == one query.
func dohPump(conn net.Conn, tcp bool) {
        defer conn.Close()
        buf := make([]byte, 65536)
        for {
                n, err := conn.Read(buf)
                if err != nil {
                        return
                }
                if n == 0 {
                        continue
                }
                q := append([]byte(nil), buf[:n]...)
                if tcp { // DNS-over-TCP frames carry a 2-byte length prefix
                        if len(q) < 2 {
                                return
                        }
                        flen := int(q[0])<<8 | int(q[1])
                        if len(q)-2 < flen {
                                return
                        }
                        q = q[2 : 2+flen]
                }
                resp, rerr := dohExchange(q)
                if rerr != nil {
                        // DoH unreachable (captive portal, blocked 443) —
                        // degrade to plaintext rather than fail hard.
                        resp, rerr = plainExchange(q)
                        if rerr != nil {
                                return
                        }
                }
                if tcp {
                        resp = append([]byte{byte(len(resp) >> 8), byte(len(resp))}, resp...)
                }
                if _, werr := conn.Write(resp); werr != nil {
                        return
                }
        }
}

// dohExchange POSTs a wire-format query to the DoH endpoints, rotating
// through them across retries.
func dohExchange(query []byte) ([]byte, error) {
        eps := dnsDoHEndpoints
        if len(eps) == 0 {
                return nil, fmt.Errorf("no DoH endpoints configured")
        }
        dnsMu.Lock()
        dnsTries["doh"]++
        start := (dnsTries["doh"] - 1) % len(eps)
        dnsMu.Unlock()
        var lastErr error
        for i := range eps {
                ep := eps[(start+i)%len(eps)]
                req, err := http.NewRequest(http.MethodPost, ep, bytes.NewReader(query))
                if err != nil {
                        return nil, err
                }
                req.Header.Set("Content-Type", "application/dns-message")
                req.Header.Set("Accept", "application/dns-message")
                resp, err := dohHTTPClient.Do(req)
                if err != nil {
                        lastErr = err
                        continue
                }
                body, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
                resp.Body.Close()
                if err != nil {
                        lastErr = err
                        continue
                }
                if resp.StatusCode != http.StatusOK {
                        lastErr = fmt.Errorf("%s: HTTP %d", ep, resp.StatusCode)
                        continue
                }
                if len(body) < 12 {
                        lastErr = fmt.Errorf("%s: short response (%d bytes)", ep, len(body))
                        continue
                }
                return body, nil
        }
        return nil, lastErr
}

// plainExchange is the last-resort fallback: one UDP round trip against
// the public resolver pool. Response transaction IDs are verified.
func plainExchange(query []byte) ([]byte, error) {
        if len(query) < 12 {
                return nil, fmt.Errorf("short query")
        }
        for _, pool := range [][]string{dnsPublicV4, dnsPublicV6} {
                for _, srv := range pool {
                        conn, err := net.DialTimeout("udp", srv, 2*time.Second)
                        if err != nil {
                                continue
                        }
                        conn.SetDeadline(time.Now().Add(3 * time.Second))
                        if _, err := conn.Write(query); err != nil {
                                conn.Close()
                                continue
                        }
                        resp := make([]byte, 65536)
                        n, err := conn.Read(resp)
                        conn.Close()
                        if err != nil || n < 12 {
                                continue
                        }
                        if resp[0] != query[0] || resp[1] != query[1] {
                                continue // transaction ID mismatch
                        }
                        return resp[:n], nil
                }
        }
        return nil, fmt.Errorf("plain DNS fallback failed")
}

// dohHTTPClient dials DoH endpoints without ever consulting
// net.DefaultResolver (that's this hook — using it here would recurse).
// IP-literal endpoint hosts dial directly; hostname endpoints (operator's
// choice) are resolved through a dedicated plaintext resolver.
var dohHTTPClient = &http.Client{
        Transport: &http.Transport{
                DialContext:           dohDialContext,
                TLSHandshakeTimeout:   5 * time.Second,
                ResponseHeaderTimeout: 5 * time.Second,
        },
        Timeout: 8 * time.Second,
}

func dohDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
        host, port, err := net.SplitHostPort(addr)
        if err != nil {
                return nil, err
        }
        if net.ParseIP(host) == nil {
                ips, err := dohPlainResolver.LookupIPAddr(ctx, host)
                if err != nil {
                        return nil, fmt.Errorf("bootstrap resolve %s: %w", host, err)
                }
                if len(ips) == 0 {
                        return nil, fmt.Errorf("bootstrap resolve %s: no addresses", host)
                }
                host = ips[0].IP.String()
        }
        var d net.Dialer
        return d.DialContext(ctx, network, net.JoinHostPort(host, port))
}

// dohPlainResolver only ever resolves DoH endpoint *hostnames* (the defaults
// are IP literals and never touch it). It deliberately bypasses the DoH hook.
var dohPlainResolver = &net.Resolver{
        PreferGo: true,
        Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
                pool := dnsPublicV4
                if strings.HasPrefix(address, "[") {
                        pool = dnsPublicV6
                }
                dnsMu.Lock()
                dnsTries["doh-bootstrap"]++
                idx := (dnsTries["doh-bootstrap"] - 1) % len(pool)
                dnsMu.Unlock()
                return dialAddr(ctx, network, pool[idx])
        },
}

// inspectResolvConf reports whether /etc/resolv.conf provides a usable
// (non-loopback) server, and whether every server it lists is loopback.
func inspectResolvConf() (usable, onlyLoopback bool) {
        data, err := os.ReadFile("/etc/resolv.conf")
        if err != nil || len(strings.TrimSpace(string(data))) == 0 {
                return false, false
        }
        total, nonLoop := 0, 0
        for _, line := range strings.Split(string(data), "\n") {
                f := strings.Fields(strings.TrimSpace(line))
                if len(f) < 2 || f[0] != "nameserver" {
                        continue
                }
                if ip := net.ParseIP(f[1]); ip != nil {
                        total++
                        if !ip.IsLoopback() {
                                nonLoop++
                        }
                }
        }
        return nonLoop > 0, total > 0 && nonLoop == 0
}

// parseDNSList normalizes "1.1.1.1, 9.9.9.9:5353, [2620:fe::fe]" into
// dialable "host:port" entries.
func parseDNSList(v string) []string {
        var out []string
        for _, part := range strings.Split(v, ",") {
                part = strings.TrimSpace(part)
                if part == "" {
                        continue
                }
                if net.ParseIP(part) != nil { // bare IPv4/IPv6, no port
                        out = append(out, net.JoinHostPort(part, "53"))
                        continue
                }
                if _, _, err := net.SplitHostPort(part); err == nil {
                        out = append(out, part)
                }
        }
        return out
}

// parseDoHList validates https:// DoH endpoints.
func parseDoHList(v string) []string {
        var out []string
        for _, part := range strings.Split(v, ",") {
                part = strings.TrimSpace(part)
                if part == "" {
                        continue
                }
                if !strings.HasPrefix(part, "https://") {
                        log.Printf("[DNS] ignoring non-https DoH endpoint %q", part)
                        continue
                }
                out = append(out, part)
        }
        return out
}
