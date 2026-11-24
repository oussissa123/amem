package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/oussissa/amem/internal/chunk"
	"github.com/oussissa/amem/internal/domain"
	"github.com/oussissa/amem/internal/embed"
	"github.com/oussissa/amem/internal/pgstore"
)

// semantic holds the embeddings side of the service (spec 02 §6.3). It is
// optional: without provider or pgvector, search runs in lexical mode and
// reports degraded ["semantic"] (EF-041).
type semantic struct {
	emb          embed.Embedder
	queryTimeout time.Duration
	wake         chan struct{}

	mu      sync.Mutex
	modelID int
	dims    int
	skip    map[int64]bool // chunks the provider rejects; retried after a restart
}

// EnableSemantic attaches an embeddings provider. It returns an error (and
// leaves the service lexical) when pgvector is not available.
func (s *Service) EnableSemantic(ctx context.Context, e embed.Embedder, queryTimeout time.Duration) error {
	if e == nil {
		return nil
	}
	if err := s.DB.EnsureVector(ctx); err != nil {
		return err
	}
	if queryTimeout <= 0 {
		queryTimeout = time.Second
	}
	sem := &semantic{emb: embed.Cached(e, 10000), queryTimeout: queryTimeout, wake: make(chan struct{}, 1), skip: map[int64]bool{}}
	m := e.Model()
	id, dims, err := pgstore.FindModel(ctx, s.DB.Pool, m.Provider, m.Name)
	if err != nil {
		return err
	}
	if id > 0 && (m.Dims == 0 || m.Dims == dims) {
		sem.modelID, sem.dims = id, dims
	}
	s.sem = sem
	return nil
}

// SemanticEnabled reports whether a provider is attached.
func (s *Service) SemanticEnabled() bool { return s.sem != nil }

// model returns the registered model, registering it once its dimension is known.
func (s *Service) model(ctx context.Context, dims int) (int, error) {
	s.sem.mu.Lock()
	defer s.sem.mu.Unlock()
	if s.sem.modelID > 0 && s.sem.dims == dims {
		return s.sem.modelID, nil
	}
	m := s.sem.emb.Model()
	id, err := pgstore.RegisterModel(ctx, s.DB, m.Provider, m.Name, dims)
	if err != nil {
		return 0, err
	}
	s.sem.modelID, s.sem.dims = id, dims
	return id, nil
}

func (s *Service) currentModel() (id, dims int) {
	s.sem.mu.Lock()
	defer s.sem.mu.Unlock()
	return s.sem.modelID, s.sem.dims
}

// encodeQuery is the only model call on the search path (spec 00 §9.2).
func (s *Service) encodeQuery(ctx context.Context, text string) ([]float32, int, int, error) {
	ctx, cancel := context.WithTimeout(ctx, s.sem.queryTimeout)
	defer cancel()
	vecs, err := s.sem.emb.Embed(ctx, []string{text}, embed.Query)
	if err != nil {
		return nil, 0, 0, err
	}
	id, err := s.model(ctx, len(vecs[0]))
	if err != nil {
		return nil, 0, 0, err
	}
	return vecs[0], id, len(vecs[0]), nil
}

// wakeIndexer tells a running indexer that chunks changed.
func (s *Service) wakeIndexer() {
	if s.sem == nil {
		return
	}
	select {
	case s.sem.wake <- struct{}{}:
	default:
	}
}

// EmbedPending computes the missing embeddings of every project, in batches
// of embed.MaxBatch, until none is left or ctx ends. It returns the number of
// chunks embedded.
func (s *Service) EmbedPending(ctx context.Context) (int, error) {
	if s.sem == nil {
		return 0, domain.Errf(domain.CodeValidation, "no embeddings provider configured (AMEM_EMBEDDINGS_PROVIDER)")
	}
	done := 0
	for {
		modelID, _ := s.currentModel()
		s.sem.mu.Lock()
		skip := make([]int64, 0, len(s.sem.skip))
		for id := range s.sem.skip {
			skip = append(skip, id)
		}
		s.sem.mu.Unlock()
		batch, err := pgstore.PendingChunks(ctx, s.DB.Pool, modelID, embed.MaxBatch, skip)
		if err != nil || len(batch) == 0 {
			return done, err
		}
		n, err := s.embedBatch(ctx, batch)
		done += n
		if err != nil {
			return done, err
		}
	}
}

func (s *Service) embedBatch(ctx context.Context, batch []pgstore.PendingChunk) (int, error) {
	inputs := make([]string, len(batch))
	for i, c := range batch {
		inputs[i] = c.Input
	}
	vecs, err := s.sem.emb.Embed(ctx, inputs, embed.Document)
	if err != nil {
		var pe *embed.ProviderError
		if errors.As(err, &pe) && pe.Status/100 == 4 && pe.Status != 408 && pe.Status != 429 && len(batch) > 1 {
			// One input is refused (too long, invalid): isolate it.
			n := 0
			for i := range batch {
				m, err := s.embedBatch(ctx, batch[i:i+1])
				n += m
				if err != nil && !errors.As(err, &pe) {
					return n, err
				}
			}
			return n, nil
		}
		if errors.As(err, &pe) && pe.Status/100 == 4 && pe.Status != 408 && pe.Status != 429 {
			s.sem.mu.Lock()
			s.sem.skip[batch[0].ID] = true
			s.sem.mu.Unlock()
			s.Log.Warn("embeddings: chunk refused by the provider, skipped", "chunk", batch[0].ID, "error", err)
			return 0, nil
		}
		return 0, err
	}
	modelID, err := s.model(ctx, len(vecs[0]))
	if err != nil {
		return 0, err
	}
	lits := make([]string, len(vecs))
	for i, v := range vecs {
		lits[i] = embed.Literal(v)
	}
	if err := pgstore.SaveEmbeddings(ctx, s.DB, modelID, batch, lits); err != nil {
		return 0, err
	}
	return len(batch), nil
}

// RunIndexer computes embeddings in the background until ctx ends: after
// each write, and every 30 s. A provider failure is retried with an
// exponential backoff capped at 5 minutes; writes and lexical search are
// never blocked (ENF-03).
func (s *Service) RunIndexer(ctx context.Context) {
	if s.sem == nil {
		return
	}
	backoff := time.Duration(0)
	for {
		n, err := s.EmbedPending(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			backoff = min(max(2*backoff, 5*time.Second), 5*time.Minute)
			s.Log.Warn("embeddings: indexing paused", "error", err, "retry_in", backoff.String())
		default:
			backoff = 0
			if n > 0 {
				s.Log.Info("embeddings: chunks indexed", "count", n)
				if pruned, err := pgstore.PruneEmbeddings(ctx, s.DB.Pool); err == nil && pruned > 0 {
					s.Log.Info("embeddings: unused vectors pruned", "count", pruned)
				}
			}
		}
		wait := 30 * time.Second
		if backoff > 0 {
			wait = backoff
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.sem.wake:
			t.Stop()
			if backoff > 0 {
				// The provider was failing: do not hammer it on every write.
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
			}
		case <-t.C:
		}
	}
}

// IndexStatus describes the derived indexes of a project (UC-13).
type IndexStatus struct {
	Project          string `json:"project"`
	Documents        int    `json:"documents"`
	Chunks           int    `json:"chunks"`
	Semantic         bool   `json:"semantic"`
	Model            string `json:"model,omitempty"`
	Dims             int    `json:"dims,omitempty"`
	EmbeddedChunks   int    `json:"embedded_chunks"`
	PendingChunks    int    `json:"pending_chunks"`
	SemanticDisabled string `json:"semantic_disabled,omitempty"`
}

func (s *Service) IndexStatus(ctx context.Context, c *Caller, project string) (*IndexStatus, error) {
	if err := c.require(project, domain.RoleReader); err != nil {
		return nil, err
	}
	st := &IndexStatus{Project: project}
	err := s.DB.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM knowledge WHERE project_id=$1 AND deleted_at IS NULL),
		(SELECT count(*) FROM chunks c JOIN knowledge k ON k.id=c.knowledge_id WHERE c.project_id=$1 AND k.deleted_at IS NULL)`,
		project).Scan(&st.Documents, &st.Chunks)
	if err != nil {
		return nil, err
	}
	if s.sem == nil {
		st.SemanticDisabled = "no embeddings provider configured"
		st.PendingChunks = st.Chunks
		return st, nil
	}
	m := s.sem.emb.Model()
	id, dims := s.currentModel()
	st.Semantic, st.Model, st.Dims = true, fmt.Sprintf("%s/%s", m.Provider, m.Name), dims
	if id > 0 {
		if _, st.EmbeddedChunks, err = pgstore.Coverage(ctx, s.DB.Pool, project, id); err != nil {
			return nil, err
		}
	}
	st.PendingChunks = st.Chunks - st.EmbeddedChunks
	return st, nil
}

func (s *Service) semanticDuplicates(ctx context.Context, k *domain.Knowledge, threshold float64, allowRestricted bool) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*s.sem.queryTimeout)
	defer cancel()
	input := chunk.EmbedInput(string(k.Type), "", k.Title, "header", k.Title+"\n"+k.Abstract+"\n"+k.Overview)
	vecs, err := s.sem.emb.Embed(ctx, []string{input}, embed.Document)
	if err != nil {
		return nil, err
	}
	id, err := s.model(ctx, len(vecs[0]))
	if err != nil {
		return nil, err
	}
	return pgstore.NearHeaders(ctx, s.DB.Pool, k.ProjectID, id, len(vecs[0]), embed.Literal(vecs[0]), threshold, allowRestricted, 3)
}
