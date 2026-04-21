package githubapi

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type AuthConfig struct {
	AppID          string
	InstallationID string
	PrivateKeyPEM  string
	PrivateKeyPath string
}

type Client struct {
	baseURL    string
	auth       AuthConfig
	httpClient *http.Client

	mu          sync.Mutex
	cachedToken string
	tokenExpiry time.Time
	lastWriteAt time.Time
}

type IssueComment struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
}

type Error struct {
	StatusCode int
	Message    string
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	return fmt.Sprintf("github api error (%d)", e.StatusCode)
}

func NewClient(baseURL string, auth AuthConfig) *Client {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		auth: AuthConfig{
			AppID:          strings.TrimSpace(auth.AppID),
			InstallationID: strings.TrimSpace(auth.InstallationID),
			PrivateKeyPEM:  strings.TrimSpace(auth.PrivateKeyPEM),
			PrivateKeyPath: strings.TrimSpace(auth.PrivateKeyPath),
		},
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Enabled() bool {
	return c.auth.AppID != "" && c.auth.InstallationID != "" &&
		(c.auth.PrivateKeyPEM != "" || c.auth.PrivateKeyPath != "")
}

func (c *Client) CreateIssueComment(ctx context.Context, owner, repo string, number int, body string) (IssueComment, error) {
	if err := c.waitWriteTurn(ctx); err != nil {
		return IssueComment{}, err
	}
	var out IssueComment
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number), map[string]string{"body": body}, &out)
	return out, err
}

func (c *Client) UpdateIssueComment(ctx context.Context, owner, repo string, commentID int64, body string) (IssueComment, error) {
	if err := c.waitWriteTurn(ctx); err != nil {
		return IssueComment{}, err
	}
	var out IssueComment
	err := c.doJSON(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/%s/issues/comments/%d", owner, repo, commentID), map[string]string{"body": body}, &out)
	return out, err
}

func (c *Client) DeleteIssueComment(ctx context.Context, owner, repo string, commentID int64) error {
	if err := c.waitWriteTurn(ctx); err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/%s/issues/comments/%d", owner, repo, commentID), nil, nil)
}

func (c *Client) GetIssueComment(ctx context.Context, owner, repo string, commentID int64) (IssueComment, error) {
	var out IssueComment
	err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/issues/comments/%d", owner, repo, commentID), nil, &out)
	return out, err
}

func (c *Client) ListIssueCommentsForIssue(ctx context.Context, owner, repo string, number int) ([]IssueComment, error) {
	var out []IssueComment
	err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100", owner, repo, number), nil, &out)
	return out, err
}

func (c *Client) waitWriteTurn(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.lastWriteAt.IsZero() {
		waitFor := time.Until(c.lastWriteAt.Add(time.Second))
		if waitFor > 0 {
			timer := time.NewTimer(waitFor)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	c.lastWriteAt = time.Now().UTC()
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, out any) error {
	target := path
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = c.baseURL + target
	}

	var reqBody io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "prtags")

	token, err := c.authorizationToken(ctx)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return decodeHTTPError(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) authorizationToken(ctx context.Context) (string, error) {
	if !c.Enabled() {
		return "", nil
	}

	c.mu.Lock()
	if c.cachedToken != "" && time.Until(c.tokenExpiry) > 2*time.Minute {
		token := c.cachedToken
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()

	jwtToken, err := c.appJWT()
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+fmt.Sprintf("/app/installations/%s/access_tokens", c.auth.InstallationID), bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "prtags")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", decodeHTTPError(resp)
	}

	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Token) == "" {
		return "", fmt.Errorf("github app token response missing token")
	}

	c.mu.Lock()
	c.cachedToken = out.Token
	c.tokenExpiry = out.ExpiresAt
	c.mu.Unlock()
	return out.Token, nil
}

func (c *Client) appJWT() (string, error) {
	privateKey, err := c.privateKey()
	if err != nil {
		return "", err
	}

	headerJSON, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	now := time.Now().UTC()
	payloadJSON, _ := json.Marshal(map[string]any{
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": c.auth.AppID,
	})

	unsigned := encodeSegment(headerJSON) + "." + encodeSegment(payloadJSON)
	hash := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, hash[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + encodeSegment(signature), nil
}

func (c *Client) privateKey() (*rsa.PrivateKey, error) {
	keyPEM := c.auth.PrivateKeyPEM
	if keyPEM == "" && c.auth.PrivateKeyPath != "" {
		body, err := os.ReadFile(c.auth.PrivateKeyPath)
		if err != nil {
			return nil, err
		}
		keyPEM = string(body)
	}
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, fmt.Errorf("github app private key is invalid")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("github app private key must be RSA")
	}
	return key, nil
}

func encodeSegment(in []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(in), "=")
}

func decodeHTTPError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	message := strings.TrimSpace(string(body))
	if message != "" {
		var payload struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Message) != "" {
			message = strings.TrimSpace(payload.Message)
		}
	}

	apiErr := &Error{
		StatusCode: resp.StatusCode,
		Message:    message,
	}
	if value := strings.TrimSpace(resp.Header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
			apiErr.RetryAfter = time.Duration(seconds) * time.Second
		}
	}
	if apiErr.RetryAfter == 0 {
		if reset := strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")); reset != "" {
			if unixSeconds, err := strconv.ParseInt(reset, 10, 64); err == nil {
				resetAt := time.Unix(unixSeconds, 0).UTC()
				if delay := time.Until(resetAt); delay > 0 {
					apiErr.RetryAfter = delay
				}
			}
		}
	}
	return apiErr
}
