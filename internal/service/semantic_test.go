package service_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oussissa/amem/internal/domain"
	"github.com/oussissa/amem/internal/embed"
	"github.com/oussissa/amem/internal/search"
)

// conceptEmbedder maps words to concepts, so that French and English
// wordings of the same idea are close without sharing a single term.
type conceptEmbedder struct {
	mu       sync.Mutex
	fail     bool
	docCalls int
}

var concepts = map[string]int{
	"database": 0, "postgresql": 0, "sql": 0, "base": 0, "données": 0,
	"slow": 1, "latency": 1, "lenteur": 1, "lente": 1,
	"stripe": 2, "payment": 2, "paiement": 2, "webhook": 2,
	"deploy": 3, "déploiement": 3,
}

func (c *conceptEmbedder) Model() embed.ModelInfo {
	return embed.ModelInfo{Provider: "fake", Name: "concepts"}
}

func (c *conceptEmbedder) Embed(_ context.Context, texts []string, kind embed.InputKind) ([][]float32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return nil, errors.New("provider down")
	}
	if kind == embed.Document {
		c.docCalls++
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 8)
		v[7] = 0.2 // never a zero vector
		for _, w := range strings.FieldsFunc(strings.ToLower(t), func(r rune) bool { return r == ' ' || r == '\n' || r == '.' || r == ',' }) {
			if d, ok := concepts[w]; ok {
				v[d]++
			}
		}
		out[i] = v
	}
	return out, nil
}

func TestSemanticSearch(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	fake := &conceptEmbedder{}
	if err := s.EnableSemantic(ctx, fake, time.Second); err != nil {
		t.Skip("pgvector not available: ", err)
	}
	mustInit(t, s, "alpha")
	slow := incident("PostgreSQL reports are slow", "report-timeout")
	slow.Abstract, slow.Overview = "Database latency on monthly reports.", "SQL latency comes from a missing index."
	a, err := s.Record(ctx, admin, "alpha", slow)
	if err != nil {
		t.Fatal(err)
	}
	pay := incident("Stripe webhook retries", "stripe-retry")
	pay.Abstract, pay.Overview = "Payment webhook delivered twice.", "Stripe retries the payment webhook."
	b, err := s.Record(ctx, admin, "alpha", pay)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.EmbedPending(ctx); err != nil || n < 2 {
		t.Fatalf("embed pending: %d %v", n, err)
	}

	// No shared term with the English incident: only the semantic channel matches.
	resp, err := s.Search(ctx, admin, search.Request{Project: "alpha", Query: "lenteur de la base de données"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Degraded) != 0 || len(resp.Results) == 0 || resp.Results[0].Key != a.Key {
		t.Fatalf("semantic search: %+v", resp)
	}
	if m := resp.Results[0].Matched; !slices.Contains(m, "semantic") || slices.Contains(m, "lexical") {
		t.Fatalf("matched %v", m)
	}
	for _, r := range resp.Results {
		if r.Key == b.Key {
			t.Fatalf("unrelated incident returned as relevant: %+v", r)
		}
	}

	// RG-09: a near-identical knowledge is reported as a possible duplicate.
	dup := slow
	dup.Keywords = []string{"other-keyword"}
	d, err := s.Record(ctx, admin, "alpha", dup)
	if err != nil || !slices.Contains(d.PossibleDuplicates, a.Key) || slices.Contains(d.PossibleDuplicates, b.Key) {
		t.Fatalf("duplicates: %+v %v", d, err)
	}

	// Provider down: lexical search still answers and says it is degraded (EF-041).
	fake.mu.Lock()
	fake.fail = true
	fake.mu.Unlock()
	resp, err = s.Search(ctx, admin, search.Request{Project: "alpha", Query: "Stripe webhook"})
	if err != nil || !slices.Equal(resp.Degraded, []string{"semantic"}) || len(resp.Results) == 0 || resp.Results[0].Key != b.Key {
		t.Fatalf("degraded search: %+v %v", resp, err)
	}
	if _, err := s.Record(ctx, admin, "alpha", incident("Deploy froze", "deploy-freeze")); err != nil {
		t.Fatalf("writes must not depend on the provider: %v", err)
	}
	fake.mu.Lock()
	fake.fail = false
	fake.mu.Unlock()
	if _, err := s.EmbedPending(ctx); err != nil {
		t.Fatal(err)
	}

	// A rebuild reuses the vectors of unchanged chunks.
	fake.mu.Lock()
	before := fake.docCalls
	fake.mu.Unlock()
	if _, err := s.Rebuild(ctx, admin, "alpha"); err != nil {
		t.Fatal(err)
	}
	n, err := s.EmbedPending(ctx)
	fake.mu.Lock()
	after := fake.docCalls
	fake.mu.Unlock()
	if err != nil || n != 0 || after != before {
		t.Fatalf("rebuild re-embedded %d chunks (%d provider calls)", n, after-before)
	}
	st, err := s.IndexStatus(ctx, admin, "alpha")
	if err != nil || st.PendingChunks != 0 || st.EmbeddedChunks != st.Chunks || st.Dims != 8 {
		t.Fatalf("index status: %+v %v", st, err)
	}

	// Project weights: switching the semantic channel off removes semantic-only hits.
	if err := s.UpdateProject(ctx, admin, "alpha", func(p *domain.Project) error {
		zero := 0.0
		p.Search.Weights.Semantic = &zero
		p.Search.SemanticMinSimilarity = 1.01
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	resp, err = s.Search(ctx, admin, search.Request{Project: "alpha", Query: "lenteur des données"})
	if err != nil || !resp.NoRelevantResult {
		t.Fatalf("semantic channel disabled by project weights: %+v %v", resp, err)
	}
}
