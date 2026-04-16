package publicid

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"

	petname "github.com/dustinkirkland/golang-petname"
)

const groupEntropyAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

func NewGroupID() (string, error) {
	name := strings.ToLower(strings.TrimSpace(petname.Generate(2, "-")))
	if name == "" {
		return "", fmt.Errorf("generate petname: empty result")
	}

	var suffix strings.Builder
	suffix.Grow(4)
	for i := 0; i < 4; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(groupEntropyAlphabet))))
		if err != nil {
			return "", fmt.Errorf("generate entropy: %w", err)
		}
		suffix.WriteByte(groupEntropyAlphabet[n.Int64()])
	}

	return name + "-" + suffix.String(), nil
}
