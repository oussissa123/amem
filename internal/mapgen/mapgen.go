// Package mapgen renders the L0 project map within its token budget (spec 02 §8).
package mapgen

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oussissa/amem/internal/chunk"
	"github.com/oussissa/amem/internal/domain"
	"github.com/oussissa/amem/internal/pgstore"
)

const (
	TargetTokens = 1500
	MaxTokens    = 2000
)

type section struct {
	title    string
	intro    string
	items    []string
	priority int // lower is dropped first
}

// Generate builds PROJECT_MAP.md from metadata only (deterministic).
func Generate(ctx context.Context, q pgstore.Q, p *domain.Project, now time.Time) (string, int, error) {
	const visible = `project_id=$1 AND deleted_at IS NULL AND classification='internal' AND status NOT IN ('obsolete','superseded','rejected')`

	var total int
	counts := map[string]int{}
	rows, err := q.Query(ctx, `SELECT type, count(*) FROM knowledge WHERE `+visible+` GROUP BY type ORDER BY type`, p.ID)
	if err != nil {
		return "", 0, err
	}
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			rows.Close()
			return "", 0, err
		}
		counts[t], total = n, total+n
	}
	rows.Close()

	list := func(sql string, format func(vals []any) string) ([]string, error) {
		rows, err := q.Query(ctx, sql, p.ID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				return nil, err
			}
			out = append(out, format(vals))
		}
		return out, rows.Err()
	}
	countFmt := func(v []any) string { return fmt.Sprintf("%v (%v)", v[0], v[1]) }
	entryFmt := func(v []any) string { return fmt.Sprintf("`%v` %v — %v", v[0], v[1], v[2]) }

	incidents, err := list(`SELECT key, title || ' [' || severity || ', ' || resolution || ']', abstract FROM knowledge
		WHERE `+visible+` AND type='incident' AND resolution IN ('open','mitigated') AND severity IN ('high','critical')
		ORDER BY severity='critical' DESC, updated_at DESC LIMIT 15`, entryFmt)
	if err != nil {
		return "", 0, err
	}
	conventions, err := list(`SELECT key, title, abstract FROM knowledge WHERE `+visible+` AND type='convention' AND status='confirmed'
		ORDER BY updated_at DESC LIMIT 20`, entryFmt)
	if err != nil {
		return "", 0, err
	}
	decisions, err := list(`SELECT key, title, abstract FROM knowledge WHERE `+visible+` AND type='decision' AND status='confirmed'
		ORDER BY ('structurant' = ANY(topics)) DESC, updated_at DESC LIMIT 10`, entryFmt)
	if err != nil {
		return "", 0, err
	}
	components, err := list(`SELECT c, count(*) FROM knowledge, unnest(components) c WHERE `+visible+`
		GROUP BY c ORDER BY count(*) DESC, c LIMIT 15`, countFmt)
	if err != nil {
		return "", 0, err
	}
	technologies, err := list(`SELECT t, count(*) FROM knowledge, unnest(technologies) t WHERE `+visible+`
		GROUP BY t ORDER BY count(*) DESC, t LIMIT 15`, countFmt)
	if err != nil {
		return "", 0, err
	}
	topics, err := list(`SELECT t, count(*) FROM knowledge, unnest(topics) t WHERE `+visible+`
		GROUP BY t ORDER BY count(*) DESC, t LIMIT 30`, countFmt)
	if err != nil {
		return "", 0, err
	}

	sections := []*section{
		{title: "Open critical incidents", items: incidents, priority: 6},
		{title: "Conventions", items: conventions, priority: 5},
		{title: "Key decisions", items: decisions, priority: 4},
		{title: "Main components", items: components, priority: 3},
		{title: "Technologies", items: technologies, priority: 2},
		{title: "Topics", intro: "Use them as `filters.topics` in memory_search.", items: topics, priority: 1},
	}

	render := func() string {
		var b strings.Builder
		fmt.Fprintf(&b, "# Project map — %s\n\n", p.Name)
		if p.Description != "" {
			b.WriteString(strings.TrimSpace(p.Description) + "\n\n")
		}
		if len(p.Repositories) > 0 {
			var repos []string
			for _, r := range p.Repositories {
				s := domain.NormalizeRemote(r.URL)
				if r.Role != "" {
					s += " (" + r.Role + ")"
				}
				repos = append(repos, s)
			}
			b.WriteString("Repositories: " + strings.Join(repos, ", ") + "\n\n")
		}
		if total == 0 {
			b.WriteString("The memory of this project is empty. Record what you learn with `memory_record` " +
				"(incidents, decisions, conventions, pitfalls), citing your sources.\n\n")
		} else {
			var parts []string
			for _, t := range domain.AllTypes() {
				if n := counts[string(t)]; n > 0 {
					parts = append(parts, fmt.Sprintf("%d %s", n, t))
				}
			}
			fmt.Fprintf(&b, "%d active knowledge items: %s.\n\n", total, strings.Join(parts, ", "))
		}
		for _, s := range sections {
			if len(s.items) == 0 {
				continue
			}
			fmt.Fprintf(&b, "## %s\n", s.title)
			if s.intro != "" {
				b.WriteString(s.intro + "\n")
			}
			for _, it := range s.items {
				b.WriteString("- " + it + "\n")
			}
			b.WriteString("\n")
		}
		b.WriteString("---\nUse `memory_search` for any question (exact error messages work best), `memory_read <key>` for details. ")
		fmt.Fprintf(&b, "Generated %s.\n", now.UTC().Format(time.RFC3339))
		return b.String()
	}

	out := render()
	for chunk.Tokens(out) > TargetTokens {
		var victim *section
		for _, s := range sections {
			if len(s.items) > 0 && (victim == nil || s.priority < victim.priority) {
				victim = s
			}
		}
		if victim == nil {
			break
		}
		victim.items = victim.items[:len(victim.items)-1]
		out = render()
	}
	if t := chunk.Tokens(out); t > MaxTokens {
		r := []rune(out)
		out = string(r[:MaxTokens*7/2]) + "\n…\n"
	}
	return out, chunk.Tokens(out), nil
}
