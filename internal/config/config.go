package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr         string
	DatabaseURL        string
	GHReplicaBaseURL   string
	AllowUnauthWrites  bool
	EnableWorker       bool
	WorkerPollInterval time.Duration
	EmbeddingModel     string
}

func FromEnv() Config {
	cfg := Config{
		ListenAddr:         envOrDefault("LISTEN_ADDR", ":8081"),
		DatabaseURL:        strings.TrimSpace(os.Getenv("DATABASE_URL")),
		GHReplicaBaseURL:   envOrDefault("GHREPLICA_BASE_URL", "https://ghreplica.dutiful.dev"),
		AllowUnauthWrites:  envBool("ALLOW_UNAUTH_WRITES", false),
		EnableWorker:       envBool("ENABLE_BACKGROUND_WORKER", true),
		WorkerPollInterval: envDuration("WORKER_POLL_INTERVAL", 2*time.Second),
		EmbeddingModel:     envOrDefault("EMBEDDING_MODEL", "local-hash@1"),
	}
	return cfg
}

func (c Config) Validate() error {
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
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
	return nil
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
