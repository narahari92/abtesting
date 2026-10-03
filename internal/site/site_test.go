package site

import (
	"strings"
	"testing"
	"time"
)

func TestAPIKeyAndHash(t *testing.T) {
	k1, err := NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := NewAPIKey()
	if !strings.HasPrefix(k1, APIKeyPrefix) || len(k1) != len(APIKeyPrefix)+43 || k1 == k2 {
		t.Fatalf("bad keys %q %q", k1, k2)
	}
	if HashKey(k1) == HashKey(k2) || len(HashKey(k1)) != 64 || HashKey(k1) != HashKey(k1) {
		t.Fatal("hash must be stable, distinct and hex sha256")
	}
	if strings.Contains(HashKey(k1), k1[8:20]) {
		t.Fatal("hash must not contain the key")
	}
	if !KeysEqual(k1, k1) || KeysEqual(k1, k2) || KeysEqual(k1, k1[:10]) {
		t.Fatal("KeysEqual")
	}
}

func TestNormalizeOrigin(t *testing.T) {
	ok := map[string]string{
		"https://www.acme.com":       "https://www.acme.com",
		"HTTPS://WWW.Acme.com/":      "https://www.acme.com",
		"https://www.acme.com:443":   "https://www.acme.com",
		"http://localhost:8080":      "http://localhost:8080",
		"http://localhost:80":        "http://localhost",
		"https://shop.acme.com:8443": "https://shop.acme.com:8443",
		"  https://a.com  ":          "https://a.com",
	}
	for in, want := range ok {
		got, valid := NormalizeOrigin(in)
		if !valid || got != want {
			t.Errorf("NormalizeOrigin(%q) = %q,%v want %q", in, got, valid, want)
		}
	}
	for _, in := range []string{"", "null", "acme.com", "ftp://a.com", "https://a.com/path", "https://a.com?x=1", "https://user@a.com", "https://", "javascript:alert(1)"} {
		if _, valid := NormalizeOrigin(in); valid {
			t.Errorf("NormalizeOrigin(%q) should be invalid", in)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	allowed := []string{"https://www.acme.com", "http://localhost:8080", "not a url"}
	yes := []string{"https://www.acme.com", "https://WWW.acme.com:443", "http://localhost:8080"}
	no := []string{"https://acme.com", "http://www.acme.com", "https://www.acme.com:8443", "https://evil.com", "", "null", "https://www.acme.com.evil.com", "not a url"}
	for _, o := range yes {
		if !OriginAllowed(o, allowed) {
			t.Errorf("%q should be allowed", o)
		}
	}
	for _, o := range no {
		if OriginAllowed(o, allowed) {
			t.Errorf("%q should be rejected", o)
		}
	}
	if OriginAllowed("https://www.acme.com", nil) {
		t.Error("empty allow-list permits nothing")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter()
	l.now = func() time.Time { return now }

	// burst of 3 at 2 rps
	for i := 0; i < 3; i++ {
		if !l.Allow("a", 2, 3) {
			t.Fatalf("request %d within burst should pass", i)
		}
	}
	if l.Allow("a", 2, 3) {
		t.Fatal("4th request must be limited")
	}
	if !l.Allow("b", 2, 3) {
		t.Fatal("other site has its own bucket")
	}
	now = now.Add(500 * time.Millisecond) // +1 token
	if !l.Allow("a", 2, 3) || l.Allow("a", 2, 3) {
		t.Fatal("refill at 2 rps should allow exactly one")
	}
	now = now.Add(time.Hour)
	if !l.Allow("a", 2, 3) {
		t.Fatal("refill caps at burst but must allow")
	}
	if !l.Allow("c", 0, 0) {
		t.Fatal("rate 0 means unlimited")
	}
	// burst defaults to rate
	for i := 0; i < 5; i++ {
		l.Allow("d", 5, 0)
	}
	if l.Allow("d", 5, 0) {
		t.Fatal("burst should default to rate")
	}
	// pruning removes idle buckets
	now = now.Add(3 * time.Minute)
	l.Allow("e", 1, 1)
	l.mu.Lock()
	_, hasA := l.buckets["a"]
	l.mu.Unlock()
	if hasA {
		t.Fatal("idle bucket should have been pruned")
	}
}
