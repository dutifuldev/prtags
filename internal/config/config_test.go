package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFromEnvParsesDatabasePoolSettings(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("PRTAGS_SCHEMA", "prtags")
	t.Setenv("GHREPLICA_SCHEMA", "ghreplica")
	t.Setenv("DB_MAX_OPEN_CONNS", "7")
	t.Setenv("DB_MAX_IDLE_CONNS", "3")
	t.Setenv("DB_CONN_MAX_IDLE_TIME", "90s")
	t.Setenv("DB_CONN_MAX_LIFETIME", "45m")

	cfg := FromEnv()
	require.Equal(t, 7, cfg.DBMaxOpenConns)
	require.Equal(t, 3, cfg.DBMaxIdleConns)
	require.Equal(t, 90*time.Second, cfg.DBConnMaxIdleTime)
	require.Equal(t, 45*time.Minute, cfg.DBConnMaxLifetime)
	require.Equal(t, "prtags", cfg.PRTagsSchema)
	require.Equal(t, "ghreplica", cfg.GHReplicaSchema)
	require.NoError(t, cfg.Validate())
}

func TestValidateRejectsInvalidDatabasePoolSettings(t *testing.T) {
	cfg := Config{
		DatabaseURL:        "postgres://example",
		DBMaxOpenConns:     2,
		DBMaxIdleConns:     3,
		DBConnMaxIdleTime:  time.Minute,
		DBConnMaxLifetime:  time.Minute,
		PRTagsSchema:       "public",
		GHReplicaSchema:    "public",
		WorkerPollInterval: time.Second,
		EmbeddingModel:     "local-hash@1",
	}

	err := cfg.Validate()
	require.ErrorContains(t, err, "DB_MAX_IDLE_CONNS cannot exceed DB_MAX_OPEN_CONNS")
}

func TestConfigHelpersAndGitHubAppValidation(t *testing.T) {
	t.Setenv("BOOL_ENV", "true")
	t.Setenv("INT_ENV", "17")
	t.Setenv("DURATION_ENV", "3s")
	require.True(t, envBool("BOOL_ENV", false))
	require.Equal(t, 17, envInt("INT_ENV", 0))
	require.Equal(t, 3*time.Second, envDuration("DURATION_ENV", 0))
	require.Equal(t, "fallback", envOrDefault("MISSING_ENV", "fallback"))

	cfg := Config{
		DatabaseURL:             "sqlite:///tmp/test.db",
		DBMaxOpenConns:          1,
		DBMaxIdleConns:          1,
		DBConnMaxIdleTime:       time.Minute,
		DBConnMaxLifetime:       time.Minute,
		PRTagsSchema:            "public",
		GHReplicaSchema:         "public",
		WorkerPollInterval:      time.Second,
		EmbeddingModel:          "local-hash@1",
		GitHubAppID:             "1",
		GitHubInstallationID:    "2",
		GitHubAppPrivateKeyPath: "/tmp/key.pem",
	}
	require.True(t, cfg.HasGitHubApp())
	require.NoError(t, cfg.Validate())

	cfg.GitHubAppPrivateKeyPath = ""
	cfg.GitHubAppPrivateKeyPEM = ""
	err := cfg.Validate()
	require.ErrorContains(t, err, "GITHUB_APP_PRIVATE_KEY_PEM or GITHUB_APP_PRIVATE_KEY_PATH")

	cfg.GitHubAppID = ""
	require.True(t, hasAnyGitHubAppValue(cfg))
	require.ErrorContains(t, validateGitHubApp(cfg), "GITHUB_APP_ID is required")
}

func TestConfigValidationErrors(t *testing.T) {
	require.ErrorContains(t, validateDatabase(Config{}), "DATABASE_URL is required")
	require.ErrorContains(t, validatePool(Config{DBMaxOpenConns: 0, DBMaxIdleConns: 0, DBConnMaxIdleTime: time.Second, DBConnMaxLifetime: time.Second}), "DB_MAX_OPEN_CONNS")
	require.ErrorContains(t, validateSchemasAndWorker(Config{PRTagsSchema: "bad-schema", GHReplicaSchema: "public", WorkerPollInterval: time.Second}), "PRTAGS_SCHEMA")
	require.ErrorContains(t, validateSchemasAndWorker(Config{PRTagsSchema: "public", GHReplicaSchema: "bad-schema", WorkerPollInterval: time.Second}), "GHREPLICA_SCHEMA")
	require.ErrorContains(t, validateSchemasAndWorker(Config{PRTagsSchema: "public", GHReplicaSchema: "public"}), "WORKER_POLL_INTERVAL")
	require.ErrorContains(t, validateEmbedding(Config{}), "EMBEDDING_MODEL is required")
}
