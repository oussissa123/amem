package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/oussissa/amem/internal/chunk"
	"github.com/oussissa/amem/internal/docstore"
)

// hnswMaxDims is the largest dimension pgvector can index with HNSW on the
// vector type; larger models are searched exactly.
const hnswMaxDims = 2000

// EnsureVector installs pgvector and the embeddings table. It fails when the
// extension is not available on the server; callers then stay lexical.
func (db *DB) EnsureVector(ctx context.Context) error {
	return pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('amem:migrate'))`); err != nil {
			return err
		}
		var available bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name='vector')`).Scan(&available); err != nil {
			return err
		}
		if !available {
			return errors.New("the PostgreSQL extension pgvector is not installed on the server (use the pgvector/pgvector image)")
		}
		_, err := tx.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector;
			CREATE TABLE IF NOT EXISTS embeddings (
			  model_id   smallint NOT NULL REFERENCES embedding_models(id) ON DELETE CASCADE,
			  hash       text NOT NULL,
			  embedding  vector NOT NULL,
			  created_at timestamptz NOT NULL DEFAULT now(),
			  PRIMARY KEY (model_id, hash)
			)`)
		return err
	})
}

// RegisterModel returns the id of (provider, name, dims), creating the row
// and its HNSW index (m=16, ef_construction=64) on first use.
func RegisterModel(ctx context.Context, db *DB, provider, name string, dims int) (int, error) {
	var id int
	err := pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('amem:migrate'))`); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `INSERT INTO embedding_models (provider, name, dims) VALUES ($1,$2,$3)
			ON CONFLICT (provider, name, dims) DO UPDATE SET provider=excluded.provider RETURNING id`, provider, name, dims).Scan(&id)
		if err != nil {
			return err
		}
		if dims > hnswMaxDims {
			return nil
		}
		_, err = tx.Exec(ctx, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS embeddings_hnsw_m%d ON embeddings
			USING hnsw ((embedding::vector(%d)) vector_cosine_ops) WITH (m = 16, ef_construction = 64) WHERE model_id = %d`, id, dims, id))
		return err
	})
	return id, err
}

// FindModel returns the most recent registration of a model, whatever its dimension.
func FindModel(ctx context.Context, q Q, provider, name string) (id, dims int, err error) {
	err = q.QueryRow(ctx, `SELECT id, dims FROM embedding_models WHERE provider=$1 AND name=$2 ORDER BY id DESC LIMIT 1`,
		provider, name).Scan(&id, &dims)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	return id, dims, err
}

// PendingChunk is a chunk whose embedding input has no vector for a model.
type PendingChunk struct {
	ID    int64
	Hash  string // current embed_hash
	Input string
}

// InputHash is the content address of an embedding input.
func InputHash(input string) string { return docstore.Hash([]byte(input)) }

// PendingChunks lists up to limit chunks lacking an embedding for modelID
// (0: model not registered yet, every chunk is pending), across projects,
// except the chunk ids in skip.
func PendingChunks(ctx context.Context, q Q, modelID, limit int, skip []int64) ([]PendingChunk, error) {
	rows, err := q.Query(ctx, `SELECT c.id, c.embed_hash, k.type, k.key, k.title, c.section, c.text
		FROM chunks c JOIN knowledge k ON k.id = c.knowledge_id
		WHERE k.deleted_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.model_id = $1 AND e.hash = c.embed_hash)
		  AND c.id <> ALL($3)
		ORDER BY c.id LIMIT $2`, modelID, limit, skip)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingChunk
	for rows.Next() {
		var p PendingChunk
		var typ, key, title, section, text string
		if err := rows.Scan(&p.ID, &p.Hash, &typ, &key, &title, &section, &text); err != nil {
			return nil, err
		}
		p.Input = chunk.EmbedInput(typ, key, title, section, text)
		out = append(out, p)
	}
	return out, rows.Err()
}

// SaveEmbeddings stores vectors (pgvector text literals) for chunks and
// fixes their embed_hash when it was computed by an older version.
func SaveEmbeddings(ctx context.Context, db *DB, modelID int, chunks []PendingChunk, vectors []string) error {
	return pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		for i, c := range chunks {
			h := InputHash(c.Input)
			if _, err := tx.Exec(ctx, `INSERT INTO embeddings (model_id, hash, embedding) VALUES ($1,$2,$3::text::vector)
				ON CONFLICT (model_id, hash) DO NOTHING`, modelID, h, vectors[i]); err != nil {
				return err
			}
			if h != c.Hash {
				if _, err := tx.Exec(ctx, `UPDATE chunks SET embed_hash=$2 WHERE id=$1`, c.ID, h); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// PruneEmbeddings deletes vectors no chunk refers to any more.
func PruneEmbeddings(ctx context.Context, q Q) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM embeddings e WHERE NOT EXISTS (SELECT 1 FROM chunks c WHERE c.embed_hash = e.hash)`)
	return tag.RowsAffected(), err
}

// Coverage counts the chunks of a project and those embedded with modelID.
func Coverage(ctx context.Context, q Q, project string, modelID int) (total, embedded int, err error) {
	err = q.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE EXISTS
		  (SELECT 1 FROM embeddings e WHERE e.model_id = $2 AND e.hash = c.embed_hash))
		FROM chunks c JOIN knowledge k ON k.id = c.knowledge_id
		WHERE c.project_id = $1 AND k.deleted_at IS NULL`, project, modelID).Scan(&total, &embedded)
	return total, embedded, err
}

// NearHeaders returns the keys of the knowledge whose header chunk has a
// cosine similarity ≥ threshold with vec (RG-09). Obsolete, superseded and
// rejected knowledge is ignored.
func NearHeaders(ctx context.Context, q Q, project string, modelID, dims int, vec string, threshold float64, allowRestricted bool, limit int) ([]string, error) {
	rows, err := q.Query(ctx, fmt.Sprintf(`SELECT k.key FROM (
		  SELECT c.knowledge_id, (e.embedding::vector(%d) <=> $2::text::vector(%d)) AS dist
		  FROM embeddings e JOIN chunks c ON c.embed_hash = e.hash
		  WHERE e.model_id = %d AND c.project_id = $1 AND c.section = 'header'
		) n JOIN knowledge k ON k.id = n.knowledge_id
		WHERE k.deleted_at IS NULL AND k.status NOT IN ('obsolete','superseded','rejected')
		  AND ($4 OR k.classification = 'internal') AND 1 - n.dist >= $3
		ORDER BY n.dist LIMIT $5`, dims, dims, modelID), project, vec, threshold, allowRestricted, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}
