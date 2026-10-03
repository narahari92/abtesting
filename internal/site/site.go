// Package site holds tenant credential and abuse-control primitives:
// API key generation and hashing, origin allow-list matching, and a
// per-site token-bucket rate limiter. None of it touches storage.
package site

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"math/big"
	"net/url"
	"strings"
)

// APIKeyPrefix makes keys recognisable in logs and secret scanners.
const APIKeyPrefix = "sk_live_"

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// NewAPIKey returns a fresh private site API key: the prefix plus 43
// base62 characters (256 bits of entropy).
func NewAPIKey() (string, error) {
	var sb strings.Builder
	sb.WriteString(APIKeyPrefix)
	max := big.NewInt(int64(len(base62)))
	for i := 0; i < 43; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		sb.WriteByte(base62[n.Int64()])
	}
	return sb.String(), nil
}

// HashKey is the stored form of an API key. SHA-256 is appropriate here
// because the key is a 256-bit random secret, not a human password, so
// there is nothing for a slow hash to protect against.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// KeysEqual compares two keys in constant time.
func KeysEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// NormalizeOrigin canonicalises an origin for storage and comparison:
// lowercase scheme and host, default port dropped, no path, query,
// fragment or userinfo. It returns false for anything that is not a plain
// http(s) origin.
func NormalizeOrigin(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", false
	}
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		return scheme + "://" + host + ":" + port, true
	}
	return scheme + "://" + host, true
}

// OriginAllowed reports whether a request Origin header matches one of
// the site's allowed origins exactly (after normalisation). No wildcards:
// a customer with many subdomains lists them.
func OriginAllowed(origin string, allowed []string) bool {
	got, ok := NormalizeOrigin(origin)
	if !ok {
		return false
	}
	for _, a := range allowed {
		if want, ok := NormalizeOrigin(a); ok && want == got {
			return true
		}
	}
	return false
}
