package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/orbit/control-server/internal/config"
	"github.com/orbit/control-server/internal/handlers"
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

	authHandler := handlers.NewAuthHandler(db, cfg.JWTSecret, cfg.JWTExpiry)
	userHandler := handlers.NewUserHandler(db)
	friendHandler := handlers.NewFriendHandler(db)
	projectHandler := handlers.NewProjectHandler(db)

	r := chi.NewRouter()
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(corsMiddleware)

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/auth/signup", authHandler.Signup)
		r.Post("/auth/signin", authHandler.Signin)

		r.Group(func(r chi.Router) {
			r.Use(middleware.AuthMiddleware(cfg.JWTSecret))

			r.Get("/profile", userHandler.GetProfile)
			r.Put("/profile", userHandler.UpdateProfile)
			r.Put("/profile/key", userHandler.UpdatePublicKey)

			r.Get("/users/search", userHandler.SearchUsers)
			r.Get("/users/{id}", func(w http.ResponseWriter, r *http.Request) {
				id := chi.URLParam(r, "id")
				user, err := db.GetUserByID(id)
				if err != nil || user == nil {
					http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
					return
				}
				writeJSON(w, http.StatusOK, user)
			})

			r.Post("/friends/request", friendHandler.SendRequest)
			r.Post("/friends/accept", friendHandler.AcceptRequest)
			r.Get("/friends/requests", friendHandler.GetRequests)
			r.Get("/friends", friendHandler.ListFriends)

			r.Post("/projects", projectHandler.Create)
			r.Get("/projects", projectHandler.List)
			r.Get("/projects/{id}/members", projectHandler.Members)
			r.Post("/projects/{id}/invite", projectHandler.Invite)
			r.Post("/projects/{id}/push", projectHandler.PushDelta)
			r.Get("/projects/{id}/pull", projectHandler.PullDeltas)
		})
	})

	addr := fmt.Sprintf(":%s", cfg.Port)
	log.Printf("OrBit control server listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
