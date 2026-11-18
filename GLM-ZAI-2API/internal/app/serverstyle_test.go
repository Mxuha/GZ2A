package app

import (
	"context"
	"net"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

// TestServerStyleDoH reproduces the server wiring exactly: DefaultResolver
// replaced (as init() does), DoH forced, then (1) a direct lookup and
// (2) the same zaiHTTP GET that scrapeConfig performs.
func TestServerStyleDoH(t *testing.T) {
	dnsUseDoH = true
	dnsDoHEndpoints = parseDoHList(dohDefaultEndpoints)
	defer func() { dnsUseDoH = false }()
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: dnsDial}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	// 1. direct lookup through the replaced DefaultResolver
	start := time.Now()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, "chat.z.ai")
	t.Logf("direct DefaultResolver lookup: err=%v ips=%v took=%s", err, ips, time.Since(start))
	if err != nil {
		t.Errorf("direct lookup failed: %v", err)
	}

	// 2. the real scrape path: bogdanfinn tls-client
	client, err := zaiHTTP(nil)
	if err != nil {
		t.Fatalf("zaiHTTP: %v", err)
	}
	req, _ := fhttp.NewRequestWithContext(ctx, "GET", baseURL, nil)
	start = time.Now()
	resp, err := client.Do(req)
	if err != nil {
		t.Errorf("bogdanfinn GET %s failed after %s: %v", baseURL, time.Since(start), err)
		return
	}
	defer resp.Body.Close()
	t.Logf("bogdanfinn GET %s: status=%d took=%s", baseURL, resp.StatusCode, time.Since(start))
	if resp.StatusCode != 200 {
		t.Errorf("unexpected status %d", resp.StatusCode)
	}
}
