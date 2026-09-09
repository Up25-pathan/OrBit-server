package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type tokenBucket struct {
	tokens    int
	maxTokens int
	refillAt  time.Time
	rate      time.Duration
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

var limiter = &rateLimiter{
	buckets: make(map[string]*tokenBucket),
}

// StartRateLimiterCleanup periodically removes stale rate limiter entries
// to prevent unbounded memory growth from unique IP addresses.
func StartRateLimiterCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				count := limiter.purgeStale()
				if count > 0 {
					slog.Debug("[ratelimit] Purged stale bucket(s)", "count", count)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// purgeStale removes entries that haven't been used in over 1 hour.
func (rl *rateLimiter) purgeStale() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := time.Now().Add(-1 * time.Hour)
	var stale []string
	for key, b := range rl.buckets {
		if b.refillAt.Before(cutoff) {
			stale = append(stale, key)
		}
	}
	for _, key := range stale {
		delete(rl.buckets, key)
	}
	return len(stale)
}

func (rl *rateLimiter) allow(key string, maxTokens int, rate time.Duration) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, exists := rl.buckets[key]
	if !exists {
		b = &tokenBucket{tokens: maxTokens - 1, maxTokens: maxTokens, refillAt: time.Now(), rate: rate}
		rl.buckets[key] = b
		return true
	}

	now := time.Now()
	if now.After(b.refillAt) {
		elapsed := now.Sub(b.refillAt)
		refill := int(elapsed / b.rate)
		if refill > 0 {
			b.tokens = min(b.tokens+refill, b.maxTokens)
			b.refillAt = b.refillAt.Add(time.Duration(refill) * b.rate)
		}
	}

	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

// getIP derives the client key from the socket peer address only. X-Forwarded-For
// / X-Real-IP are client-controlled (or reverse-proxy controlled) and must not be
// trusted: a caller could otherwise spoof a new header value per request to reset
// their bucket (or to hammer someone else's bucket). If the server ever runs
// behind a trusted proxy, the proxy's own IP becomes the shared key — acceptable
// and bounded by the generous per-key capacities below.
func getIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	return strings.Trim(strings.TrimSpace(host), "[]")
}

func RateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := getIP(r)
		path := r.URL.Path

		// Authenticated requests (active app user with JWT token): apply high-capacity limit rather than complete bypass
		if r.Header.Get("Authorization") != "" {
			if !limiter.allow("auth_user:"+ip, 100000, 100*time.Millisecond) {
				slog.Warn("Rate limit exceeded", "category", "auth_user", "ip", ip, "path", path, "method", r.Method)
				http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// Local loopback connections (development & desktop app polling): bypass rate limiter
		if ip == "127.0.0.1" || ip == "::1" || ip == "localhost" {
			next.ServeHTTP(w, r)
			return
		}

		if strings.HasSuffix(path, "/auth/license") {
			if !limiter.allow("auth:"+ip, 5000, 100*time.Millisecond) {
				slog.Warn("Rate limit exceeded", "category", "auth", "ip", ip, "path", path, "method", r.Method)
				http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// Write endpoints (POST/PUT/DELETE): high capacity
		method := r.Method
		if method == "POST" || method == "PUT" || method == "DELETE" {
			if !limiter.allow("write:"+ip, 10000, 100*time.Millisecond) {
				slog.Warn("Rate limit exceeded", "category", "write", "ip", ip, "path", path, "method", r.Method)
				http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// Read endpoints: high capacity for live polling and search
		if !limiter.allow("read:"+ip, 50000, 100*time.Millisecond) {
			slog.Warn("Rate limit exceeded", "category", "read", "ip", ip, "path", path, "method", r.Method)
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
