package models

import "time"

type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Language  string    `json:"language"`
	Domain    string    `json:"domain"`
	OwnerID   string    `json:"ownerId"`
	CreatedAt time.Time `json:"createdAt"`
}

type Task struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"projectId"`
	Title       string    `json:"title"`
	AssigneeID  string    `json:"assigneeId"`
	CreatorID   string    `json:"creatorId"`
	Status      string    `json:"status"` // "open" | "completed"
	CreatedAt   time.Time `json:"createdAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Assignee    UserSearchResult `json:"assignee"`
}

type ProjectMember struct {
	ProjectID string `json:"projectId"`
	UserID    string `json:"userId"`
	Role      string `json:"role"`
	Path      string `json:"path"`
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
	Name     string `json:"name"`
	Language string `json:"language"`
	Domain   string `json:"domain"`
}

type InviteMemberRequest struct {
	UserID string `json:"userId"`
}

type PushDeltaRequest struct {
	Data string `json:"data"`
}

type UpdateProjectRequest struct {
	Name string `json:"name"`
}

type CreateTaskRequest struct {
	Title      string `json:"title"`
	AssigneeID string `json:"assigneeId"`
}

type ActivityLog struct {
	ID        string    `json:"id"`
	UserID    string    `json:"userId"`
	ProjectID string    `json:"projectId"`
	Action    string    `json:"action"` // "task_completed", "delta_pushed"
	CreatedAt time.Time `json:"createdAt"`
}

type LeaderboardEntry struct {
	User           UserSearchResult `json:"user"`
	TasksCompleted int              `json:"tasksCompleted"`
	DeltasPushed   int              `json:"deltasPushed"`
	TotalScore     int              `json:"totalScore"`
}

type PulseEntry struct {
	Date           string `json:"date"` // YYYY-MM-DD
	TasksCompleted int    `json:"tasksCompleted"`
	DeltasPushed   int    `json:"deltasPushed"`
}

type ChatMessage struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	AuthorID  string    `json:"authorId"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
	Author    UserSearchResult `json:"author"`
}

type SendMessageRequest struct {
	Text string `json:"text"`
}
