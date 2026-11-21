package pgstore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oussissa/amem/internal/chunk"
	"github.com/oussissa/amem/internal/docstore"
	"github.com/oussissa/amem/internal/domain"
)

// Q is implemented by *pgxpool.Pool and pgx.Tx.
type Q interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Row is the projection of a knowledge needed by the write path.
type Row struct {
	ID, Key, Type, Status, CreatedBy, FilePath, ContentHash, Classification string
	SupersedesKey, SupersededByKey                                           string
	Version                                                                  int
	Deleted                                                                  bool
}

func GetRow(ctx context.Context, q Q, project, key string, forUpdate bool) (*Row, error) {
	sql := `SELECT id, key, type, status, created_by, file_path, content_hash, classification,
	               coalesce(supersedes_key,''), coalesce(superseded_by_key,''), version, deleted_at IS NOT NULL
	        FROM knowledge WHERE project_id=$1 AND (key=$2 OR id=$2)`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	var r Row
	err := q.QueryRow(ctx, sql, project, key).Scan(&r.ID, &r.Key, &r.Type, &r.Status, &r.CreatedBy, &r.FilePath,
		&r.ContentHash, &r.Classification, &r.SupersedesKey, &r.SupersededByKey, &r.Version, &r.Deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NotFound(key)
	}
	return &r, err
}

// NextKey allocates the next key for (project, type, year) (RG-01).
func NextKey(ctx context.Context, tx pgx.Tx, project string, t domain.Type, year int) (string, error) {
	var n int
	err := tx.QueryRow(ctx, `INSERT INTO key_counters (project_id, prefix, year, next) VALUES ($1,$2,$3,2)
		ON CONFLICT (project_id, prefix, year) DO UPDATE SET next = key_counters.next + 1
		RETURNING next - 1`, project, t.Prefix(), year).Scan(&n)
	if err != nil {
		return "", err
	}
	return domain.FormatKey(t, year, n), nil
}

const insertChunkSQL = `INSERT INTO chunks (project_id, knowledge_id, version, ordinal, section, text, token_count, text_hash, tsv)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,
  setweight(to_tsvector('amem_simple',$9),'A') || setweight(to_tsvector('amem_fr',$9),'A') || setweight(to_tsvector('amem_en',$9),'A') ||
  setweight(to_tsvector('amem_simple',$10),'B') || setweight(to_tsvector('amem_fr',$10),'B') || setweight(to_tsvector('amem_en',$10),'B') ||
  setweight(to_tsvector('amem_fr',$11),'C') || setweight(to_tsvector('amem_en',$11),'C') || setweight(to_tsvector('amem_simple',$11),'D'))`

// Project writes the derived projection of a document: knowledge row,
// relations and lexical chunks (spec 02 §6).
func Project(ctx context.Context, tx pgx.Tx, k *domain.Knowledge, rel, hash string) error {
	var deletedAt *time.Time = k.DeletedAt
	kwNorm := strings.ToLower(strings.Join(k.Keywords, " ‖ "))
	_, err := tx.Exec(ctx, `INSERT INTO knowledge (id, project_id, key, type, title, abstract, overview, topics, components,
		technologies, keywords, keywords_norm, status, resolution, severity, confidence, classification, origin, supersedes_key,
		source_refs, version, content_hash, file_path, created_by, updated_by, created_at, updated_at, deleted_at, index_state)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,nullif($14,''),nullif($15,''),$16,$17,$18,nullif($19,''),$20,$21,$22,$23,$24,$25,$26,$27,$28,'lexical')
	ON CONFLICT (id) DO UPDATE SET title=$5, abstract=$6, overview=$7, topics=$8, components=$9, technologies=$10, keywords=$11,
		keywords_norm=$12, status=$13, resolution=nullif($14,''), severity=nullif($15,''), confidence=$16, classification=$17,
		supersedes_key=nullif($19,''), source_refs=$20, version=$21, content_hash=$22, file_path=$23, updated_by=$25,
		updated_at=$27, deleted_at=$28, index_state='lexical'`,
		k.ID, k.ProjectID, k.Key, string(k.Type), k.Title, k.Abstract, k.Overview, nz(k.Topics), nz(k.Components),
		nz(k.Technologies), nz(k.Keywords), kwNorm, string(k.Status), k.Resolution, k.Severity, k.Confidence, k.Classification,
		orDefault(k.Origin, "manual"), k.Supersedes, nz(k.SourceRefs), k.Version, hash, rel, k.CreatedBy, k.UpdatedBy,
		k.CreatedAt, k.UpdatedAt, deletedAt)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM knowledge_relations WHERE source_id=$1`, k.ID); err != nil {
		return err
	}
	for _, r := range k.Relations {
		if _, err := tx.Exec(ctx, `INSERT INTO knowledge_relations (project_id, source_id, type, target_key) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
			k.ProjectID, k.ID, r.Type, r.Target); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM chunks WHERE knowledge_id=$1`, k.ID); err != nil {
		return err
	}
	if k.DeletedAt != nil {
		return nil
	}
	for _, c := range chunk.Split(k) {
		body := ""
		if c.Section != "header" {
			body = c.Text
		}
		if _, err := tx.Exec(ctx, insertChunkSQL, k.ProjectID, k.ID, k.Version, c.Ordinal, c.Section, c.Text,
			chunk.Tokens(c.Text), docstore.Hash([]byte(c.Text)), c.A, c.B, body); err != nil {
			return err
		}
	}
	return nil
}

// SetSupersededBy maintains the reverse pointer used to fold chains (EF-036).
func SetSupersededBy(ctx context.Context, tx pgx.Tx, project, oldKey, newKey string) error {
	_, err := tx.Exec(ctx, `UPDATE knowledge SET superseded_by_key=$3 WHERE project_id=$1 AND key=$2`, project, oldKey, newKey)
	return err
}

// Rebuild replaces the derived data of a project with the content of its files (EF-070).
func Rebuild(ctx context.Context, tx pgx.Tx, store *docstore.Store, project string) (int, error) {
	if err := LockProject(ctx, tx, project); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM knowledge WHERE project_id=$1`, project); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM key_counters WHERE project_id=$1`, project); err != nil {
		return 0, err
	}
	n := 0
	supersedes := map[string]string{}
	err := store.Walk(project, func(rel string, k *domain.Knowledge, hash string) error {
		if k.ProjectID != project {
			return errors.New(rel + ": project_id does not match the project directory")
		}
		n++
		if k.Supersedes != "" && k.Status == domain.Confirmed {
			supersedes[k.Supersedes] = k.Key
		}
		return Project(ctx, tx, k, rel, hash)
	})
	if err != nil {
		return 0, err
	}
	for old, nw := range supersedes {
		if err := SetSupersededBy(ctx, tx, project, old, nw); err != nil {
			return 0, err
		}
	}
	keys, err := store.AllKeys(project)
	if err != nil {
		return 0, err
	}
	for _, key := range keys {
		var prefix string
		var year, num int
		parts := strings.Split(key, "-")
		prefix = parts[0]
		year = atoi(parts[1])
		num = atoi(parts[2])
		if _, err := tx.Exec(ctx, `INSERT INTO key_counters (project_id, prefix, year, next) VALUES ($1,$2,$3,$4)
			ON CONFLICT (project_id, prefix, year) DO UPDATE SET next = greatest(key_counters.next, $4)`,
			project, prefix, year, num+1); err != nil {
			return 0, err
		}
	}
	return n, nil
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

func nz(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
