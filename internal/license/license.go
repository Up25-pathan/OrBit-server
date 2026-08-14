package license

// LicenseInfo holds validated user metadata from the license authority.
type LicenseInfo struct {
	UserID    string `json:"userId"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatarUrl"`
	PlanTier  string `json:"planTier"` // "free", "pro", "enterprise"
}

// LicenseValidator is the interface that any license backend must implement.
type LicenseValidator interface {
	Validate(key string) (*LicenseInfo, error)
}

