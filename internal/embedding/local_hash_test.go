package embedding

import (
	"context"
	"math"
	"testing"
)

func TestNewLocalHashProviderDefaultsDimensions(t *testing.T) {
	provider := NewLocalHashProvider("local", 0)

	if provider.Model() != "local" {
		t.Fatalf("expected model to round-trip, got %q", provider.Model())
	}
	if provider.Dimensions() != 128 {
		t.Fatalf("expected default dimensions, got %d", provider.Dimensions())
	}
}

func TestLocalHashProviderEmbedEmptyText(t *testing.T) {
	provider := NewLocalHashProvider("local", 8)

	vector, err := provider.Embed(context.Background(), "   ")
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	if len(vector) != 8 {
		t.Fatalf("expected vector length 8, got %d", len(vector))
	}
	for i, value := range vector {
		if value != 0 {
			t.Fatalf("expected zero vector entry at %d, got %f", i, value)
		}
	}
}

func TestLocalHashProviderEmbedIsStableAndNormalized(t *testing.T) {
	provider := NewLocalHashProvider("local", 32)

	a, err := provider.Embed(context.Background(), "Hello WORLD")
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	b, err := provider.Embed(context.Background(), " hello world ")
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	if len(a) != len(b) {
		t.Fatalf("expected equal vector lengths, got %d and %d", len(a), len(b))
	}

	var magnitude float64
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("expected stable normalized output at %d, got %f vs %f", i, a[i], b[i])
		}
		magnitude += float64(a[i] * a[i])
	}
	if math.Abs(math.Sqrt(magnitude)-1) > 1e-5 {
		t.Fatalf("expected unit-normalized vector, magnitude=%f", math.Sqrt(magnitude))
	}
}

func TestTrigrams(t *testing.T) {
	if out := trigrams("go"); out != nil {
		t.Fatalf("expected nil for short token, got %#v", out)
	}

	out := trigrams("token")
	want := []string{"tok", "oke", "ken"}
	if len(out) != len(want) {
		t.Fatalf("expected %d trigrams, got %d", len(want), len(out))
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("expected trigram %q at %d, got %q", want[i], i, out[i])
		}
	}
}
