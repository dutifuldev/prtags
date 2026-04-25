package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dutifuldev/prtags/internal/auth"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
	authToken  string
	actorID    string
}

type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("request failed (%d): %s", e.StatusCode, strings.TrimSpace(e.Body))
}

func NewClient(baseURL string) *Client {
	authToken := strings.TrimSpace(firstNonEmptyEnv("PRTAGS_GITHUB_TOKEN"))
	if authToken == "" {
		if stored, err := auth.LoadStoredToken(); err == nil {
			authToken = strings.TrimSpace(stored.AccessToken)
		}
	}
	if authToken == "" {
		authToken = strings.TrimSpace(firstNonEmptyEnv("GITHUB_TOKEN", "GH_TOKEN"))
	}

	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		authToken: authToken,
		actorID:   firstNonEmptyEnv("PRTAGS_ACTOR", "X_ACTOR"),
	}
}

func (c *Client) DoJSON(ctx context.Context, method, path string, payload any) ([]byte, error) {
	body, hasPayload, err := jsonRequestBody(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	applyJSONHeaders(req, hasPayload, c.authToken, c.actorID)
	return c.do(req)
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: string(raw)}
	}
	return raw, nil
}

func jsonRequestBody(payload any) (io.Reader, bool, error) {
	if payload == nil {
		return nil, false, nil
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, false, err
	}
	return bytes.NewReader(raw), true, nil
}

func applyJSONHeaders(req *http.Request, hasPayload bool, authToken, actorID string) {
	if hasPayload {
		req.Header.Set("Content-Type", "application/json")
	}
	if token := strings.TrimSpace(authToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		return
	}
	if actor := strings.TrimSpace(actorID); actor != "" {
		req.Header.Set("X-Actor", actor)
	}
}

func ExtractJSendData(raw []byte) ([]byte, error) {
	var envelope struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if envelope.Status == "" {
		return nil, fmt.Errorf("response is not a jsend envelope")
	}
	if len(envelope.Data) == 0 {
		return []byte("null"), nil
	}
	return envelope.Data, nil
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
