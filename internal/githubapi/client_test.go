package githubapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthorizationTokenFailsWhenPrivateKeyPathIsUnreadable(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	client := NewClient("https://api.github.com", AuthConfig{
		AppID:          "42",
		InstallationID: "123",
		PrivateKeyPath: dir,
	})

	_, err := client.authorizationToken(context.Background())
	require.Error(t, err)
}
