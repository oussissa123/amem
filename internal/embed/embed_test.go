package embed

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllama(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{3, 4}, {0, 2}}})
	}))
	defer srv.Close()
	e, err := New(Config{Provider: "ollama", BaseURL: srv.URL, Model: "m", DocumentPrefix: "passage: "})
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.Embed(context.Background(), []string{"a", "b"}, Document)
	if err != nil {
		t.Fatal(err)
	}
	if v[0][0] != 0.6 || v[0][1] != 0.8 || v[1][1] != 1 {
		t.Errorf("vectors are not normalised: %v", v)
	}
	if in := got["input"].([]any); in[0] != "passage: a" {
		t.Errorf("document prefix not applied: %v", in)
	}
	if m := e.Model(); m.Dims != 2 || m.Provider != "ollama" {
		t.Errorf("model %+v", m)
	}
}

func TestOpenAIOrderAndErrors(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("missing key")
		}
		if status != http.StatusOK {
			http.Error(w, "too long", status)
			return
		}
		// Out of order on purpose: the index field gives the position.
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"index": 1, "embedding": []float32{0, 1}}, {"index": 0, "embedding": []float32{1, 0}}}})
	}))
	defer srv.Close()
	t.Setenv("TEST_KEY", "sk-test")
	e, err := New(Config{Provider: "openai", BaseURL: srv.URL, Model: "m", APIKeyEnv: "TEST_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.Embed(context.Background(), []string{"x", "y"}, Query)
	if err != nil || v[0][0] != 1 || v[1][1] != 1 {
		t.Fatalf("%v %v", v, err)
	}
	status = http.StatusBadRequest
	_, err = e.Embed(context.Background(), []string{"x"}, Query)
	if pe, ok := err.(*ProviderError); !ok || pe.Status != 400 {
		t.Fatalf("want ProviderError 400, got %v", err)
	}
}

type counting struct{ calls int }

func (c *counting) Model() ModelInfo { return ModelInfo{Provider: "fake", Name: "f", Dims: 1} }
func (c *counting) Embed(_ context.Context, texts []string, _ InputKind) ([][]float32, error) {
	c.calls++
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1}
	}
	return out, nil
}

func TestQueryCache(t *testing.T) {
	inner := &counting{}
	e := Cached(inner, 2)
	ctx := context.Background()
	e.Embed(ctx, []string{"Redis  refused"}, Query)
	e.Embed(ctx, []string{"redis refused"}, Query) // same normalised text
	if inner.calls != 1 {
		t.Fatalf("normalised query not cached: %d calls", inner.calls)
	}
	e.Embed(ctx, []string{"b"}, Query)
	e.Embed(ctx, []string{"c"}, Query) // evicts "redis refused"
	e.Embed(ctx, []string{"redis refused"}, Query)
	if inner.calls != 4 {
		t.Fatalf("LRU eviction: %d calls", inner.calls)
	}
	e.Embed(ctx, []string{"doc"}, Document)
	e.Embed(ctx, []string{"doc"}, Document)
	if inner.calls != 6 {
		t.Fatalf("documents must not be cached: %d calls", inner.calls)
	}
}

func TestLiteral(t *testing.T) {
	if got := Literal([]float32{0.5, -1, float32(math.SmallestNonzeroFloat32)}); got != "[0.5,-1,1e-45]" {
		t.Errorf("literal %s", got)
	}
}
