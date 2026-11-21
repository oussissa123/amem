// Package pgstore holds the PostgreSQL side: derived index tables and
// operational data (spec 02 §5.1). Every query on project data takes the
// project id explicitly (ENF-05).
package pgstore

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type DB struct {
	Pool *pgxpool.Pool
}

func Open(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("database url: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database unreachable: %w", err)
	}
	db := &DB{Pool: pool}
	if err := db.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) Close() { db.Pool.Close() }

// migrate applies embedded migrations in order, under an advisory lock so
// several amem processes can start at once.
func (db *DB) migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('amem:migrate'))`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return err
		}
		names, err := fs.Glob(migrationsFS, "migrations/*.sql")
		if err != nil {
			return err
		}
		sort.Strings(names)
		for _, name := range names {
			var done bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name=$1)`, name).Scan(&done); err != nil {
				return err
			}
			if done {
				continue
			}
			sql, err := migrationsFS.ReadFile(name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return fmt.Errorf("migration %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
				return err
			}
		}
		return nil
	})
}

// LockKnowledge serialises writers of one document across processes (spec 02 §5.2).
func LockKnowledge(ctx context.Context, tx pgx.Tx, project, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('amem:' || $1 || ':' || $2))`, project, key)
	return err
}

func LockProject(ctx context.Context, tx pgx.Tx, project string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('amem:project:' || $1))`, project)
	return err
}

// Audit appends to the audit log; details never carry document content.
func Audit(ctx context.Context, tx pgx.Tx, project, principal, action, target string, version int, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	var v any
	if version > 0 {
		v = version
	}
	var p any
	if project != "" {
		p = project
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit_log (project_id, principal_id, action, target, version, details) VALUES ($1,$2,$3,$4,$5,$6)`,
		p, principal, action, target, v, details)
	return err
}

func joinLower(xs []string) string { return strings.ToLower(strings.Join(xs, " ‖ ")) }
