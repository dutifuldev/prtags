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
)

type Client struct {
	baseURL    string
	httpClient *http.Client
	authToken  string
	actorID    string
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		authToken: firstNonEmptyEnv("PRTAGS_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"),
		actorID:   firstNonEmptyEnv("PRTAGS_ACTOR", "X_ACTOR"),
	}
}

func (c *Client) DoJSON(ctx context.Context, method, path string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(c.authToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.authToken))
	} else if strings.TrimSpace(c.actorID) != "" {
		req.Header.Set("X-Actor", strings.TrimSpace(c.actorID))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
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
