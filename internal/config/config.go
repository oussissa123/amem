// Package config reads the process configuration (AMEM_* variables) and
// builds the local caller (spec 02 §14.1, §15).
package config

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/oussissa/amem/internal/domain"
	"github.com/oussissa/amem/internal/service"
)

const DefaultDatabaseURL = "postgres://amem:amem@127.0.0.1:5433/amem?sslmode=disable"

type Config struct {
	DatabaseURL string
	Root        string
	Listen      string
	Project     string
}

func Load() Config {
	c := Config{
		DatabaseURL: env("AMEM_DATABASE_URL", DefaultDatabaseURL),
		Root:        env("AMEM_ROOT", ""),
		Listen:      env("AMEM_LISTEN", "127.0.0.1:7420"),
		Project:     os.Getenv("AMEM_PROJECT"),
	}
	if c.Root == "" {
		base := os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".local", "share")
		}
		c.Root = filepath.Join(base, "amem", "projects")
	}
	return c
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// orchestratorVars are recorded in the audit log when present.
var orchestratorVars = []string{"MULTICA_TASK_ID", "MULTICA_AGENT_NAME", "MULTICA_AGENT_ID", "MULTICA_WORKSPACE_ID"}

// LocalCaller builds the identity of a process running without token.
//
// An agent gets the contributor role so that RG-04 (no self-confirmation)
// also holds locally; a human at the terminal gets admin on their machine.
// The process is treated as an agent when AMEM_PRINCIPAL says so, or when it
// runs under a known agent (Multica task, Claude Code, Codex): an agent
// calling the CLI through its shell must not gain the human role.
func LocalCaller(project string, forceAgent bool) *service.Caller {
	host, _ := os.Hostname()
	principal := os.Getenv("AMEM_PRINCIPAL")
	if principal == "" {
		switch {
		case os.Getenv("MULTICA_AGENT_NAME") != "":
			principal = "agent:multica/" + slug(os.Getenv("MULTICA_AGENT_NAME"))
		case os.Getenv("MULTICA_TASK_ID") != "":
			principal = "agent:multica@" + host
		case os.Getenv("CLAUDECODE") != "":
			principal = "agent:claude-code@" + host
		case os.Getenv("CODEX_SANDBOX") != "" || os.Getenv("CODEX_SANDBOX_NETWORK_DISABLED") != "":
			principal = "agent:codex@" + host
		case forceAgent:
			principal = "agent:mcp@" + host
		default:
			name := "unknown"
			if u, err := user.Current(); err == nil {
				name = u.Username
			}
			principal = "human:" + name
		}
	}
	role := domain.RoleAdmin
	if strings.HasPrefix(principal, "agent:") {
		role = domain.RoleContributor
	}
	if r, err := domain.ParseRole(os.Getenv("AMEM_LOCAL_ROLE")); err == nil {
		role = r
	}
	cwd, _ := os.Getwd()
	c := &service.Caller{Principal: principal, LocalRole: role, Configured: project, Cwd: cwd,
		ExternalRef: os.Getenv("AMEM_EXTERNAL_REF")}
	for _, k := range orchestratorVars {
		if v := os.Getenv(k); v != "" {
			if c.Orchestrator == nil {
				c.Orchestrator = map[string]string{}
			}
			c.Orchestrator[k] = v
		}
	}
	return c
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// Slug is exported for principal names built by adapters.
func Slug(s string) string { return slug(s) }
