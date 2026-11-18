package app

import (
	"context"
	"net"
	"testing"
	"time"
)

// TestDoHExchangeRaw exercises the RFC 8484 POST path with a hand-built
// wire-format query (example.com A).
func TestDoHExchangeRaw(t *testing.T) {
	eps := parseDoHList(dohDefaultEndpoints)
	if len(eps) == 0 {
		t.Fatal("no default endpoints parsed")
	}
	dnsDoHEndpoints = eps
	// hand-built query: id 0x1234, RD, QD=1, example.com A IN
	q := []byte{
		0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 0x03, 'c', 'o', 'm', 0x00,
		0x00, 0x01, 0x00, 0x01,
	}
	resp, err := dohExchange(q)
	if err != nil {
		t.Fatalf("dohExchange: %v", err)
	}
	if len(resp) < 12 {
		t.Fatalf("short response: %d bytes", len(resp))
	}
	if resp[0] != 0x12 || resp[1] != 0x34 {
		t.Fatalf("transaction ID mismatch: % x", resp[:2])
	}
	t.Logf("dohExchange OK: %d bytes, RCODE=%d ANCOUNT=%d", len(resp), resp[3]&0x0F, resp[7])
}

// TestDoHShimThroughResolver runs a full LookupIPAddr through the same
// Dial hook the server installs, with DoH forced on.
func TestDoHShimThroughResolver(t *testing.T) {
	dnsUseDoH = true
	dnsDoHEndpoints = parseDoHList(dohDefaultEndpoints)
	defer func() { dnsUseDoH = false }()
	r := &net.Resolver{PreferGo: true, Dial: dnsDial}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	ips, err := r.LookupIPAddr(ctx, "chat.z.ai")
	if err != nil {
		t.Fatalf("LookupIPAddr via DoH shim: %v (took %s)", err, time.Since(start))
	}
	if len(ips) == 0 {
		t.Fatal("no addresses returned")
	}
	t.Logf("resolved chat.z.ai via DoH shim in %s: %v", time.Since(start), ips)
}
