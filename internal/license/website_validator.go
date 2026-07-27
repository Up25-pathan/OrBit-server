package license

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WebsiteValidator connects to the Website Server verification authority (GET /api/v1/licenses/verify?key=...)
type WebsiteValidator struct {
	WebsiteURL     string
	ServerSecret   string
	EnableMockKeys bool
	Client         *http.Client
	MockValidator  *MockValidator
}

type webVerifyResponse struct {
	Valid       bool   `json:"valid"`
	Status      string `json:"status"`
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	PlanTier    string `json:"planTier"`
	Error       string `json:"error"`
}

func NewWebsiteValidator(websiteURL string, serverSecret string, enableMockKeys bool) *WebsiteValidator {
	if websiteURL == "" {
		websiteURL = "https://orbit-sync.onrender.com"
	}
	return &WebsiteValidator{
		WebsiteURL:     strings.TrimRight(websiteURL, "/"),
		ServerSecret:   serverSecret,
		EnableMockKeys: enableMockKeys,
		Client:         &http.Client{Timeout: 10 * time.Second},
		MockValidator:  &MockValidator{},
	}
}

func (w *WebsiteValidator) Validate(key string) (*LicenseInfo, error) {
	cleanKey := strings.TrimSpace(key)
	if cleanKey == "" {
		return nil, fmt.Errorf("license key is empty")
	}

	// 1. Check mock keys fallback (works offline, in dev, and for registered system keys)
	if info, err := w.MockValidator.Validate(cleanKey); err == nil {
		return info, nil
	}

	// 2. Query live Website Server verification authority API
	reqURL := fmt.Sprintf("%s/api/v1/licenses/verify?key=%s", w.WebsiteURL, url.QueryEscape(cleanKey))
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build license request: %w", err)
	}

	if w.ServerSecret != "" {
		req.Header.Set("X-Control-Server-Secret", w.ServerSecret)
	}

	resp, err := w.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("website authority connection error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("license verification failed with status %d", resp.StatusCode)
	}

	var data webVerifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to parse verification response: %w", err)
	}

	if !data.Valid {
		errMsg := data.Error
		if errMsg == "" {
			errMsg = "invalid license key"
		}
		return nil, fmt.Errorf("%s", errMsg)
	}

	name := data.DisplayName
	if name == "" {
		name = data.Email
	}

	return &LicenseInfo{
		UserID:   data.UserID,
		Name:     name,
		Email:    data.Email,
		PlanTier: data.PlanTier,
	}, nil
}
