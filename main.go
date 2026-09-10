package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/orbit/control-server/internal/config"
	"github.com/orbit/control-server/internal/handlers"
	"github.com/orbit/control-server/internal/license"
	"github.com/orbit/control-server/internal/logger"
	"github.com/orbit/control-server/internal/middleware"
	"github.com/orbit/control-server/internal/repository"
	"github.com/orbit/control-server/internal/websocket"
)

func main() {
	cfg := config.Load()
	logger.Init(cfg.WebsiteURL, cfg.ServerSecret)

	// DB loads from Postgres when DATABASE_URL is set (durable across Render
	// restarts/redeploys), otherwise from the local JSON file.
	db, err := repository.New(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}

	// Secrets are persisted durably (Postgres kv table in production, files in
	// local dev) so sessions and invite tokens survive restarts. The env vars
	// ORBIT_JWT_SECRET / ORBIT_INVITE_SALT still take precedence when set.
	jwtSecret := db.GetOrCreateSecret("jwt", 32)
	inviteSalt := db.GetOrCreateSecret("invite", 16)

	// Graceful shutdown: create cancellable context for background goroutines
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	// Encrypted Cloud Relay: Start background sweeper to purge expired delta blobs (3-day TTL)
	db.StartDeltaSweeperWithCtx(ctx)

	// Orphaned Projects: Start background sweeper to purge empty/failed project creations
	db.StartOrphanSweeperWithCtx(ctx)

	// Presence Heartbeat: Mark users offline after inactivity
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				db.HeartbeatSweep()
			case <-ctx.Done():
				return
			}
		}
	}()

	// Message & Activity Log Sweepers: Clean up old data periodically
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				db.MessageSweep()
				db.ActivityLogSweep()
			case <-ctx.Done():
				return
			}
		}
	}()

	// Signal Sweeper: Purge stale WebRTC signaling messages every 30 minutes
	wg.Add(1)
	go func() {
		defer wg.Done()
		const signalTTL = 30 * time.Minute
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if n := db.SweepExpiredSignals(signalTTL); n > 0 {
					log.Printf("[signal-gc] Purged %d expired signal(s)", n)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Initialize License Validator
	validator := license.NewWebsiteValidator(cfg.WebsiteURL, cfg.ServerSecret)
	log.Printf("[License Authority] Verifying licenses against Website Server at %s", cfg.WebsiteURL)

	authHandler := handlers.NewAuthHandler(db, validator, jwtSecret, cfg.JWTExpiry)
	userHandler := handlers.NewUserHandler(db, validator)
	friendHandler := handlers.NewFriendHandler(db)
	projectHandler := handlers.NewProjectHandler(db, inviteSalt)
	wsHub := websocket.NewHub(db)
	signalingHandler := handlers.NewSignalingHandler(db, wsHub)
	telemetryHandler := handlers.NewTelemetryHandler(db)

	r := chi.NewRouter()
	r.Use(corsMiddleware)
	r.Use(middleware.RateLimit)
	r.Use(logger.HTTPLogger)
	r.Use(logger.Recoverer)

	// Ultra-lightweight health endpoints for keep-alive pings (UptimeRobot / Cron)
	healthHandler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}
	r.Get("/", healthHandler)
	r.Head("/", healthHandler)
	r.Get("/health", healthHandler)
	r.Head("/health", healthHandler)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", healthHandler)
		r.Head("/health", healthHandler)
		// Audit Fix #42: Add updater endpoint handler returning current version info
		r.Get("/system/status", telemetryHandler.GetStatus)
		r.Post("/system/sweep", telemetryHandler.TriggerSweep)
		r.Get("/updater/latest.json", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"version":"0.1.0","notes":"No updates available currently.","pub_date":"2026-07-27T00:00:00Z","platforms":{"windows-x86_64":{"signature":"","url":""},"darwin-x86_64":{"signature":"","url":""},"darwin-aarch64":{"signature":"","url":""},"linux-x86_64":{"signature":"","url":""}}}`))
		})

		// TURN credentials for NAT traversal (short-lived, signed)
		r.Get("/turn-credentials", func(w http.ResponseWriter, r *http.Request) {
			userID := middleware.GetUserID(r)
			if userID == "" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			// In production, generate time-limited TURN credentials using a TURN secret
			// For now, return static configuration - replace with actual TURN server
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"username":"orbit-user","credential":"turn-secret-change-in-production","urls":["turn:turn.orbit-server-xbr5.onrender.com:3478?transport=udp","turn:turn.orbit-server-xbr5.onrender.com:3478?transport=tcp"],"ttl":86400}`))
		})

		// License Key Authentication — single endpoint, no signup/signin
		r.Post("/auth/license", authHandler.AuthenticateKey)

		r.Group(func(r chi.Router) {
			r.Use(middleware.AuthMiddleware(jwtSecret, func(userID string) bool {
				user, err := db.GetUserByID(userID)
				return err == nil && user != nil
			}))

			r.Get("/profile", userHandler.GetProfile)
			r.Post("/profile/sync", userHandler.SyncWebProfile)
			r.Put("/profile", userHandler.UpdateProfile)
			r.Put("/profile/key", userHandler.UpdatePublicKey)
			r.Put("/users/presence", userHandler.UpdatePresence)
			r.Get("/users/{id}/pulse", userHandler.GetPulse)
			r.Get("/users/{id}/messages", userHandler.GetDirectMessages)
			r.Post("/users/{id}/messages", userHandler.SendDirectMessage)

			r.Get("/users/search", userHandler.SearchUsers)
			r.Get("/users/{id}", func(w http.ResponseWriter, r *http.Request) {
				id := chi.URLParam(r, "id")
				user, err := db.GetUserByID(id)
				if err != nil || user == nil {
					http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(user)
			})

			r.Post("/friends/request", friendHandler.SendRequest)
			r.Post("/friends/accept", friendHandler.AcceptRequest)
			r.Post("/friends/decline", friendHandler.DeclineRequest)
			r.Get("/friends/requests", friendHandler.GetRequests)
			r.Get("/friends", friendHandler.ListFriends)

			r.Post("/projects", projectHandler.Create)
			r.Get("/projects", projectHandler.List)
			r.Get("/projects/{id}/members", projectHandler.Members)
			r.Put("/projects/{id}", projectHandler.Update)
			r.Delete("/projects/{id}", projectHandler.DeleteProject)
			r.Post("/projects/{id}/invite", projectHandler.Invite)
			r.Get("/projects/{id}/token", projectHandler.GenerateToken)
			r.Put("/projects/{id}/token", projectHandler.UpdateToken)
			r.Post("/projects/join", projectHandler.JoinByToken)
			r.Post("/projects/{id}/join", projectHandler.JoinProject)
			r.Put("/projects/{id}/path", projectHandler.UpdateMemberPath)
			r.Delete("/projects/{id}/members/{userId}", projectHandler.RemoveMember)
			r.Post("/projects/{id}/leave", projectHandler.LeaveProject)
			r.Post("/projects/{id}/messages", projectHandler.SendMessage)
			r.Get("/projects/{id}/messages", projectHandler.ListMessages)
			// Encrypted Cloud Relay: The Go server acts as a temporary "Dead Drop" vault.
			// POST stores the E2EE encrypted blob; GET delivers missed packages to offline peers.
			r.Post("/projects/{id}/push", projectHandler.PushDelta)
			r.Get("/projects/{id}/pull", projectHandler.PullDeltas)
			// Clients confirm a delta was applied locally; the relay clears the
			// blob once every member has acked (no more stale re-delivery).
			r.Post("/projects/{id}/ack", projectHandler.AckDelta)

			r.Post("/projects/{id}/tasks", projectHandler.CreateTask)
			r.Get("/projects/{id}/tasks", projectHandler.ListTasks)
			r.Patch("/projects/{id}/tasks/{taskId}", projectHandler.UpdateTask)
			r.Put("/projects/{id}/tasks/{taskId}/complete", projectHandler.CompleteTask)
			r.Delete("/projects/{id}/tasks/{taskId}", projectHandler.DeleteTask)
			r.Get("/projects/{id}/leaderboard", projectHandler.Leaderboard)

			// Signaling for P2P NAT traversal
			r.Post("/projects/{id}/signal", signalingHandler.SendSignal)
			r.Get("/projects/{id}/signals", signalingHandler.GetSignals)
			r.Get("/projects/{id}/ws", wsHub.HandleWebSocket)
		})
	})

	addr := fmt.Sprintf(":%s", cfg.Port)
	srv := &http.Server{Addr: addr, Handler: r}

	// Start rate limiter cleanup goroutine
	middleware.StartRateLimiterCleanup(ctx)

	// Graceful shutdown on SIGINT/SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("[server] Received signal — shutting down...", "signal", sig.String())
		cancel()
		// Stop accepting new requests
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("[server] Shutdown error", "err", err.Error())
		}
	}()

	slog.Info("OrBit control server listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("Server fatal error", "err", err.Error())
		os.Exit(1)
	}

	// Wait for background goroutines to finish
	wg.Wait()
	// Final persist under write lock
	if err := db.Shutdown(); err != nil {
		slog.Error("[server] Final save error", "err", err.Error())
	}
	slog.Info("[server] Shutdown complete")
}

var allowedOrigins = map[string]bool{
	"tauri://localhost":                      true,
	"http://tauri.localhost":                 true,
	"https://tauri.localhost":                true,
	"asset://localhost":                      true,
	"https://orbit-server-xbr5.onrender.com":        true,
	"https://orbit-server-kae6.onrender.com": true,
	"https://orbit.dev":                      true,
}

func isAllowedOrigin(origin string) bool {
	if allowedOrigins[origin] {
		return true
	}
	// Loopback origins only — allow any port on localhost/127.0.0.1 (dev +
	// orbit-web) but never a similarly-named host like localhost.evil.com.
	// A ":" cannot appear in a hostname, so the "http://localhost:" prefix is
	// safe and bounded to the loopback host itself.
	if origin == "http://localhost" || origin == "https://localhost" ||
		strings.HasPrefix(origin, "http://localhost:") || strings.HasPrefix(origin, "https://localhost:") {
		return true
	}
	if origin == "http://127.0.0.1" || origin == "https://127.0.0.1" ||
		strings.HasPrefix(origin, "http://127.0.0.1:") || strings.HasPrefix(origin, "https://127.0.0.1:") {
		return true
	}
	return false
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && isAllowedOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		} else if origin != "" {
			// Origin present but not allowed — reject
			http.Error(w, `{"error":"origin not allowed"}`, http.StatusForbidden)
			return
		}
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
