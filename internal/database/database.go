package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivermigrate"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxIdleTime time.Duration
	ConnMaxLifetime time.Duration
}

func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxOpenConns:    5,
		MaxIdleConns:    2,
		ConnMaxIdleTime: 5 * time.Minute,
		ConnMaxLifetime: 30 * time.Minute,
	}
}

func Open(databaseURL string) (*gorm.DB, error) {
	return OpenWithPool(databaseURL, DefaultPoolConfig())
}

func OpenWithPool(databaseURL string, pool PoolConfig) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{
		Logger: NewQueryMetricsLogger(logger.New(log.New(os.Stdout, "\r\n", log.LstdFlags), logger.Config{
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
		})),
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}

	sqlDB.SetMaxOpenConns(pool.MaxOpenConns)
	sqlDB.SetMaxIdleConns(pool.MaxIdleConns)
	sqlDB.SetConnMaxIdleTime(pool.ConnMaxIdleTime)
	sqlDB.SetConnMaxLifetime(pool.ConnMaxLifetime)

	return db, nil
}

func RunMigrations(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}

	ctx := context.Background()
	if err := ensureSchemaMigrationsTable(ctx, sqlDB); err != nil {
		return err
	}

	dir, err := migrationsDir()
	if err != nil {
		return err
	}

	versions, err := migrationVersions(dir)
	if err != nil {
		return err
	}

	if err := applyPendingMigrations(ctx, sqlDB, dir, versions); err != nil {
		return err
	}

	migrator, err := rivermigrate.New(riverdatabasesql.New(sqlDB), nil)
	if err != nil {
		return err
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("apply river migrations: %w", err)
	}

	return nil
}

func applyPendingMigrations(ctx context.Context, sqlDB *sql.DB, dir string, versions []string) error {
	for _, version := range versions {
		applied, err := migrationApplied(ctx, sqlDB, version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := applyMigration(ctx, sqlDB, dir, version); err != nil {
			return err
		}
	}
	return nil
}

func migrationsDir() (string, error) {
	seen := map[string]struct{}{}
	for _, candidate := range migrationDirCandidates() {
		candidate = filepath.Clean(candidate)
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}

		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return candidate, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}

	return "", fmt.Errorf("migrations directory not found")
}

func ensureSchemaMigrationsTable(ctx context.Context, sqlDB *sql.DB) error {
	_, err := sqlDB.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	return err
}

func migrationVersions(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		versions = append(versions, strings.TrimSuffix(name, ".up.sql"))
	}
	sort.Strings(versions)
	return versions, nil
}

func migrationApplied(ctx context.Context, sqlDB *sql.DB, version string) (bool, error) {
	var applied string
	err := sqlDB.QueryRowContext(ctx, `SELECT version FROM schema_migrations WHERE version = $1`, version).Scan(&applied)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, err
	}
}

func applyMigration(ctx context.Context, sqlDB *sql.DB, dir, version string) error {
	contents, err := os.ReadFile(filepath.Join(dir, version+".up.sql"))
	if err != nil {
		return err
	}
	if _, err := sqlDB.ExecContext(ctx, string(contents)); err != nil {
		return fmt.Errorf("apply migration %s: %w", version, err)
	}
	_, err = sqlDB.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, version)
	return err
}

func migrationDirCandidates() []string {
	candidates := []string{}
	if env := strings.TrimSpace(os.Getenv("PRTAGS_MIGRATIONS_DIR")); env != "" {
		candidates = append(candidates, env)
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Join(filepath.Dir(file), "..", "..", "migrations"))
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "migrations"),
			filepath.Join(exeDir, "..", "migrations"),
			filepath.Join(exeDir, "..", "..", "migrations"),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "migrations"),
			filepath.Join(wd, "..", "migrations"),
		)
	}
	return candidates
}
