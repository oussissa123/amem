// Package domain holds the knowledge model and its rules (spec 00 §6, spec 01 §7).
// It has no dependency on storage or transport.
package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

const SchemaKnowledge = "amem/knowledge/v1"

type Type string

const (
	Incident     Type = "incident"
	Decision     Type = "decision"
	Architecture Type = "architecture"
	Component    Type = "component"
	Convention   Type = "convention"
	Lesson       Type = "lesson"
	Procedure    Type = "procedure"
	Note         Type = "note"
)

var types = map[Type]struct{ prefix, dir string }{
	Incident:     {"INC", "incidents"},
	Decision:     {"DEC", "decisions"},
	Architecture: {"ARC", "architecture"},
	Component:    {"CMP", "components"},
	Convention:   {"CNV", "conventions"},
	Lesson:       {"LSN", "lessons"},
	Procedure:    {"PRC", "procedures"},
	Note:         {"NOTE", "notes"},
}

func (t Type) Valid() bool   { _, ok := types[t]; return ok }
func (t Type) Prefix() string { return types[t].prefix }
func (t Type) Dir() string    { return types[t].dir }

func AllTypes() []Type {
	return []Type{Incident, Decision, Architecture, Component, Convention, Lesson, Procedure, Note}
}

// TypeForPrefix returns the type whose key prefix is p.
func TypeForPrefix(p string) (Type, bool) {
	for t, v := range types {
		if v.prefix == p {
			return t, true
		}
	}
	return "", false
}

type Status string

const (
	Proposed   Status = "proposed"
	Confirmed  Status = "confirmed"
	Disputed   Status = "disputed"
	Obsolete   Status = "obsolete"
	Superseded Status = "superseded"
	Rejected   Status = "rejected"
)

// HiddenByDefault are statuses excluded from search unless requested (EF-035).
var HiddenByDefault = []Status{Obsolete, Superseded, Rejected}

func (s Status) Valid() bool {
	return slices.Contains([]Status{Proposed, Confirmed, Disputed, Obsolete, Superseded, Rejected}, s)
}

var (
	confidences     = []string{"low", "medium", "high"}
	resolutions     = []string{"open", "mitigated", "resolved", "wont_fix"}
	severities      = []string{"low", "medium", "high", "critical"}
	classifications = []string{"internal", "restricted"}
	origins         = []string{"manual", "agent", "extracted", "imported"}
	claimKinds      = []string{"observed", "hypothesis", "proposal", "conclusion"}
	relationTypes   = []string{"relates_to", "caused_by", "fixes", "duplicates", "contradicts", "depends_on"}
	sourceSchemes   = []string{"git", "file", "ticket", "session", "url", "log"}
)

type Claim struct {
	Text       string   `yaml:"text" json:"text"`
	Kind       string   `yaml:"kind" json:"kind"`
	SourceRefs []string `yaml:"source_refs,omitempty" json:"source_refs,omitempty"`
}

type Relation struct {
	Type   string `yaml:"type" json:"type"`
	Target string `yaml:"target" json:"target"`
}

// Knowledge is one durable unit of project memory. The YAML tags define the
// on-disk front matter (spec 02 §4.1); Body is the Markdown after it.
type Knowledge struct {
	Schema         string     `yaml:"schema" json:"-"`
	ID             string     `yaml:"id" json:"id"`
	Key            string     `yaml:"key" json:"key"`
	ProjectID      string     `yaml:"project_id" json:"project"`
	Type           Type       `yaml:"type" json:"type"`
	Title          string     `yaml:"title" json:"title"`
	Abstract       string     `yaml:"abstract" json:"abstract"`
	Overview       string     `yaml:"overview" json:"overview"`
	Topics         []string   `yaml:"topics,omitempty" json:"topics,omitempty"`
	Components     []string   `yaml:"components,omitempty" json:"components,omitempty"`
	Technologies   []string   `yaml:"technologies,omitempty" json:"technologies,omitempty"`
	Keywords       []string   `yaml:"keywords,omitempty" json:"keywords,omitempty"`
	Status         Status     `yaml:"status" json:"status"`
	Resolution     string     `yaml:"resolution,omitempty" json:"resolution,omitempty"`
	Severity       string     `yaml:"severity,omitempty" json:"severity,omitempty"`
	Confidence     string     `yaml:"confidence" json:"confidence"`
	ConfidenceNote string     `yaml:"confidence_note,omitempty" json:"confidence_note,omitempty"`
	Claims         []Claim    `yaml:"claims,omitempty" json:"claims,omitempty"`
	SourceRefs     []string   `yaml:"source_refs,omitempty" json:"source_refs,omitempty"`
	Relations      []Relation `yaml:"relations,omitempty" json:"relations,omitempty"`
	Supersedes     string     `yaml:"supersedes,omitempty" json:"supersedes,omitempty"`
	Classification string     `yaml:"classification" json:"classification"`
	Origin         string     `yaml:"origin" json:"origin"`
	CreatedBy      string     `yaml:"created_by" json:"created_by"`
	UpdatedBy      string     `yaml:"updated_by" json:"updated_by"`
	CreatedAt      time.Time  `yaml:"created_at" json:"created_at"`
	UpdatedAt      time.Time  `yaml:"updated_at" json:"updated_at"`
	Version        int        `yaml:"version" json:"version"`
	StatusReason   string     `yaml:"status_reason,omitempty" json:"status_reason,omitempty"`
	DeletedAt      *time.Time `yaml:"deleted_at,omitempty" json:"deleted_at,omitempty"`
	DeletedBy      string     `yaml:"deleted_by,omitempty" json:"deleted_by,omitempty"`
	Body           string     `yaml:"-" json:"body,omitempty"`
}

var (
	keyRe        = regexp.MustCompile(`^(INC|DEC|ARC|CMP|CNV|LSN|PRC|NOTE)-(\d{4})-(\d{3,})$`)
	KeyInTextRe  = regexp.MustCompile(`\b(?:INC|DEC|ARC|CMP|CNV|LSN|PRC|NOTE)-\d{4}-\d{3,}\b`)
	projectIDRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

func ValidKey(k string) bool { return keyRe.MatchString(k) }

// FormatKey builds a key per RG-01.
func FormatKey(t Type, year, n int) string { return fmt.Sprintf("%s-%d-%03d", t.Prefix(), year, n) }

func ValidProjectID(id string) bool { return projectIDRe.MatchString(id) }

// Normalize applies the canonical form to list fields (EF-012).
func (k *Knowledge) Normalize() {
	k.Title = strings.TrimSpace(k.Title)
	k.Abstract = strings.TrimSpace(k.Abstract)
	k.Overview = strings.TrimSpace(k.Overview)
	k.Topics = normList(k.Topics, true)
	k.Components = normList(k.Components, false)
	k.Technologies = normList(k.Technologies, true)
	k.Keywords = normList(k.Keywords, false)
	k.SourceRefs = normList(k.SourceRefs, false)
	if k.Classification == "" {
		k.Classification = "internal"
	}
	if k.Confidence == "" {
		k.Confidence = "medium"
	}
}

func normList(in []string, lower bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if lower {
			s = strings.ToLower(s)
		}
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// Validate checks the model constraints, including RG-15 and RG-16.
func (k *Knowledge) Validate() error {
	var errs []FieldError
	add := func(field, msg string) { errs = append(errs, FieldError{Field: field, Message: msg}) }

	if !k.Type.Valid() {
		add("type", "unknown type")
	}
	if !ValidProjectID(k.ProjectID) {
		add("project", "invalid project id")
	}
	if k.Key != "" && !ValidKey(k.Key) {
		add("key", "invalid key format")
	}
	if k.Title == "" || len([]rune(k.Title)) > 120 {
		add("title", "required, at most 120 characters")
	}
	if k.Abstract == "" || len([]rune(k.Abstract)) > 300 {
		add("abstract", "required, at most 300 characters")
	}
	if k.Overview == "" || len([]rune(k.Overview)) > 1200 {
		add("overview", "required, at most 1200 characters")
	}
	if len([]rune(k.Body)) > 200000 {
		add("body", "at most 200000 characters")
	}
	if !k.Status.Valid() {
		add("status", "unknown status")
	}
	if !slices.Contains(confidences, k.Confidence) {
		add("confidence", "must be low, medium or high")
	}
	if !slices.Contains(classifications, k.Classification) {
		add("classification", "must be internal or restricted")
	}
	if k.Origin != "" && !slices.Contains(origins, k.Origin) {
		add("origin", "unknown origin")
	}
	if k.Type == Incident {
		if !slices.Contains(resolutions, k.Resolution) {
			add("resolution", "required for incidents: open, mitigated, resolved or wont_fix")
		}
		if !slices.Contains(severities, k.Severity) {
			add("severity", "required for incidents: low, medium, high or critical")
		}
	} else {
		if k.Resolution != "" {
			add("resolution", "only allowed for incidents")
		}
		if k.Severity != "" {
			add("severity", "only allowed for incidents")
		}
	}
	for i, c := range k.Claims {
		if strings.TrimSpace(c.Text) == "" || !slices.Contains(claimKinds, c.Kind) {
			add(fmt.Sprintf("claims[%d]", i), "text required; kind must be observed, hypothesis, proposal or conclusion")
		}
	}
	for i, r := range k.Relations {
		if !slices.Contains(relationTypes, r.Type) || !ValidKey(r.Target) {
			add(fmt.Sprintf("relations[%d]", i), "unknown relation type or invalid target key")
		}
	}
	for i, s := range k.SourceRefs {
		if !ValidSourceRef(s) {
			add(fmt.Sprintf("source_refs[%d]", i), "must look like git:…, file:…, ticket:…, session:…, url:… or log:…")
		}
	}
	if k.Supersedes != "" && (!ValidKey(k.Supersedes) || k.Supersedes == k.Key) {
		add("supersedes", "invalid key")
	}
	if k.Status == Confirmed && len(k.SourceRefs) == 0 {
		add("source_refs", "a confirmed knowledge needs at least one source (RG-04)")
	}
	if len(errs) > 0 {
		return &Error{Code: CodeValidation, Message: "knowledge is invalid", Fields: errs}
	}
	return nil
}

func ValidSourceRef(s string) bool {
	scheme, rest, ok := strings.Cut(s, ":")
	return ok && rest != "" && slices.Contains(sourceSchemes, scheme)
}

// Clone returns a deep copy.
func (k *Knowledge) Clone() *Knowledge {
	b, _ := json.Marshal(k)
	var c Knowledge
	_ = json.Unmarshal(b, &c)
	c.Schema = k.Schema
	return &c
}
