package jsonrpc

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxLoggedParamsMeta = 256

// DefaultJSONRPCAPIs is the safe public surface: eth/net/web3 plus operational
// txpool + dpos (validators). debug and bridge stay off unless explicitly enabled.
var DefaultJSONRPCAPIs = []string{"eth", "net", "web3", "txpool", "dpos"}

var sensitiveHexRE = regexp.MustCompile(`(?i)(0x)?[0-9a-f]{64,}`)

// enableDPoSAdminRPC gates destructive / test-only DPoS JSON-RPC methods.
// Default is disabled. Operators may set VCITY_ENABLE_DPOS_ADMIN_RPC=1 for
// localhost-only maintenance (not for public RPC).
func enableDPoSAdminRPC() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("VCITY_ENABLE_DPOS_ADMIN_RPC")))
	return v == "1" || v == "true" || v == "yes"
}

func errDPoSAdminRPCDisabled(method string) error {
	return fmt.Errorf(
		"%s is disabled (set VCITY_ENABLE_DPOS_ADMIN_RPC=1 to enable; bind RPC to localhost only)",
		method,
	)
}

// paramsLogMeta returns length and a truncated, control-char-sanitized preview
// of raw JSON-RPC params for logging. Never log the raw body — it may contain
// private keys or other secrets. Long hex blobs (likely keys) are redacted.
func paramsLogMeta(raw []byte) (int, string) {
	n := len(raw)
	if n == 0 {
		return 0, ""
	}

	limit := maxLoggedParamsMeta
	if n < limit {
		limit = n
	}

	var b strings.Builder
	b.Grow(limit)
	for i := 0; i < limit; {
		r, size := utf8.DecodeRune(raw[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteByte('?')
			i++
			continue
		}
		i += size
		if r == '\n' || r == '\r' || r == '\t' {
			b.WriteByte(' ')
			continue
		}
		if r < 0x20 {
			b.WriteByte('?')
			continue
		}
		b.WriteRune(r)
	}
	preview := sensitiveHexRE.ReplaceAllString(b.String(), "[REDACTED]")
	if n > maxLoggedParamsMeta {
		preview += "…"
	}
	return n, preview
}

// sanitizeRPCErrorMessage strips control chars, redacts long hex, and truncates
// before returning an error string to JSON-RPC clients.
func sanitizeRPCErrorMessage(msg string) string {
	msg = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 {
			return -1
		}
		return r
	}, msg)
	msg = sensitiveHexRE.ReplaceAllString(msg, "[REDACTED]")
	const max = 512
	if len(msg) > max {
		return msg[:max] + "…"
	}
	return msg
}

func parseJSONRPCAPIList(apis []string) map[string]struct{} {
	enabled := make(map[string]struct{}, len(apis))
	for _, a := range apis {
		name := strings.ToLower(strings.TrimSpace(a))
		if name == "" {
			continue
		}
		enabled[name] = struct{}{}
	}
	return enabled
}

// ipRateLimiter is a simple per-IP token-bucket style limiter for HTTP JSON-RPC.
type ipRateLimiter struct {
	mu       sync.Mutex
	clients  map[string]*ipBucket
	rate     int           // tokens per window
	window   time.Duration
	lastPrune time.Time
}

type ipBucket struct {
	tokens    int
	windowStart time.Time
}

func newIPRateLimiter(rate int, window time.Duration) *ipRateLimiter {
	if rate <= 0 {
		rate = 200
	}
	if window <= 0 {
		window = time.Second
	}
	return &ipRateLimiter{
		clients: make(map[string]*ipBucket),
		rate:    rate,
		window:  window,
	}
}

func (l *ipRateLimiter) allow(ip string) bool {
	if ip == "" {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastPrune) > time.Minute {
		for k, b := range l.clients {
			if now.Sub(b.windowStart) > 2*l.window {
				delete(l.clients, k)
			}
		}
		l.lastPrune = now
	}

	b, ok := l.clients[ip]
	if !ok || now.Sub(b.windowStart) >= l.window {
		l.clients[ip] = &ipBucket{tokens: l.rate - 1, windowStart: now}
		return true
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
