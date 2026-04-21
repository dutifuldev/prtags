package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFromEnvParsesDatabasePoolSettings(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("GHREPLICA_BASE_URL", "https://ghreplica.example")
	t.Setenv("DB_MAX_OPEN_CONNS", "7")
	t.Setenv("DB_MAX_IDLE_CONNS", "3")
	t.Setenv("DB_CONN_MAX_IDLE_TIME", "90s")
	t.Setenv("DB_CONN_MAX_LIFETIME", "45m")

	cfg := FromEnv()
	require.Equal(t, 7, cfg.DBMaxOpenConns)
	require.Equal(t, 3, cfg.DBMaxIdleConns)
	require.Equal(t, 90*time.Second, cfg.DBConnMaxIdleTime)
	require.Equal(t, 45*time.Minute, cfg.DBConnMaxLifetime)
	require.NoError(t, cfg.Validate())
}

func TestValidateRejectsInvalidDatabasePoolSettings(t *testing.T) {
	cfg := Config{
		DatabaseURL:        "postgres://example",
		DBMaxOpenConns:     2,
		DBMaxIdleConns:     3,
		DBConnMaxIdleTime:  time.Minute,
		DBConnMaxLifetime:  time.Minute,
		GHReplicaBaseURL:   "https://ghreplica.example",
		WorkerPollInterval: time.Second,
		EmbeddingModel:     "local-hash@1",
	}

	err := cfg.Validate()
	require.ErrorContains(t, err, "DB_MAX_IDLE_CONNS cannot exceed DB_MAX_OPEN_CONNS")
}
