package middleware

import (
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

func (rl *rateLimiter) allow(key string, maxTokens int, rate time.Duration) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, exists := rl.buckets[key]
	if !exists {
		b = &tokenBucket{tokens: maxTokens - 1, maxTokens: maxTokens, refillAt: time.Now().Add(rate), rate: rate}
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

func getIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.Split(xff, ",")[0]
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return ip
}

func RateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := getIP(r)
		path := r.URL.Path

		// Local loopback connections (development & desktop app polling): bypass rate limiter
		if ip == "127.0.0.1" || ip == "::1" || ip == "localhost" {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasSuffix(path, "/auth/license") {
			if !limiter.allow("auth:"+ip, 100, time.Minute) {
				http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// Write endpoints (POST/PUT/DELETE): high capacity
		method := r.Method
		if method == "POST" || method == "PUT" || method == "DELETE" {
			if !limiter.allow("write:"+ip, 300, time.Minute) {
				http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// Read endpoints: high capacity for live polling and search
		if !limiter.allow("read:"+ip, 1200, time.Minute) {
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
