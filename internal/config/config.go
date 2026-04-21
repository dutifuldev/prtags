package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr              string
	DatabaseURL             string
	DBMaxOpenConns          int
	DBMaxIdleConns          int
	DBConnMaxIdleTime       time.Duration
	DBConnMaxLifetime       time.Duration
	GHReplicaBaseURL        string
	GitHubBaseURL           string
	GitHubAppID             string
	GitHubInstallationID    string
	GitHubAppPrivateKeyPEM  string
	GitHubAppPrivateKeyPath string
	AllowUnauthWrites       bool
	EnableWorker            bool
	WorkerPollInterval      time.Duration
	EmbeddingModel          string
}

func FromEnv() Config {
	cfg := Config{
		ListenAddr:              envOrDefault("LISTEN_ADDR", ":8081"),
		DatabaseURL:             strings.TrimSpace(os.Getenv("DATABASE_URL")),
		DBMaxOpenConns:          envInt("DB_MAX_OPEN_CONNS", 5),
		DBMaxIdleConns:          envInt("DB_MAX_IDLE_CONNS", 2),
		DBConnMaxIdleTime:       envDuration("DB_CONN_MAX_IDLE_TIME", 5*time.Minute),
		DBConnMaxLifetime:       envDuration("DB_CONN_MAX_LIFETIME", 30*time.Minute),
		GHReplicaBaseURL:        envOrDefault("GHREPLICA_BASE_URL", "https://ghreplica.dutiful.dev"),
		GitHubBaseURL:           envOrDefault("GITHUB_BASE_URL", "https://api.github.com"),
		GitHubAppID:             strings.TrimSpace(os.Getenv("GITHUB_APP_ID")),
		GitHubInstallationID:    strings.TrimSpace(os.Getenv("GITHUB_APP_INSTALLATION_ID")),
		GitHubAppPrivateKeyPEM:  strings.TrimSpace(os.Getenv("GITHUB_APP_PRIVATE_KEY_PEM")),
		GitHubAppPrivateKeyPath: strings.TrimSpace(os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH")),
		AllowUnauthWrites:       envBool("ALLOW_UNAUTH_WRITES", false),
		EnableWorker:            envBool("ENABLE_BACKGROUND_WORKER", true),
		WorkerPollInterval:      envDuration("WORKER_POLL_INTERVAL", 2*time.Second),
		EmbeddingModel:          envOrDefault("EMBEDDING_MODEL", "local-hash@1"),
	}
	return cfg
}

func (c Config) Validate() error {
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	if c.DBMaxOpenConns <= 0 {
		return errors.New("DB_MAX_OPEN_CONNS must be positive")
	}
	if c.DBMaxIdleConns < 0 {
		return errors.New("DB_MAX_IDLE_CONNS must be zero or positive")
	}
	if c.DBMaxIdleConns > c.DBMaxOpenConns {
		return errors.New("DB_MAX_IDLE_CONNS cannot exceed DB_MAX_OPEN_CONNS")
	}
	if c.DBConnMaxIdleTime <= 0 {
		return errors.New("DB_CONN_MAX_IDLE_TIME must be positive")
	}
	if c.DBConnMaxLifetime <= 0 {
		return errors.New("DB_CONN_MAX_LIFETIME must be positive")
	}
	if strings.TrimSpace(c.GHReplicaBaseURL) == "" {
		return errors.New("GHREPLICA_BASE_URL is required")
	}
	if c.WorkerPollInterval <= 0 {
		return errors.New("WORKER_POLL_INTERVAL must be positive")
	}
	if strings.TrimSpace(c.EmbeddingModel) == "" {
		return errors.New("EMBEDDING_MODEL is required")
	}
	hasAnyGitHubAppValue := strings.TrimSpace(c.GitHubAppID) != "" ||
		strings.TrimSpace(c.GitHubInstallationID) != "" ||
		strings.TrimSpace(c.GitHubAppPrivateKeyPEM) != "" ||
		strings.TrimSpace(c.GitHubAppPrivateKeyPath) != ""
	if hasAnyGitHubAppValue {
		if strings.TrimSpace(c.GitHubAppID) == "" {
			return errors.New("GITHUB_APP_ID is required when GitHub App auth is configured")
		}
		if strings.TrimSpace(c.GitHubInstallationID) == "" {
			return errors.New("GITHUB_APP_INSTALLATION_ID is required when GitHub App auth is configured")
		}
		if strings.TrimSpace(c.GitHubAppPrivateKeyPEM) == "" && strings.TrimSpace(c.GitHubAppPrivateKeyPath) == "" {
			return errors.New("GITHUB_APP_PRIVATE_KEY_PEM or GITHUB_APP_PRIVATE_KEY_PATH is required when GitHub App auth is configured")
		}
	}
	return nil
}

func (c Config) HasGitHubApp() bool {
	return strings.TrimSpace(c.GitHubAppID) != "" &&
		strings.TrimSpace(c.GitHubInstallationID) != "" &&
		(strings.TrimSpace(c.GitHubAppPrivateKeyPEM) != "" || strings.TrimSpace(c.GitHubAppPrivateKeyPath) != "")
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
