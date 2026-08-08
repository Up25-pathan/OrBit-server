package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/orbit/control-server/internal/config"
	"github.com/orbit/control-server/internal/handlers"
	"github.com/orbit/control-server/internal/license"
	"github.com/orbit/control-server/internal/middleware"
	"github.com/orbit/control-server/internal/repository"
)

func main() {
	// Restore the database and persisted secrets from durable backup BEFORE
	// config.Load() reads (or regenerates) the secrets and db.New() loads the
	// data. This heals Render's ephemeral filesystem after a restart/redeploy:
	// friendships, project memberships and relayed deltas all come back.
	envCfg := config.LoadEnv()
	if err := repository.RestoreBackupFiles(envCfg.DatabasePath, envCfg.Backup); err != nil {
		log.Printf("[Backup] startup restore failed: %v", err)
	}

	cfg := config.Load()

	db, err := repository.New(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	db.SetBackup(envCfg.Backup)

	// Graceful shutdown: create cancellable context for background goroutines
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	// Encrypted Cloud Relay: Start background sweeper to purge expired delta blobs (7-day TTL)
	db.StartDeltaSweeperWithCtx(ctx)

	// Presence Heartbeat: Mark users offline after 90s of inactivity
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(60 * time.Second)
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

	authHandler := handlers.NewAuthHandler(db, validator, cfg.JWTSecret, cfg.JWTExpiry)
	userHandler := handlers.NewUserHandler(db)
	friendHandler := handlers.NewFriendHandler(db)
	projectHandler := handlers.NewProjectHandler(db, cfg.InviteSalt)
	signalingHandler := handlers.NewSignalingHandler(db)

	r := chi.NewRouter()
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(corsMiddleware)
	r.Use(middleware.RateLimit)

	r.Route("/api/v1", func(r chi.Router) {
		// Health check — used by keep-alive ping to prevent Render free tier spin-down
		r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		})

		// Audit Fix #42: Add updater endpoint handler returning current version info
		r.Get("/updater/latest.json", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"version":"0.1.0","notes":"No updates available currently.","pub_date":"2026-07-27T00:00:00Z","platforms":{"windows-x86_64":{"signature":"","url":""},"darwin-x86_64":{"signature":"","url":""},"darwin-aarch64":{"signature":"","url":""},"linux-x86_64":{"signature":"","url":""}}}`))
		})

		// License Key Authentication — single endpoint, no signup/signin
		r.Post("/auth/license", authHandler.AuthenticateKey)

		r.Group(func(r chi.Router) {
			r.Use(middleware.AuthMiddleware(cfg.JWTSecret))

			r.Get("/profile", userHandler.GetProfile)
			r.Put("/profile", userHandler.UpdateProfile)
			r.Put("/profile/key", userHandler.UpdatePublicKey)
			r.Put("/users/presence", userHandler.UpdatePresence)
			r.Get("/users/{id}/pulse", userHandler.GetPulse)

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
			r.Post("/projects/join", projectHandler.JoinByToken)
			r.Put("/projects/{id}/path", projectHandler.UpdateMemberPath)
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
			r.Put("/projects/{id}/tasks/{taskId}/complete", projectHandler.CompleteTask)
			r.Delete("/projects/{id}/tasks/{taskId}", projectHandler.DeleteTask)
			r.Get("/projects/{id}/leaderboard", projectHandler.Leaderboard)

			// Signaling for P2P NAT traversal
			r.Post("/projects/{id}/signal", signalingHandler.SendSignal)
			r.Get("/projects/{id}/signals", signalingHandler.GetSignals)
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
		log.Printf("[server] Received signal %v — shutting down...", sig)
		cancel()
		// Stop accepting new requests
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("[server] Shutdown error: %v", err)
		}
	}()

	log.Printf("OrBit control server listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}

	// Wait for background goroutines to finish
	wg.Wait()
	// Final persist under write lock
	if err := db.Shutdown(); err != nil {
		log.Printf("[server] Final save error: %v", err)
	}
	log.Printf("[server] Shutdown complete")
}

var allowedOrigins = map[string]bool{
	"tauri://localhost":                      true,
	"http://tauri.localhost":                 true,
	"https://tauri.localhost":                true,
	"asset://localhost":                      true,
	"https://orbit-sync.onrender.com":        true,
	"https://orbit-server-kae6.onrender.com": true,
	"https://orbit.dev":                      true,
}

func isAllowedOrigin(origin string) bool {
	if allowedOrigins[origin] {
		return true
	}
	// Audit Fix #17: Allow any localhost/127.0.0.1 port for local dev & orbit-web
	if strings.HasPrefix(origin, "http://localhost") || strings.HasPrefix(origin, "http://127.0.0.1") || strings.HasPrefix(origin, "https://localhost") {
		return true
	}
	if strings.HasSuffix(origin, ".onrender.com") || strings.HasSuffix(origin, ".vercel.app") {
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
