package database

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAutoMigrateIsDisabled(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.ErrorIs(t, AutoMigrate(db), ErrAutoMigrateDisabled)
}

func TestApplyTestSchemaCreatesSQLiteTables(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, ApplyTestSchema(db))
	require.True(t, db.Migrator().HasTable((&RepositoryProjection{}).TableName()))
	require.True(t, db.Migrator().HasTable((&Group{}).TableName()))
	require.True(t, db.Migrator().HasTable((&GroupCommentSyncTarget{}).TableName()))
}

func TestSchemaModelsDeclareExplicitTableNames(t *testing.T) {
	type tableNamer interface {
		TableName() string
	}

	seen := make(map[string]struct{})
	for _, model := range schemaModels() {
		namer, ok := model.(tableNamer)
		require.True(t, ok, "model %T must declare TableName()", model)
		name := namer.TableName()
		require.NotEmpty(t, name, "model %T must return a non-empty table name", model)
		_, exists := seen[name]
		require.False(t, exists, "duplicate table name %q declared in schema model registry", name)
		seen[name] = struct{}{}
	}
}

func TestMigrationsDirPrefersExplicitEnvironmentOverride(t *testing.T) {
	tempDir := t.TempDir()
	override := filepath.Join(tempDir, "migrations")
	require.NoError(t, os.MkdirAll(override, 0o755))

	t.Setenv("PRTAGS_MIGRATIONS_DIR", override)

	dir, err := migrationsDir()
	require.NoError(t, err)
	require.Equal(t, override, dir)
}
