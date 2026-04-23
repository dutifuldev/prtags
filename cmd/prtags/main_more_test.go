package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dutifuldev/prtags/internal/auth"
	"github.com/dutifuldev/prtags/internal/config"
	"github.com/dutifuldev/prtags/internal/jsend"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type testContextKey string

func TestGroupSearchTargetsAndParserHelpers(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jsend.Success(map[string]any{"ok": true}))
	}))
	defer server.Close()

	_, stderr, err := runCLI(t, server.URL, "group", "create", "-R", "acme/widgets", "--kind", "mixed", "--title", "Auth")
	require.NoError(t, err, stderr)
	_, stderr, err = runCLI(t, server.URL, "group", "list", "-R", "acme/widgets")
	require.NoError(t, err, stderr)
	_, stderr, err = runCLI(t, server.URL, "group", "get", "steady-otter-k4m2", "--include-metadata")
	require.NoError(t, err, stderr)
	_, stderr, err = runCLI(t, server.URL, "group", "update", "steady-otter-k4m2", "--title", "Updated")
	require.NoError(t, err, stderr)
	_, stderr, err = runCLI(t, server.URL, "group", "add-pr", "steady-otter-k4m2", "22")
	require.NoError(t, err, stderr)
	_, stderr, err = runCLI(t, server.URL, "group", "add-issue", "steady-otter-k4m2", "11")
	require.NoError(t, err, stderr)
	_, stderr, err = runCLI(t, server.URL, "group", "sync-comments", "steady-otter-k4m2")
	require.NoError(t, err, stderr)
	_, stderr, err = runCLI(t, server.URL, "group", "list-comment-sync-targets", "-R", "acme/widgets")
	require.NoError(t, err, stderr)
	_, stderr, err = runCLI(t, server.URL, "search", "text", "-R", "acme/widgets", "auth")
	require.NoError(t, err, stderr)
	_, stderr, err = runCLI(t, server.URL, "search", "similar", "-R", "acme/widgets", "auth")
	require.NoError(t, err, stderr)
	_, stderr, err = runCLI(t, server.URL, "targets", "filter", "-R", "acme/widgets", "--type", "pull_request", "--field", "intent", "--value", "auth")
	require.NoError(t, err, stderr)

	require.Contains(t, requests, "POST /v1/repos/acme/widgets/groups")
	require.Contains(t, requests, "GET /v1/repos/acme/widgets/groups")
	require.Contains(t, requests, "PATCH /v1/groups/steady-otter-k4m2")
	require.Contains(t, requests, "POST /v1/repos/acme/widgets/search/text")
	require.Contains(t, requests, "GET /v1/repos/acme/widgets/targets")
}

func TestAnnotationPairParsingHelpers(t *testing.T) {
	parsed, err := parseAnnotationPairs([]string{"flag=true", "count=3", "items=[\"a\"]", "note=hello", "gone=null"})
	require.NoError(t, err)
	require.Equal(t, true, parsed["flag"])
	require.EqualValues(t, 3, parsed["count"])
	require.Equal(t, []any{"a"}, parsed["items"])
	require.Equal(t, "hello", parsed["note"])
	require.Nil(t, parsed["gone"])

	_, err = parseAnnotationPairs([]string{"bad"})
	require.Error(t, err)
	_, _, err = parseAnnotationPair(" =oops")
	require.Error(t, err)
	value, err := parseAnnotationValue("plain")
	require.NoError(t, err)
	require.Equal(t, "plain", value)
}

func TestMiscCLIHelpers(t *testing.T) {
	require.Equal(t, "{\n  \"ok\": true\n}", prettyJSON([]byte(`{"ok":true}`)))
	require.Equal(t, "not-json", prettyJSON([]byte(`not-json`)))
	owner, repo, err := splitRepo("acme/widgets")
	require.NoError(t, err)
	require.Equal(t, "acme", owner)
	require.Equal(t, "widgets", repo)
	_, _, err = splitRepo("bad")
	require.Error(t, err)
}

func TestAuthAndRuntimeHelpers(t *testing.T) {
	cmd := &cobra.Command{Use: "auth"}
	cmd.SetOut(new(bytes.Buffer))
	cmd.Flags().String("client-id", "", "")
	cmd.Flags().String("scope", "", "")
	require.NoError(t, cmd.Flags().Set("client-id", "override-client"))
	require.NoError(t, cmd.Flags().Set("scope", "repo"))

	cfg := authConfigFromFlags(cmd)
	require.Equal(t, "override-client", cfg.ClientID)
	require.Equal(t, "repo", cfg.Scope)

	require.Equal(t, context.Background(), commandContext(&cobra.Command{}))
	withContext := &cobra.Command{}
	withContext.SetContext(context.WithValue(context.Background(), testContextKey("k"), "v"))
	require.Equal(t, "v", commandContext(withContext).Value(testContextKey("k")))

	device := auth.DeviceCodeResponse{VerificationURI: "https://github.com/login/device", UserCode: "ABCD"}
	printDeviceFlowPrompt(cmd, device)
	out := cmd.OutOrStdout().(*bytes.Buffer).String()
	require.Contains(t, out, device.VerificationURI)
	require.Contains(t, out, device.UserCode)

	tempDir := t.TempDir()
	t.Setenv("PRTAGS_CONFIG_DIR", tempDir)
	viewerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(auth.Viewer{Login: "dutifulbob", ID: 7})
	}))
	defer viewerServer.Close()
	saveCmd := &cobra.Command{}
	saveOut := new(bytes.Buffer)
	saveCmd.SetOut(saveOut)
	err := saveViewerToken(saveCmd, auth.Config{ClientID: "client", Scope: "repo", OAuthBaseURL: viewerServer.URL, APIBaseURL: viewerServer.URL, HTTPClient: viewerServer.Client()}, auth.AccessTokenResponse{AccessToken: "gho", Scope: "repo", TokenType: "bearer"})
	require.NoError(t, err)
	require.Contains(t, saveOut.String(), "Logged in as dutifulbob")

	_, err = openConfiguredDatabase(config.Config{
		DatabaseURL:        "sqlite://" + filepath.Join(t.TempDir(), "prtags.db"),
		DBMaxOpenConns:     1,
		DBMaxIdleConns:     1,
		DBConnMaxIdleTime:  time.Minute,
		DBConnMaxLifetime:  time.Minute,
		GHReplicaBaseURL:   "https://ghreplica.example",
		WorkerPollInterval: time.Second,
		EmbeddingModel:     "local-hash@1",
	})
	require.Error(t, err)

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.Nil(t, buildCommentSyncService(db, config.Config{}))
	require.NotNil(t, buildCommentSyncService(db, config.Config{
		GitHubBaseURL:           viewerServer.URL,
		GitHubAppID:             "1",
		GitHubInstallationID:    "2",
		GitHubAppPrivateKeyPEM:  "pem",
		GitHubAppPrivateKeyPath: "",
	}))

	dispatcher, err := newRiverDispatcherForDB(db, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, dispatcher)
}

func TestFieldAndAccessHelpers(t *testing.T) {
	require.True(t, stringSlicesEqual([]string{"a", "b"}, []string{"a", "b"}))
	require.False(t, stringSlicesEqual([]string{"a"}, []string{"b"}))
	require.Equal(t, []string{"a", "b"}, normalizeEnumValuesCLI([]string{" b ", "a", "a"}))
	require.Equal(t, "json", defaultFieldListFormat(new(bytes.Buffer)))

	var buf bytes.Buffer
	fields := []fieldDefinitionView{{ID: 1, Name: "intent", ObjectScope: "pull_request", FieldType: "text", DisplayName: "Intent"}}
	require.NoError(t, printFieldDefinitions(&buf, fields, "table"))
	require.Contains(t, buf.String(), "intent")

	buf.Reset()
	require.NoError(t, printFieldDefinitions(&buf, fields, "json"))
	require.Contains(t, buf.String(), `"status": "success"`)

	tempFile, err := os.CreateTemp(t.TempDir(), "stdout")
	require.NoError(t, err)
	defer func() { require.NoError(t, tempFile.Close()) }()
	require.Equal(t, "json", defaultFieldListFormat(tempFile))

	require.NotNil(t, newAccessGrantCommand())
	var repo string
	require.NotNil(t, newAccessGrantUpsertCommand(&repo))
	require.NotNil(t, newAccessGrantListCommand(&repo))
	require.NotNil(t, newAccessGrantRevokeCommand(&repo))
}

func TestOpenOpsServiceWithSQLite(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("DATABASE_URL", "sqlite://"+filepath.Join(tempDir, "ops.db"))
	t.Setenv("GHREPLICA_BASE_URL", "https://ghreplica.example")
	t.Setenv("DB_MAX_OPEN_CONNS", "1")
	t.Setenv("DB_MAX_IDLE_CONNS", "1")
	t.Setenv("DB_CONN_MAX_IDLE_TIME", "1m")
	t.Setenv("DB_CONN_MAX_LIFETIME", "1m")
	t.Setenv("EMBEDDING_MODEL", "local-hash@1")

	_, cleanup, err := openOpsService()
	require.Error(t, err)
	require.Nil(t, cleanup)
}
