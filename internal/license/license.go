package license

import (
	"fmt"
	"strings"
)

// LicenseInfo holds validated user metadata from the license authority.
type LicenseInfo struct {
	UserID   string `json:"userId"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	PlanTier string `json:"planTier"` // "free", "pro", "enterprise"
}

// LicenseValidator is the interface that any license backend must implement.
// Today: MockValidator. Later: swap to OfficialWebsiteValidator.
// The React client and Go handlers never change — only this implementation does.
type LicenseValidator interface {
	Validate(key string) (*LicenseInfo, error)
}

// MockValidator is a temporary in-process license validator for development.
// It returns hardcoded metadata for known test keys.
type MockValidator struct{}

// mockKeys maps license keys to their user metadata.
var mockKeys = map[string]*LicenseInfo{
	"ORBIT-PRO-JAMAL": {
		UserID:   "usr_jamal_001",
		Name:     "Jamal",
		Email:    "jamal@orbit.dev",
		PlanTier: "pro",
	},
	"ORBIT-PRO-TESTER": {
		UserID:   "usr_tester_002",
		Name:     "Tester",
		Email:    "tester@orbit.dev",
		PlanTier: "pro",
	},
}

// Validate checks the provided key against the mock database.
func (m *MockValidator) Validate(key string) (*LicenseInfo, error) {
	cleanKey := strings.ToUpper(strings.TrimSpace(key))
	info, ok := mockKeys[cleanKey]
	if !ok {
		return nil, fmt.Errorf("invalid license key")
	}
	// Return a copy to prevent mutation
	result := *info
	return &result, nil
}
