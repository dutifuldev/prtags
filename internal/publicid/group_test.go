package publicid

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewGroupID(t *testing.T) {
	value, err := NewGroupID()
	require.NoError(t, err)
	require.Regexp(t, regexp.MustCompile(`^[a-z0-9]+-[a-z0-9]+-[a-z0-9]{4}$`), value)
}
