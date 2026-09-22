package license

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// WebsiteValidator connects to the Website Server verification authority (GET /api/v1/licenses/verify?key=...)
type WebsiteValidator struct {
	WebsiteURL   string
	ServerSecret string
	Client       *http.Client
}

type webVerifyResponse struct {
	Valid       bool   `json:"valid"`
	Status      string `json:"status"`
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	AvatarURL   string `json:"avatarUrl"`
	Email       string `json:"email"`
	PlanTier    string  `json:"planTier"`
	Price       float64 `json:"price"`
	ExpiresAt   string  `json:"expiresAt"`
	Error       string  `json:"error"`
}

func NewWebsiteValidator(websiteURL string, serverSecret string) *WebsiteValidator {
	if websiteURL == "" {
		websiteURL = "https://orbit-sync.onrender.com"
	}
	return &WebsiteValidator{
		WebsiteURL:   strings.TrimRight(websiteURL, "/"),
		ServerSecret: serverSecret,
		Client:       &http.Client{Timeout: 10 * time.Second},
	}
}

func (w *WebsiteValidator) Validate(key string, deviceId string) (*LicenseInfo, error) {
	cleanKey := strings.TrimSpace(key)
	if cleanKey == "" {
		return nil, fmt.Errorf("license key is empty")
	}

	reqBody, _ := json.Marshal(map[string]string{
		"licenseKey": cleanKey,
		"deviceId":   deviceId,
		"hostname":   "peer-node",
		"platform":   "desktop",
	})

	// 2. Query live Website Server verification authority API via POST
	reqURL := fmt.Sprintf("%s/api/v1/licenses/verify", w.WebsiteURL)
	req, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to build license request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if w.ServerSecret != "" {
		req.Header.Set("X-Control-Server-Secret", w.ServerSecret)
	}

	resp, err := w.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("website authority connection error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return nil, fmt.Errorf("license is already bound to maximum allowed devices")
	}
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

	var expires time.Time
	if data.ExpiresAt != "" {
		expires, _ = time.Parse(time.RFC3339, data.ExpiresAt)
	}

	return &LicenseInfo{
		UserID:    data.UserID,
		Name:      name,
		Email:     data.Email,
		AvatarURL: data.AvatarURL,
		PlanTier:  data.PlanTier,
		Price:     data.Price,
		ExpiresAt: expires,
	}, nil
}
