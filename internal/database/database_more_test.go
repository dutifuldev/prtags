package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMigrationHelpersApplyAndDetectVersions(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migrations.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	db, err := gdb.DB()
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	_, err = db.ExecContext(context.Background(), `CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	applied, err := migrationApplied(context.Background(), db, "000001_initial")
	require.NoError(t, err)
	require.False(t, applied)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "000001_initial.up.sql"), []byte(`CREATE TABLE helper_rows (id INTEGER PRIMARY KEY);`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ignore.down.sql"), []byte(`DROP TABLE helper_rows;`), 0o644))

	versions, err := migrationVersions(dir)
	require.NoError(t, err)
	require.Equal(t, []string{"000001_initial"}, versions)
	require.NoError(t, applyPendingMigrations(context.Background(), db, dir, versions))

	applied, err = migrationApplied(context.Background(), db, "000001_initial")
	require.NoError(t, err)
	require.True(t, applied)
}

func TestMigrationDirCandidatesAndErrors(t *testing.T) {
	_, err := migrationsDir()
	require.NoError(t, err)

	candidates := migrationDirCandidates()
	require.NotEmpty(t, candidates)
}

func TestOpenWithPoolRejectsInvalidURL(t *testing.T) {
	_, err := OpenWithPool("bad://url", DefaultPoolConfig())
	require.Error(t, err)
}

func TestApplyTestSchemaRejectsNilAndNonSQLite(t *testing.T) {
	require.Error(t, ApplyTestSchema(nil))

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, ApplyTestSchema(db))
}

func TestDatabaseOpenHelpersAndConflicts(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "schema.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	defer func() { require.NoError(t, sqlDB.Close()) }()

	require.Error(t, ensureSchemaMigrationsTable(context.Background(), sqlDB))

	pool := DefaultPoolConfig()
	require.Equal(t, 5, pool.MaxOpenConns)
	require.Equal(t, 2, pool.MaxIdleConns)
	require.False(t, isPublicIDConflict(nil))
	require.True(t, isPublicIDConflict(gorm.ErrDuplicatedKey))
}

func TestQueryMetricsRecordContextAndLogger(t *testing.T) {
	metrics := NewQueryMetrics()
	metrics.Record(2 * time.Millisecond)
	metrics.Record(5 * time.Millisecond)

	snapshot := metrics.Snapshot()
	require.Equal(t, 2, snapshot.QueryCount)
	require.Equal(t, 7*time.Millisecond, snapshot.QueryDuration)
	require.Equal(t, 5*time.Millisecond, snapshot.SlowestQuery)

	ctx := WithQueryMetrics(context.Background(), metrics)
	fromContext, ok := QueryMetricsFromContext(ctx)
	require.True(t, ok)
	require.Same(t, metrics, fromContext)

	wrapped := NewQueryMetricsLogger(logger.Default.LogMode(logger.Silent))
	wrapped.Trace(ctx, time.Now().Add(-time.Millisecond), func() (string, int64) {
		return "SELECT 1", 1
	}, nil)

	require.Equal(t, 3, metrics.Snapshot().QueryCount)

	metrics.RecordStep("repo_read", 2*time.Millisecond)
	metrics.RecordStep("search_text_rows", 1500*time.Microsecond)
	step := StartQueryStep(ctx, "annotations_batch")
	step.Done()
	snapshot = metrics.Snapshot()
	require.Contains(t, snapshot.Steps, "annotations_batch=")
	require.Contains(t, snapshot.Steps, "repo_read=2.0ms")
	require.Contains(t, snapshot.Steps, "search_text_rows=1.5ms")
}
