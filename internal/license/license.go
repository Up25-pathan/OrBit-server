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

// Validate checks the provided key against the mock database or parses valid ORBIT keys dynamically.
func (m *MockValidator) Validate(key string) (*LicenseInfo, error) {
	cleanKey := strings.ToUpper(strings.TrimSpace(key))
	if cleanKey == "" {
		return nil, fmt.Errorf("invalid license key")
	}

	if info, ok := mockKeys[cleanKey]; ok {
		result := *info
		return &result, nil
	}

	// Dynamic fallback for any valid ORBIT license key structure in dev/test mode
	if strings.HasPrefix(cleanKey, "ORBIT-") || strings.HasPrefix(cleanKey, "ORBIT_") {
		tier := "free"
		if strings.Contains(cleanKey, "PRO") {
			tier = "pro"
		} else if strings.Contains(cleanKey, "ENTERPRISE") {
			tier = "enterprise"
		}

		keyHash := "dev"
		if len(cleanKey) >= 6 {
			keyHash = cleanKey[len(cleanKey)-6:]
		}

		return &LicenseInfo{
			UserID:   fmt.Sprintf("usr_%s_%s", tier, strings.ToLower(keyHash)),
			Name:     fmt.Sprintf("OrBit %s User", strings.Title(tier)),
			Email:    fmt.Sprintf("user_%s@orbit.dev", strings.ToLower(keyHash)),
			PlanTier: tier,
		}, nil
	}

	return nil, fmt.Errorf("invalid license key")
}
