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
type LicenseValidator interface {
	Validate(key string) (*LicenseInfo, error)
}

// MockValidator is an in-process license validator for development and testing.
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
	"ORBIT-PRO-DEVTEST": {
		UserID:   "usr_devtest_003",
		Name:     "DevTester",
		Email:    "devtester@orbit.dev",
		PlanTier: "pro",
	},
	"ORBIT-FREE-AD507E-1785071108-C05E81": {
		UserID:   "usr_jk_004",
		Name:     "jk7057583043",
		Email:    "jk7057583043@gmail.com",
		PlanTier: "free",
	},
	"ORBIT-PRO-7CC518-1785070462-7D4CE4": {
		UserID:   "usr_admin_005",
		Name:     "System Admin",
		Email:    "Admin@orbit-sync.com",
		PlanTier: "pro",
	},
	"ORBIT-ENTERPRISE-AFCBF9-1785009023-4AFA4A": {
		UserID:   "usr_admin2_006",
		Name:     "Admin",
		Email:    "admin@orbit-sync.com",
		PlanTier: "enterprise",
	},
	"ORBIT_DEV_PK_4F7384B371E012A79102154C": {
		UserID:   "usr_google_007",
		Name:     "Google Developer",
		Email:    "google-developer@orbit.dev",
		PlanTier: "pro",
	},
}

// Validate checks the provided key against the registered mock database.
func (m *MockValidator) Validate(key string) (*LicenseInfo, error) {
	cleanKey := strings.ToUpper(strings.TrimSpace(key))
	if cleanKey == "" {
		return nil, fmt.Errorf("invalid license key")
	}

	if info, ok := mockKeys[cleanKey]; ok {
		result := *info
		return &result, nil
	}

	return nil, fmt.Errorf("key not in mock map")
}
