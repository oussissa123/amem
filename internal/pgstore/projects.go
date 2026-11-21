package pgstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/oklog/ulid/v2"

	"github.com/oussissa/amem/internal/domain"
)

type ProjectRow struct {
	ID, Name, RootPath, ConfigHash string
}

// UpsertProject records a project and the projection of its project.yaml
// (repositories, external references).
func UpsertProject(ctx context.Context, tx pgx.Tx, p *domain.Project, root, configHash string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO projects (id, name, root_path, config_hash) VALUES ($1,$2,$3,$4)
		ON CONFLICT (id) DO UPDATE SET name=$2, root_path=$3, config_hash=$4`, p.ID, p.Name, root, configHash); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM project_repositories WHERE project_id=$1`, p.ID); err != nil {
		return err
	}
	for _, r := range p.Repositories {
		norm := domain.NormalizeRemote(r.URL)
		if norm == "" {
			continue
		}
		var owner string
		err := tx.QueryRow(ctx, `SELECT project_id FROM project_repositories WHERE remote_norm=$1 AND project_id<>$2`, norm, p.ID).Scan(&owner)
		if err == nil {
			e := domain.Errf(domain.CodeRepositoryTaken, "repository "+norm+" already belongs to project "+owner)
			e.Details = map[string]any{"repository": norm, "project": owner}
			return e
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO project_repositories (project_id, remote_norm, role) VALUES ($1,$2,nullif($3,''))
			ON CONFLICT (project_id, remote_norm) DO NOTHING`, p.ID, norm, r.Role)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.Errf(domain.CodeRepositoryTaken, "repository "+norm+" was attached to another project concurrently")
		} else if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM project_external_refs WHERE project_id=$1`, p.ID); err != nil {
		return err
	}
	for _, e := range p.ExternalRefs {
		if _, err := tx.Exec(ctx, `INSERT INTO project_external_refs (project_id, system, ref) VALUES ($1,$2,$3)
			ON CONFLICT (system, ref) DO UPDATE SET project_id=$1`, p.ID, e.System, e.Ref()); err != nil {
			return err
		}
	}
	return nil
}

func GetProject(ctx context.Context, q Q, id string) (*ProjectRow, error) {
	var p ProjectRow
	err := q.QueryRow(ctx, `SELECT id, name, root_path, config_hash FROM projects WHERE id=$1 AND archived_at IS NULL`, id).
		Scan(&p.ID, &p.Name, &p.RootPath, &p.ConfigHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NotFound("project " + id)
	}
	return &p, err
}

func ListProjects(ctx context.Context, q Q) ([]ProjectRow, error) {
	rows, err := q.Query(ctx, `SELECT id, name, root_path, config_hash FROM projects WHERE archived_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ProjectRow, error) {
		var p ProjectRow
		err := r.Scan(&p.ID, &p.Name, &p.RootPath, &p.ConfigHash)
		return p, err
	})
}

// ProjectsForRemotes maps normalized remotes to their projects.
func ProjectsForRemotes(ctx context.Context, q Q, remotes []string) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT remote_norm, project_id FROM project_repositories WHERE remote_norm = ANY($1)`, remotes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var r, p string
		if err := rows.Scan(&r, &p); err != nil {
			return nil, err
		}
		out[r] = p
	}
	return out, rows.Err()
}

func ProjectForExternalRef(ctx context.Context, q Q, system, ref string) (string, error) {
	var p string
	err := q.QueryRow(ctx, `SELECT project_id FROM project_external_refs WHERE system=$1 AND ref=$2`, system, ref).Scan(&p)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return p, err
}

// ---- principals, tokens, grants (spec 02 §14.1)

func EnsurePrincipal(ctx context.Context, q Q, id, kind string) error {
	_, err := q.Exec(ctx, `INSERT INTO principals (id, kind, display) VALUES ($1,$2,$1) ON CONFLICT (id) DO NOTHING`, id, kind)
	return err
}

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// CreateToken issues a token; only its SHA-256 is stored.
func CreateToken(ctx context.Context, q Q, principal string, ttl time.Duration) (string, error) {
	b := make([]byte, 32)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(62))
		if err != nil {
			return "", err
		}
		b[i] = base62[n.Int64()]
	}
	token := "amem_" + string(b)
	sum := sha256.Sum256([]byte(token))
	var exp any
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	_, err := q.Exec(ctx, `INSERT INTO api_tokens (id, principal_id, prefix, hash, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		ulid.Make().String(), principal, token[:13], sum[:], exp)
	return token, err
}

// Authenticate returns the principal of a valid token.
func Authenticate(ctx context.Context, q Q, token string) (string, error) {
	sum := sha256.Sum256([]byte(token))
	var principal string
	err := q.QueryRow(ctx, `UPDATE api_tokens t SET last_used_at=now()
		FROM principals p
		WHERE t.hash=$1 AND t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at > now())
		  AND p.id=t.principal_id AND p.disabled_at IS NULL
		RETURNING t.principal_id`, sum[:]).Scan(&principal)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.Errf(domain.CodeUnauthenticated, "invalid, expired or revoked token")
	}
	return principal, err
}

func Grant(ctx context.Context, q Q, principal, project string, role domain.Role) error {
	_, err := q.Exec(ctx, `INSERT INTO grants (principal_id, project_id, role) VALUES ($1,$2,$3)
		ON CONFLICT (principal_id, project_id) DO UPDATE SET role=$3`, principal, project, role.String())
	return err
}

// Grants returns project → role for a principal.
func Grants(ctx context.Context, q Q, principal string) (map[string]domain.Role, error) {
	rows, err := q.Query(ctx, `SELECT g.project_id, g.role FROM grants g JOIN projects p ON p.id=g.project_id
		WHERE g.principal_id=$1 AND p.archived_at IS NULL`, principal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]domain.Role{}
	for rows.Next() {
		var p, r string
		if err := rows.Scan(&p, &r); err != nil {
			return nil, err
		}
		role, err := domain.ParseRole(r)
		if err != nil {
			return nil, err
		}
		out[p] = role
	}
	return out, rows.Err()
}
