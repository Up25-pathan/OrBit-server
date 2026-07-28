package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
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
	cfg := config.Load()

	db, err := repository.New(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()

	// Encrypted Cloud Relay: Start background sweeper to purge expired delta blobs (7-day TTL)
	db.StartDeltaSweeper()

	// Presence Heartbeat: Mark users offline after 90s of inactivity
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		for range ticker.C {
			db.HeartbeatSweep()
		}
	}()

	// Message & Activity Log Sweepers: Clean up old data periodically
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		for range ticker.C {
			db.MessageSweep()
			db.ActivityLogSweep()
		}
	}()

	// License Authentication: Connected live to Website Server verification authority
	validator := license.NewWebsiteValidator(cfg.WebsiteURL, cfg.ServerSecret, cfg.EnableMockKeys)
	log.Printf("[License Authority] Verifying licenses against Website Server at %s (Mock Keys: %v)", cfg.WebsiteURL, cfg.EnableMockKeys)

	authHandler := handlers.NewAuthHandler(db, validator, cfg.JWTSecret, cfg.JWTExpiry)
	userHandler := handlers.NewUserHandler(db)
	friendHandler := handlers.NewFriendHandler(db)
	projectHandler := handlers.NewProjectHandler(db)
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
			r.Put("/users/me/profile", userHandler.UpdateProfile)
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
	log.Printf("OrBit control server listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server: %v", err)
	}
}

var allowedOrigins = map[string]bool{
	"tauri://localhost":                true,
	"http://tauri.localhost":           true,
	"https://tauri.localhost":          true,
	"asset://localhost":                true,
	"https://orbit-sync.onrender.com":  true,
	"https://orbit-server-kae6.onrender.com": true,
	"https://orbit.dev":                true,
}

func isAllowedOrigin(origin string) bool {
	if allowedOrigins[origin] { return true }
	// Audit Fix #17: Allow any localhost/127.0.0.1 port for local dev & orbit-web
	if strings.HasPrefix(origin, "http://localhost") || strings.HasPrefix(origin, "http://127.0.0.1") || strings.HasPrefix(origin, "https://localhost") { return true }
	if strings.HasSuffix(origin, ".onrender.com") || strings.HasSuffix(origin, ".vercel.app") { return true }
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
