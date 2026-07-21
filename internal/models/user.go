package models

import "time"

type User struct {
	ID                   string    `json:"id"`
	DisplayName          string    `json:"displayName"`
	Email                string    `json:"email"`
	PlanTier             string    `json:"planTier"` // "free", "pro", "enterprise"
	LicenseKey           string    `json:"licenseKey,omitempty"`
	Bio                  string    `json:"bio"`
	Status               string    `json:"status"`
	Activity             string    `json:"activity,omitempty"`
	AvatarURL            string    `json:"avatarUrl,omitempty"`
	PublicKeyFingerprint string    `json:"publicKeyFingerprint,omitempty"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

// LicenseAuthRequest is the single authentication payload — just a key.
type LicenseAuthRequest struct {
	LicenseKey string `json:"licenseKey"`
}

type AuthResponse struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}

type UserSearchResult struct {
	ID                   string `json:"id"`
	DisplayName          string `json:"displayName"`
	Email                string `json:"email"`
	Bio                  string `json:"bio"`
	Status               string `json:"status"`
	Activity             string `json:"activity,omitempty"`
	AvatarURL            string `json:"avatarUrl,omitempty"`
	PublicKeyFingerprint string `json:"publicKeyFingerprint,omitempty"`
}

type FriendRequest struct {
	ID        string           `json:"id"`
	FromID    string           `json:"fromId"`
	ToID      string           `json:"toId"`
	Status    string           `json:"status"`
	CreatedAt time.Time        `json:"createdAt"`
	From      UserSearchResult `json:"from"`
}

type Friend struct {
	ID                   string `json:"id"`
	DisplayName          string `json:"displayName"`
	Email                string `json:"email"`
	Bio                  string `json:"bio"`
	Status               string `json:"status"`
	Activity             string `json:"activity,omitempty"`
	AvatarURL            string `json:"avatarUrl,omitempty"`
	PublicKeyFingerprint string `json:"publicKeyFingerprint,omitempty"`
	Online               bool   `json:"online"`
}
