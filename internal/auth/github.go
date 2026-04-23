package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultClientID     = "Ov23liUZT4jkj7w8tLAd"
	DefaultOAuthBaseURL = "https://github.com"
	DefaultAPIBaseURL   = "https://api.github.com"
	DefaultScope        = "read:org repo"
)

type Config struct {
	ClientID     string
	Scope        string
	OAuthBaseURL string
	APIBaseURL   string
	HTTPClient   *http.Client
}

type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type AccessTokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorURI         string `json:"error_uri"`
}

type Viewer struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

type StoredToken struct {
	Version      string    `json:"version"`
	Provider     string    `json:"provider"`
	ClientID     string    `json:"client_id"`
	OAuthBaseURL string    `json:"oauth_base_url"`
	APIBaseURL   string    `json:"api_base_url"`
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	Scope        string    `json:"scope"`
	UserLogin    string    `json:"user_login"`
	UserID       int64     `json:"user_id"`
	SavedAt      time.Time `json:"saved_at"`
}

func DefaultConfig() Config {
	return Config{
		ClientID:     strings.TrimSpace(firstNonEmptyEnv("PRTAGS_GITHUB_OAUTH_CLIENT_ID", "GITHUB_OAUTH_CLIENT_ID", "GITHUB_CLIENT_ID")),
		Scope:        strings.TrimSpace(firstNonEmptyEnv("PRTAGS_GITHUB_OAUTH_SCOPE", "GITHUB_OAUTH_SCOPE")),
		OAuthBaseURL: strings.TrimSpace(firstNonEmptyEnv("PRTAGS_GITHUB_OAUTH_BASE_URL", "GITHUB_OAUTH_BASE_URL")),
		APIBaseURL:   strings.TrimSpace(firstNonEmptyEnv("PRTAGS_GITHUB_API_URL", "GITHUB_API_URL")),
		HTTPClient:   &http.Client{Timeout: 30 * time.Second},
	}.withDefaults()
}

func (c Config) withDefaults() Config {
	if strings.TrimSpace(c.ClientID) == "" {
		c.ClientID = DefaultClientID
	}
	if strings.TrimSpace(c.Scope) == "" {
		c.Scope = DefaultScope
	}
	if strings.TrimSpace(c.OAuthBaseURL) == "" {
		c.OAuthBaseURL = DefaultOAuthBaseURL
	}
	if strings.TrimSpace(c.APIBaseURL) == "" {
		c.APIBaseURL = DefaultAPIBaseURL
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return c
}

func (c Config) StartDeviceFlow(ctx context.Context) (DeviceCodeResponse, error) {
	c = c.withDefaults()
	form := url.Values{}
	form.Set("client_id", c.ClientID)
	form.Set("scope", c.Scope)

	var response DeviceCodeResponse
	if err := c.postFormJSON(ctx, c.OAuthBaseURL+"/login/device/code", form, &response); err != nil {
		return DeviceCodeResponse{}, err
	}
	if strings.TrimSpace(response.DeviceCode) == "" || strings.TrimSpace(response.UserCode) == "" || strings.TrimSpace(response.VerificationURI) == "" {
		return DeviceCodeResponse{}, fmt.Errorf("device flow response missing required fields")
	}
	return response, nil
}

func (c Config) PollAccessToken(ctx context.Context, deviceCode string, interval time.Duration, expiresIn time.Duration) (AccessTokenResponse, error) {
	c = c.withDefaults()
	deadline := deviceFlowDeadline(expiresIn)
	currentInterval := pollInterval(interval)

	for {
		if err := deviceFlowPreflight(ctx, deadline); err != nil {
			return AccessTokenResponse{}, err
		}

		response, err := c.pollAccessTokenOnce(ctx, deviceCode)
		if err != nil {
			return AccessTokenResponse{}, err
		}
		done, nextInterval, err := nextPollAction(response, currentInterval)
		if err != nil {
			return AccessTokenResponse{}, err
		}
		if done {
			return response, nil
		}
		currentInterval = nextInterval
		if err := waitForNextPoll(ctx, currentInterval); err != nil {
			return AccessTokenResponse{}, err
		}
	}
}

func (c Config) GetViewer(ctx context.Context, accessToken string) (Viewer, error) {
	c = c.withDefaults()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.APIBaseURL, "/")+"/user", nil)
	if err != nil {
		return Viewer{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return Viewer{}, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Viewer{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Viewer{}, fmt.Errorf("github user lookup failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var viewer Viewer
	if err := json.Unmarshal(body, &viewer); err != nil {
		return Viewer{}, err
	}
	if strings.TrimSpace(viewer.Login) == "" {
		return Viewer{}, fmt.Errorf("github user lookup did not return login")
	}
	return viewer, nil
}

func StoredTokenPath() (string, error) {
	if configDir := strings.TrimSpace(os.Getenv("PRTAGS_CONFIG_DIR")); configDir != "" {
		return filepath.Join(configDir, "auth.json"), nil
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "prtags", "auth.json"), nil
}

func LoadStoredToken() (StoredToken, error) {
	path, err := StoredTokenPath()
	if err != nil {
		return StoredToken{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return StoredToken{}, err
	}
	var token StoredToken
	if err := json.Unmarshal(raw, &token); err != nil {
		return StoredToken{}, err
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return StoredToken{}, fmt.Errorf("stored auth token is missing access_token")
	}
	return token, nil
}

func SaveStoredToken(token StoredToken) (string, error) {
	path, err := StoredTokenPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	token.Version = "v1"
	if strings.TrimSpace(token.Provider) == "" {
		token.Provider = "github"
	}
	if token.SavedAt.IsZero() {
		token.SavedAt = time.Now().UTC()
	}
	raw, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return "", err
	}
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, raw, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return "", err
	}
	return path, nil
}

func DeleteStoredToken() error {
	path, err := StoredTokenPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (c Config) postFormJSON(ctx context.Context, endpoint string, form url.Values, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github oauth request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, target); err != nil {
		return err
	}
	return nil
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func deviceFlowDeadline(expiresIn time.Duration) time.Time {
	now := time.Now().UTC()
	if expiresIn <= 0 {
		return now.Add(15 * time.Minute)
	}
	return now.Add(expiresIn)
}

func pollInterval(interval time.Duration) time.Duration {
	if interval < 0 {
		return 0
	}
	return interval
}

func deviceFlowPreflight(ctx context.Context, deadline time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if time.Now().UTC().After(deadline) {
		return fmt.Errorf("device authorization expired")
	}
	return nil
}

func (c Config) pollAccessTokenOnce(ctx context.Context, deviceCode string) (AccessTokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", c.ClientID)
	form.Set("device_code", strings.TrimSpace(deviceCode))
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	var response AccessTokenResponse
	if err := c.postFormJSON(ctx, c.OAuthBaseURL+"/login/oauth/access_token", form, &response); err != nil {
		return AccessTokenResponse{}, err
	}
	return response, nil
}

func nextPollAction(response AccessTokenResponse, currentInterval time.Duration) (bool, time.Duration, error) {
	switch strings.TrimSpace(response.Error) {
	case "":
		if strings.TrimSpace(response.AccessToken) == "" {
			return false, currentInterval, fmt.Errorf("access token response missing access_token")
		}
		return true, currentInterval, nil
	case "authorization_pending":
		return false, currentInterval, nil
	case "slow_down":
		return false, currentInterval + 5*time.Second, nil
	case "expired_token":
		return false, currentInterval, fmt.Errorf("device authorization expired")
	case "access_denied":
		return false, currentInterval, fmt.Errorf("device authorization denied")
	default:
		message := strings.TrimSpace(response.ErrorDescription)
		if message == "" {
			message = strings.TrimSpace(response.Error)
		}
		return false, currentInterval, fmt.Errorf("device authorization failed: %s", message)
	}
}

func waitForNextPoll(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return nil
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
