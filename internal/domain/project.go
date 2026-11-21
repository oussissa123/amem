package domain

import (
	"net/url"
	"strings"
)

const SchemaProject = "amem/project/v1"

// Project is the unit of memory (spec 00 §4.5), stored as project.yaml.
type Project struct {
	Schema       string        `yaml:"schema" json:"-"`
	ID           string        `yaml:"id" json:"id"`
	Name         string        `yaml:"name" json:"name"`
	Description  string        `yaml:"description,omitempty" json:"description,omitempty"`
	Repositories []Repository  `yaml:"repositories,omitempty" json:"repositories,omitempty"`
	ExternalRefs []ExternalRef `yaml:"external_refs,omitempty" json:"external_refs,omitempty"`
	Languages    []string      `yaml:"languages,omitempty" json:"-"`
	Topics       map[string]TopicDef `yaml:"topics,omitempty" json:"-"`
	Governance   Governance    `yaml:"governance,omitempty" json:"-"`
	Privacy      Privacy       `yaml:"privacy,omitempty" json:"-"`
	Retention    Retention     `yaml:"retention,omitempty" json:"-"`
}

type Repository struct {
	URL       string `yaml:"url" json:"url"`
	Role      string `yaml:"role,omitempty" json:"role,omitempty"`
	LocalPath string `yaml:"local_path,omitempty" json:"local_path,omitempty"`
}

type ExternalRef struct {
	System    string `yaml:"system" json:"system"`
	Workspace string `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Project   string `yaml:"project" json:"project"`
}

// Ref is the lookup form stored in project_external_refs.ref.
func (e ExternalRef) Ref() string {
	if e.Workspace == "" {
		return e.Project
	}
	return e.Workspace + "/" + e.Project
}

type TopicDef struct {
	Aliases []string `yaml:"aliases,omitempty"`
}

type Governance struct {
	AgentSelfConfirm bool `yaml:"agent_self_confirm"`
}

type Privacy struct {
	ExcludePaths []string `yaml:"exclude_paths,omitempty"`
	DenyPatterns []string `yaml:"deny_patterns,omitempty"`
}

type Retention struct {
	SessionsDays int `yaml:"sessions_days,omitempty"`
}

func NewProject(id, name string) *Project {
	if name == "" {
		name = id
	}
	return &Project{
		Schema:    SchemaProject,
		ID:        id,
		Name:      name,
		Languages: []string{"fr", "en"},
		Privacy: Privacy{
			ExcludePaths: []string{"**/.env*", "**/secrets/**", "**/*.pem"},
		},
		Retention: Retention{SessionsDays: 30},
	}
}

// TopicAlias maps an alias (or the topic itself) to the canonical topic.
func (p *Project) TopicAlias(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	for name, def := range p.Topics {
		if name == t {
			return name
		}
		for _, a := range def.Aliases {
			if strings.ToLower(a) == t {
				return name
			}
		}
	}
	return t
}

// NormalizeRemote turns any Git remote URL into host/path (EF-005):
// git@github.com:acme/api.git, https://user@github.com:443/acme/api/ → github.com/acme/api.
func NormalizeRemote(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		// scp-like syntax: [user@]host:path
		if at := strings.Index(s, "@"); at >= 0 && at < strings.Index(s+":", ":") {
			s = s[at+1:]
		}
		if host, path, ok := strings.Cut(s, ":"); ok && !strings.Contains(host, "/") {
			s = "ssh://" + host + "/" + path
		} else {
			s = "file://" + s
		}
	}
	u, err := url.Parse(s)
	if err != nil {
		return strings.ToLower(raw)
	}
	// Hosting services treat owner/repository case-insensitively.
	path := strings.ToLower(strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git"))
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return path
	}
	return host + "/" + path
}
