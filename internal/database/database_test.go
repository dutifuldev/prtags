package database

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrationsDirPrefersExplicitEnvironmentOverride(t *testing.T) {
	tempDir := t.TempDir()
	override := filepath.Join(tempDir, "migrations")
	require.NoError(t, os.MkdirAll(override, 0o755))

	t.Setenv("PRTAGS_MIGRATIONS_DIR", override)

	dir, err := migrationsDir()
	require.NoError(t, err)
	require.Equal(t, override, dir)
}
