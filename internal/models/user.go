package models

import "time"

type User struct {
	ID               string    `json:"id"`
	DisplayName      string    `json:"displayName"`
	Email            string    `json:"email"`
	PasswordHash     string    `json:"-"`
	Bio              string    `json:"bio"`
	Status           string    `json:"status"`
	AvatarURL        string    `json:"avatarUrl,omitempty"`
	PublicKeyFingerprint string `json:"publicKeyFingerprint,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
}

type SignupRequest struct {
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Password    string `json:"password"`
}

type SigninRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type AuthResponse struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}

type UserSearchResult struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Bio         string `json:"bio"`
	Status      string `json:"status"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
}

type FriendRequest struct {
	ID        string    `json:"id"`
	FromID    string    `json:"fromId"`
	ToID      string    `json:"toId"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	From      UserSearchResult `json:"from"`
}

type Friend struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Bio         string `json:"bio"`
	Status      string `json:"status"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
	Online      bool   `json:"online"`
}
