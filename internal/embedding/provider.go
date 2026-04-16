package embedding

import (
	"context"
	"hash/fnv"
	"math"
	"regexp"
	"strings"
)

type Provider interface {
	Model() string
	Dimensions() int
	Embed(ctx context.Context, text string) ([]float32, error)
}

type LocalHashProvider struct {
	model      string
	dimensions int
}

var tokenPattern = regexp.MustCompile(`[a-z0-9_./-]+`)

func NewLocalHashProvider(model string, dimensions int) *LocalHashProvider {
	if dimensions <= 0 {
		dimensions = 128
	}
	return &LocalHashProvider{model: model, dimensions: dimensions}
}

func (p *LocalHashProvider) Model() string {
	return p.model
}

func (p *LocalHashProvider) Dimensions() int {
	return p.dimensions
}

func (p *LocalHashProvider) Embed(_ context.Context, text string) ([]float32, error) {
	vector := make([]float32, p.dimensions)
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return vector, nil
	}

	for _, token := range tokenPattern.FindAllString(normalized, -1) {
		p.addHash(vector, token, 1.0)
		for _, trigram := range trigrams(token) {
			p.addHash(vector, trigram, 0.35)
		}
	}

	var magnitude float64
	for _, value := range vector {
		magnitude += float64(value * value)
	}
	if magnitude == 0 {
		return vector, nil
	}

	scale := float32(1 / math.Sqrt(magnitude))
	for i := range vector {
		vector[i] *= scale
	}
	return vector, nil
}

func (p *LocalHashProvider) addHash(vector []float32, value string, weight float32) {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(value))
	hashValue := hasher.Sum64()
	index := int(hashValue % uint64(len(vector)))
	sign := float32(1)
	if (hashValue>>63)&1 == 1 {
		sign = -1
	}
	vector[index] += sign * weight
}

func trigrams(token string) []string {
	if len(token) < 3 {
		return nil
	}
	out := make([]string, 0, len(token)-2)
	for i := 0; i+3 <= len(token); i++ {
		out = append(out, token[i:i+3])
	}
	return out
}
