package app

import (
        "context"
        "testing"
        "time"
)

// TestDoHConnManual dials through dnsDial (DoH mode) and performs a manual
// query round trip on the returned conn, bypassing the real resolver —
// isolates pipe mechanics from resolver integration.
func TestDoHConnManual(t *testing.T) {
        dnsUseDoH = true
        dnsDoHEndpoints = parseDoHList(dohDefaultEndpoints)
        defer func() { dnsUseDoH = false }()

        for _, network := range []string{"udp", "tcp"} {
                t.Run(network, func(t *testing.T) {
                        conn, err := dnsDial(context.Background(), network, "192.0.2.1:53")
                        if err != nil {
                                t.Fatalf("dnsDial: %v", err)
                        }
                        defer conn.Close()
                        conn.SetDeadline(time.Now().Add(10 * time.Second))
                        q := []byte{
                                0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
                                0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 0x03, 'c', 'o', 'm', 0x00,
                                0x00, 0x01, 0x00, 0x01,
                        }
                        if network == "tcp" {
                                q = append([]byte{0x00, byte(len(q))}, q...)
                        }
                        if _, err := conn.Write(q); err != nil {
                                t.Fatalf("write: %v", err)
                        }
                        buf := make([]byte, 65536)
                        n, err := conn.Read(buf)
                        if err != nil {
                                t.Fatalf("read: %v", err)
                        }
                        r := buf[:n]
                        if network == "tcp" {
                                if n < 2 {
                                        t.Fatalf("short tcp frame")
                                }
                                r = r[2:]
                        }
                        if len(r) < 12 || r[0] != 0x12 || r[1] != 0x34 {
                                t.Fatalf("bad response: % x", r[:min(12, len(r))])
                        }
                        t.Logf("%s round trip OK: %d bytes, RCODE=%d ANCOUNT=%d", network, n, r[3]&0x0F, r[7])
                })
        }
}

func min(a, b int) int {
        if a < b {
                return a
        }
        return b
}
