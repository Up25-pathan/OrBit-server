package models

import "time"

type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	OwnerID   string    `json:"ownerId"`
	CreatedAt time.Time `json:"createdAt"`
}

type ProjectMember struct {
	ProjectID string `json:"projectId"`
	UserID    string `json:"userId"`
	Role      string `json:"role"`
	User      UserSearchResult `json:"user"`
}

type ProjectDelta struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	AuthorID  string    `json:"authorId"`
	Data      string    `json:"data"`
	CreatedAt time.Time `json:"createdAt"`
	Author    UserSearchResult `json:"author"`
}

type CreateProjectRequest struct {
	Name string `json:"name"`
}

type InviteMemberRequest struct {
	UserID string `json:"userId"`
}

type PushDeltaRequest struct {
	Data string `json:"data"`
}
