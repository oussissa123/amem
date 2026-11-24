// Package embed computes text embeddings through interchangeable providers
// (spec 02 §6.3): any OpenAI-compatible /v1/embeddings endpoint, or Ollama.
package embed

import (
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type InputKind int

const (
	Document InputKind = iota
	Query
)

// ModelInfo identifies a model; Dims is 0 until the first answer is known.
type ModelInfo struct {
	Provider string
	Name     string
	Dims     int
}

type Embedder interface {
	Model() ModelInfo
	Embed(ctx context.Context, texts []string, kind InputKind) ([][]float32, error)
}

// Config is the `embeddings` section (AMEM_EMBEDDINGS_*).
type Config struct {
	Provider  string // none | ollama | openai
	BaseURL   string
	Model     string
	APIKeyEnv string // name of the variable holding the key, never the key
	Dims      int    // optional: requested dimension (OpenAI) / expected dimension
	// Prefixes added to inputs, for models trained with instructions
	// (e.g. "query: " / "passage: " for e5).
	QueryPrefix, DocumentPrefix string
	Timeout                     time.Duration
}

// New returns nil when no provider is configured.
func New(c Config) (Embedder, error) {
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	hc := &http.Client{Timeout: c.Timeout}
	switch strings.ToLower(c.Provider) {
	case "", "none":
		return nil, nil
	case "ollama":
		if c.BaseURL == "" {
			c.BaseURL = "http://localhost:11434"
		}
		if c.Model == "" {
			c.Model = "bge-m3"
		}
		return &ollama{cfg: c, http: hc}, nil
	case "openai":
		if c.BaseURL == "" {
			c.BaseURL = "https://api.openai.com/v1"
		}
		if c.Model == "" {
			return nil, errors.New("embeddings: model is required for the openai provider")
		}
		key := ""
		if c.APIKeyEnv != "" {
			key = os.Getenv(c.APIKeyEnv)
			if key == "" {
				return nil, fmt.Errorf("embeddings: variable %s is empty", c.APIKeyEnv)
			}
		}
		return &openai{cfg: c, key: key, http: hc}, nil
	}
	return nil, fmt.Errorf("embeddings: unknown provider %q (none, ollama, openai)", c.Provider)
}

// MaxBatch is the number of texts sent per provider call.
const MaxBatch = 64

func prefix(c Config, texts []string, kind InputKind) []string {
	p := c.DocumentPrefix
	if kind == Query {
		p = c.QueryPrefix
	}
	if p == "" {
		return texts
	}
	out := make([]string, len(texts))
	for i, t := range texts {
		out[i] = p + t
	}
	return out
}

// ---- OpenAI-compatible

type openai struct {
	cfg  Config
	key  string
	http *http.Client
	mu   sync.Mutex
	dims int
}

func (o *openai) Model() ModelInfo {
	o.mu.Lock()
	defer o.mu.Unlock()
	return ModelInfo{Provider: "openai", Name: o.cfg.Model, Dims: max(o.dims, o.cfg.Dims)}
}

func (o *openai) Embed(ctx context.Context, texts []string, kind InputKind) ([][]float32, error) {
	req := map[string]any{"model": o.cfg.Model, "input": prefix(o.cfg, texts, kind), "encoding_format": "float"}
	if o.cfg.Dims > 0 {
		req["dimensions"] = o.cfg.Dims
	}
	var resp struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := post(ctx, o.http, strings.TrimSuffix(o.cfg.BaseURL, "/")+"/embeddings", o.key, req, &resp); err != nil {
		return nil, err
	}
	out := make([][]float32, len(texts))
	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, errors.New("embeddings: index out of range in response")
		}
		out[d.Index] = d.Embedding
	}
	return o.check(out)
}

func (o *openai) check(out [][]float32) ([][]float32, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return checkDims(out, &o.dims)
}

// ---- Ollama

type ollama struct {
	cfg  Config
	http *http.Client
	mu   sync.Mutex
	dims int
}

func (o *ollama) Model() ModelInfo {
	o.mu.Lock()
	defer o.mu.Unlock()
	return ModelInfo{Provider: "ollama", Name: o.cfg.Model, Dims: max(o.dims, o.cfg.Dims)}
}

func (o *ollama) Embed(ctx context.Context, texts []string, kind InputKind) ([][]float32, error) {
	var resp struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	req := map[string]any{"model": o.cfg.Model, "input": prefix(o.cfg, texts, kind)}
	if err := post(ctx, o.http, strings.TrimSuffix(o.cfg.BaseURL, "/")+"/api/embed", "", req, &resp); err != nil {
		return nil, err
	}
	if len(resp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embeddings: %d vectors for %d inputs", len(resp.Embeddings), len(texts))
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return checkDims(resp.Embeddings, &o.dims)
}

// checkDims rejects empty or inconsistent vectors and normalises them so
// that cosine distance is stable across providers.
func checkDims(out [][]float32, dims *int) ([][]float32, error) {
	for _, v := range out {
		if len(v) == 0 {
			return nil, errors.New("embeddings: missing vector in response")
		}
		if *dims == 0 {
			*dims = len(v)
		}
		if len(v) != *dims {
			return nil, fmt.Errorf("embeddings: dimension changed from %d to %d", *dims, len(v))
		}
		normalize(v)
	}
	return out, nil
}

func normalize(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return
	}
	n := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= n
	}
}

// ProviderError is returned when the provider answers with an HTTP error.
type ProviderError struct {
	Status int
	Msg    string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("embeddings provider: HTTP %d: %s", e.Status, e.Msg)
}

func post(ctx context.Context, hc *http.Client, url, key string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("embeddings provider unreachable: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &ProviderError{Status: resp.StatusCode, Msg: msg}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("embeddings provider: invalid response: %w", err)
	}
	return nil
}

// ---- query cache

// Cached wraps an embedder with an LRU of query embeddings (spec 02 §6.3:
// 10 000 entries, key = model + normalised text). Documents are not cached.
func Cached(e Embedder, size int) Embedder {
	if e == nil {
		return nil
	}
	return &cached{Embedder: e, size: size, ll: list.New(), m: map[string]*list.Element{}}
}

type cached struct {
	Embedder
	size int
	mu   sync.Mutex
	ll   *list.List
	m    map[string]*list.Element
}

type entry struct {
	key string
	vec []float32
}

func (c *cached) Embed(ctx context.Context, texts []string, kind InputKind) ([][]float32, error) {
	if kind != Query || len(texts) != 1 {
		return c.Embedder.Embed(ctx, texts, kind)
	}
	m := c.Model()
	key := m.Provider + "/" + m.Name + "\x00" + strings.Join(strings.Fields(strings.ToLower(texts[0])), " ")
	c.mu.Lock()
	if el, ok := c.m[key]; ok {
		c.ll.MoveToFront(el)
		v := el.Value.(*entry).vec
		c.mu.Unlock()
		return [][]float32{v}, nil
	}
	c.mu.Unlock()
	out, err := c.Embedder.Embed(ctx, texts, kind)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[key]; !ok {
		c.m[key] = c.ll.PushFront(&entry{key, out[0]})
		if c.ll.Len() > c.size {
			last := c.ll.Back()
			c.ll.Remove(last)
			delete(c.m, last.Value.(*entry).key)
		}
	}
	return out, nil
}

// Literal renders a vector in pgvector's text format.
func Literal(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 10)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%g", x)
	}
	b.WriteByte(']')
	return b.String()
}
