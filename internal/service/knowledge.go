package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"

	"github.com/oussissa/amem/internal/docstore"
	"github.com/oussissa/amem/internal/domain"
	"github.com/oussissa/amem/internal/pgstore"
	"github.com/oussissa/amem/internal/search"
	"github.com/oussissa/amem/internal/secrets"
)

// RecordInput is the creation payload (UC-05). Field names match the MCP tool.
type RecordInput struct {
	Type           string            `json:"type" jsonschema:"incident, decision, architecture, component, convention, lesson, procedure or note"`
	Title          string            `json:"title" jsonschema:"short title, at most 120 characters"`
	Abstract       string            `json:"abstract" jsonschema:"one sentence, at most 300 characters"`
	Overview       string            `json:"overview" jsonschema:"summary an agent can act on, at most 1200 characters"`
	Body           string            `json:"body,omitempty" jsonschema:"detailed Markdown content (sections such as Symptoms, Cause, Solution)"`
	Topics         []string          `json:"topics,omitempty"`
	Components     []string          `json:"components,omitempty"`
	Technologies   []string          `json:"technologies,omitempty"`
	Keywords       []string          `json:"keywords,omitempty" jsonschema:"exact terms: error messages, identifiers, aliases"`
	Status         string            `json:"status,omitempty" jsonschema:"proposed (default); curators may create confirmed"`
	Resolution     string            `json:"resolution,omitempty" jsonschema:"incidents only: open, mitigated, resolved, wont_fix"`
	Severity       string            `json:"severity,omitempty" jsonschema:"incidents only: low, medium, high, critical"`
	Confidence     string            `json:"confidence,omitempty" jsonschema:"low, medium (default) or high"`
	ConfidenceNote string            `json:"confidence_note,omitempty"`
	Claims         []domain.Claim    `json:"claims,omitempty" jsonschema:"assertions qualified as observed, hypothesis, proposal or conclusion"`
	SourceRefs     []string          `json:"source_refs,omitempty" jsonschema:"git:commit/<sha>, file:<path>#L1-9@<sha>, ticket:<system>/<id>, session:<id>, url:<url>, log:<ref>"`
	Relations      []domain.Relation `json:"relations,omitempty"`
	Supersedes     string            `json:"supersedes,omitempty" jsonschema:"key of the knowledge this one replaces"`
	Classification string            `json:"classification,omitempty" jsonschema:"internal (default) or restricted"`
	IdempotencyKey string            `json:"idempotency_key,omitempty" jsonschema:"same key + same payload returns the first result instead of creating twice"`
	OnDuplicate    string            `json:"on_duplicate,omitempty" jsonschema:"warn (default) or reject"`
}

type WriteResult struct {
	Project            string   `json:"project"`
	Key                string   `json:"key"`
	Version            int      `json:"version"`
	Status             string   `json:"status"`
	ETag               string   `json:"etag"`
	PossibleDuplicates []string `json:"possible_duplicates,omitempty"`
	Warnings           []string `json:"warnings,omitempty"`
	NoChange           bool     `json:"no_change,omitempty"`
}

func etag(k *domain.Knowledge) string { return fmt.Sprintf("%s.v%d", k.ID, k.Version) }

func (s *Service) scanner(project string) (*secrets.Scanner, *domain.Project, error) {
	p, _, err := s.Docs.ReadProject(project)
	if err != nil {
		return nil, nil, err
	}
	sc, err := secrets.NewScanner(p.Privacy.DenyPatterns)
	return sc, p, err
}

// checkSecrets implements RG-10 for structured writes: reject, never echo.
func checkSecrets(sc *secrets.Scanner, k *domain.Knowledge) error {
	fields := map[string]string{
		"title": k.Title, "abstract": k.Abstract, "overview": k.Overview, "body": k.Body,
		"keywords": strings.Join(k.Keywords, "\n"), "confidence_note": k.ConfidenceNote,
	}
	for i, c := range k.Claims {
		fields[fmt.Sprintf("claims[%d]", i)] = c.Text
	}
	var found []map[string]any
	for field, text := range fields {
		for _, f := range sc.Scan(text) {
			found = append(found, map[string]any{"field": field, "rule": f.Rule, "line": f.Line, "column": f.Column})
		}
	}
	if len(found) == 0 {
		return nil
	}
	e := domain.Errf(domain.CodeSecret, "the content contains what looks like a secret; remove it and retry")
	e.Details = map[string]any{"findings": found}
	return e
}

func (s *Service) checkRelations(ctx context.Context, q pgstore.Q, k *domain.Knowledge) error {
	targets := []string{}
	for _, r := range k.Relations {
		targets = append(targets, r.Target)
	}
	if k.Supersedes != "" {
		targets = append(targets, k.Supersedes)
	}
	for _, t := range targets {
		if t == k.Key {
			return domain.Errf(domain.CodeValidation, "a knowledge cannot relate to itself")
		}
		if _, err := pgstore.GetRow(ctx, q, k.ProjectID, t, false); err != nil {
			if _, ok := domain.AsError(err); ok {
				return domain.Errf(domain.CodeValidation, "relation target "+t+" does not exist in project "+k.ProjectID)
			}
			return err
		}
	}
	return nil
}

// persist writes file + history then the projection and audit in tx.
func (s *Service) persist(ctx context.Context, tx pgx.Tx, c *Caller, k *domain.Knowledge, action string, details map[string]any) (func(), error) {
	rel, hash, undo, err := s.Docs.Write(k)
	if err != nil {
		return nil, err
	}
	if err := pgstore.Project(ctx, tx, k, rel, hash); err != nil {
		undo()
		return nil, err
	}
	if err := pgstore.Audit(ctx, tx, k.ProjectID, c.Principal, action, k.Key, k.Version, s.auditDetails(c, details)); err != nil {
		undo()
		return nil, err
	}
	return undo, nil
}

// commit runs fn in a transaction and undoes file writes if the commit fails.
func (s *Service) commit(ctx context.Context, fn func(tx pgx.Tx, onFail func(func())) error) error {
	var undos []func()
	onFail := func(u func()) { undos = append(undos, u) }
	err := pgx.BeginFunc(ctx, s.DB.Pool, func(tx pgx.Tx) error { return fn(tx, onFail) })
	if err != nil {
		for i := len(undos) - 1; i >= 0; i-- {
			undos[i]()
		}
	}
	return err
}

// Record creates a knowledge (UC-05).
func (s *Service) Record(ctx context.Context, c *Caller, project string, in RecordInput) (*WriteResult, error) {
	if err := c.require(project, domain.RoleContributor); err != nil {
		return nil, err
	}
	role := c.role(project)
	now := s.Now()
	k := &domain.Knowledge{
		ID: ulid.Make().String(), ProjectID: project, Type: domain.Type(in.Type),
		Title: in.Title, Abstract: in.Abstract, Overview: in.Overview, Body: in.Body,
		Topics: in.Topics, Components: in.Components, Technologies: in.Technologies, Keywords: in.Keywords,
		Status: domain.Proposed, Resolution: in.Resolution, Severity: in.Severity,
		Confidence: in.Confidence, ConfidenceNote: in.ConfidenceNote, Claims: in.Claims,
		SourceRefs: in.SourceRefs, Relations: in.Relations, Supersedes: in.Supersedes,
		Classification: in.Classification, Origin: "manual",
		CreatedBy: c.Principal, UpdatedBy: c.Principal, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	if c.IsAgent() {
		k.Origin = "agent"
		if task := c.Orchestrator["MULTICA_TASK_ID"]; task != "" && !hasTicket(k.SourceRefs) {
			k.SourceRefs = append(k.SourceRefs, "ticket:multica/"+task)
		}
	}
	switch in.Status {
	case "", string(domain.Proposed):
	case string(domain.Confirmed):
		if role < domain.RoleCurator {
			return nil, domain.Errf(domain.CodeForbidden, "only curators can create confirmed knowledge (RG-03)")
		}
		k.Status = domain.Confirmed
	default:
		return nil, domain.Errf(domain.CodeValidation, "status at creation must be proposed or confirmed")
	}
	sc, p, err := s.scanner(project)
	if err != nil {
		return nil, err
	}
	for i, t := range k.Topics {
		k.Topics[i] = p.TopicAlias(t)
	}
	k.Normalize()
	if err := k.Validate(); err != nil {
		return nil, err
	}
	if k.Classification == "restricted" && role < domain.RoleCurator {
		return nil, domain.Errf(domain.CodeForbidden, "only curators can create restricted knowledge")
	}
	if err := checkSecrets(sc, k); err != nil {
		return nil, err
	}

	reqHash := ""
	if in.IdempotencyKey != "" {
		b, _ := json.Marshal(in)
		reqHash = docstore.Hash(b)
		var stored []byte
		var storedHash string
		err := s.DB.Pool.QueryRow(ctx, `SELECT request_hash, response FROM idempotency_keys
			WHERE project_id=$1 AND principal_id=$2 AND key=$3 AND created_at > now() - interval '24 hours'`,
			project, c.Principal, in.IdempotencyKey).Scan(&storedHash, &stored)
		if err == nil {
			if storedHash != reqHash {
				return nil, domain.Errf(domain.CodeIdempotency, "idempotency_key reused with a different payload")
			}
			var res WriteResult
			return &res, json.Unmarshal(stored, &res)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}

	dups, err := s.possibleDuplicates(ctx, c, k)
	if err != nil {
		return nil, err
	}
	if len(dups) > 0 && in.OnDuplicate == "reject" {
		e := domain.Errf("duplicate_suspected", "similar knowledge already exists")
		e.Details = map[string]any{"possible_duplicates": dups}
		return nil, e
	}

	res := &WriteResult{Project: project, PossibleDuplicates: dups}
	err = s.commit(ctx, func(tx pgx.Tx, onFail func(func())) error {
		if err := pgstore.LockProject(ctx, tx, project); err != nil {
			return err
		}
		if err := s.checkRelations(ctx, tx, k); err != nil {
			return err
		}
		key, err := pgstore.NextKey(ctx, tx, project, k.Type, now.Year())
		if err != nil {
			return err
		}
		k.Key = key
		undo, err := s.persist(ctx, tx, c, k, "knowledge.create", nil)
		if err != nil {
			return err
		}
		onFail(undo)
		if k.Status == domain.Confirmed && k.Supersedes != "" {
			if err := s.supersede(ctx, tx, c, k, onFail); err != nil {
				return err
			}
		}
		res.Key, res.Version, res.Status, res.ETag = k.Key, k.Version, string(k.Status), etag(k)
		if in.IdempotencyKey != "" {
			b, _ := json.Marshal(res)
			if _, err := tx.Exec(ctx, `INSERT INTO idempotency_keys (project_id, principal_id, key, request_hash, response)
				VALUES ($1,$2,$3,$4,$5) ON CONFLICT (project_id, principal_id, key) DO UPDATE SET request_hash=$4, response=$5, created_at=now()`,
				project, c.Principal, in.IdempotencyKey, reqHash, b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, s.RegenMap(ctx, project)
}

func hasTicket(refs []string) bool {
	for _, r := range refs {
		if strings.HasPrefix(r, "ticket:") {
			return true
		}
	}
	return false
}

// possibleDuplicates is the lexical approximation of RG-09 until embeddings land.
func (s *Service) possibleDuplicates(ctx context.Context, c *Caller, k *domain.Knowledge) ([]string, error) {
	resp, err := search.Run(ctx, s.DB.Pool, search.Request{
		Project: k.ProjectID, Query: k.Title + "\n" + k.Abstract, Limit: 3, MaxTokens: 6000,
		AllowRestricted: c.role(k.ProjectID) >= domain.RoleCurator,
		Weights:         search.Weights{Lexical: 1, FreshnessHalfLife: 365, MinCoverage: 0.8, MaxRequiredMatches: 8},
	})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range resp.Results {
		if !r.Weak {
			out = append(out, r.Key)
		}
	}
	return out, nil
}

// supersede marks k.Supersedes as superseded by k (RG-06).
func (s *Service) supersede(ctx context.Context, tx pgx.Tx, c *Caller, k *domain.Knowledge, onFail func(func())) error {
	if err := pgstore.LockKnowledge(ctx, tx, k.ProjectID, k.Supersedes); err != nil {
		return err
	}
	row, err := pgstore.GetRow(ctx, tx, k.ProjectID, k.Supersedes, true)
	if err != nil {
		return err
	}
	if row.SupersedesKey == k.Key {
		return domain.Errf(domain.CodeValidation, "supersede cycle between "+k.Key+" and "+row.Key)
	}
	old, _, err := s.Docs.ReadFile(k.ProjectID, row.FilePath)
	if err != nil {
		return err
	}
	if old.Status != domain.Superseded {
		old.Status, old.StatusReason = domain.Superseded, "superseded by "+k.Key
		old.Version++
		old.UpdatedAt, old.UpdatedBy = s.Now(), c.Principal
		undo, err := s.persist(ctx, tx, c, old, "status.change", map[string]any{"to": "superseded", "by": k.Key})
		if err != nil {
			return err
		}
		onFail(undo)
	}
	return pgstore.SetSupersededBy(ctx, tx, k.ProjectID, old.Key, k.Key)
}

// Patch lists the editable fields (UC-06); nil means unchanged.
type Patch struct {
	Title          *string            `json:"title,omitempty"`
	Abstract       *string            `json:"abstract,omitempty"`
	Overview       *string            `json:"overview,omitempty"`
	Body           *string            `json:"body,omitempty"`
	Topics         *[]string          `json:"topics,omitempty"`
	Components     *[]string          `json:"components,omitempty"`
	Technologies   *[]string          `json:"technologies,omitempty"`
	Keywords       *[]string          `json:"keywords,omitempty"`
	AddKeywords    []string           `json:"add_keywords,omitempty"`
	SourceRefs     *[]string          `json:"source_refs,omitempty"`
	AddSourceRefs  []string           `json:"add_source_refs,omitempty"`
	Relations      *[]domain.Relation `json:"relations,omitempty"`
	AddRelations   []domain.Relation  `json:"add_relations,omitempty"`
	Claims         *[]domain.Claim    `json:"claims,omitempty"`
	Resolution     *string            `json:"resolution,omitempty"`
	Severity       *string            `json:"severity,omitempty"`
	Confidence     *string            `json:"confidence,omitempty"`
	ConfidenceNote *string            `json:"confidence_note,omitempty"`
	Classification *string            `json:"classification,omitempty"`
	Supersedes     *string            `json:"supersedes,omitempty"`
	// Immutable fields: present only to reject them explicitly (RG-02).
	Type   *string `json:"type,omitempty"`
	Key    *string `json:"key,omitempty"`
	Status *string `json:"status,omitempty"`
}

// additiveOnly reports whether the patch only adds sources, keywords or relations.
func (p *Patch) additiveOnly() bool {
	return p.Title == nil && p.Abstract == nil && p.Overview == nil && p.Body == nil && p.Topics == nil &&
		p.Components == nil && p.Technologies == nil && p.Keywords == nil && p.SourceRefs == nil &&
		p.Relations == nil && p.Claims == nil && p.Resolution == nil && p.Severity == nil &&
		p.Confidence == nil && p.ConfidenceNote == nil && p.Classification == nil && p.Supersedes == nil
}

func (p *Patch) apply(k *domain.Knowledge) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	setL := func(dst *[]string, v *[]string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&k.Title, p.Title)
	set(&k.Abstract, p.Abstract)
	set(&k.Overview, p.Overview)
	set(&k.Body, p.Body)
	setL(&k.Topics, p.Topics)
	setL(&k.Components, p.Components)
	setL(&k.Technologies, p.Technologies)
	setL(&k.Keywords, p.Keywords)
	setL(&k.SourceRefs, p.SourceRefs)
	k.Keywords = append(k.Keywords, p.AddKeywords...)
	k.SourceRefs = append(k.SourceRefs, p.AddSourceRefs...)
	if p.Relations != nil {
		k.Relations = *p.Relations
	}
	for _, r := range p.AddRelations {
		if !slices.Contains(k.Relations, r) {
			k.Relations = append(k.Relations, r)
		}
	}
	if p.Claims != nil {
		k.Claims = *p.Claims
	}
	set(&k.Resolution, p.Resolution)
	set(&k.Severity, p.Severity)
	set(&k.Confidence, p.Confidence)
	set(&k.ConfidenceNote, p.ConfidenceNote)
	set(&k.Classification, p.Classification)
	set(&k.Supersedes, p.Supersedes)
}

// mutate runs the guarded write protocol of spec 02 §5.2 on one document.
func (s *Service) mutate(ctx context.Context, c *Caller, project, key string, expected int, action string,
	fn func(tx pgx.Tx, cur *domain.Knowledge, row *pgstore.Row) (*domain.Knowledge, []string, map[string]any, error)) (*WriteResult, error) {
	if expected <= 0 {
		return nil, domain.Errf(domain.CodePrecondition, "expected_version is required (read the knowledge first)")
	}
	res := &WriteResult{Project: project}
	err := s.commit(ctx, func(tx pgx.Tx, onFail func(func())) error {
		if err := pgstore.LockKnowledge(ctx, tx, project, key); err != nil {
			return err
		}
		row, err := pgstore.GetRow(ctx, tx, project, key, true)
		if err != nil {
			return err
		}
		if row.Deleted {
			return domain.NotFound(key)
		}
		if row.Classification == "restricted" && c.role(project) < domain.RoleCurator {
			return domain.NotFound(key)
		}
		if row.Version != expected {
			e := domain.Errf(domain.CodeVersionConflict, fmt.Sprintf("%s is at version %d, not %d; read it again", row.Key, row.Version, expected))
			e.Details = map[string]any{"current_version": row.Version}
			return e
		}
		cur, hash, err := s.Docs.ReadFile(project, row.FilePath)
		if err != nil {
			return err
		}
		if hash != row.ContentHash {
			e := domain.Errf(domain.CodeVersionConflict, "the file of "+row.Key+" was modified outside amem; run `amem index rebuild` then retry")
			e.Details = map[string]any{"current_version": row.Version}
			return e
		}
		before := *cur.Clone()
		next, warnings, details, err := fn(tx, cur, row)
		if err != nil {
			return err
		}
		res.Warnings = warnings
		if sameContent(next, &before) {
			res.Key, res.Version, res.Status, res.ETag, res.NoChange = before.Key, before.Version, string(before.Status), etag(&before), true
			return nil
		}
		next.Version = before.Version + 1
		next.UpdatedAt, next.UpdatedBy = s.Now(), c.Principal
		undo, err := s.persist(ctx, tx, c, next, action, details)
		if err != nil {
			return err
		}
		onFail(undo)
		if next.Status == domain.Confirmed && next.Supersedes != "" && before.Status != domain.Confirmed {
			if err := s.supersede(ctx, tx, c, next, onFail); err != nil {
				return err
			}
		}
		res.Key, res.Version, res.Status, res.ETag = next.Key, next.Version, string(next.Status), etag(next)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !res.NoChange {
		return res, s.RegenMap(ctx, project)
	}
	return res, nil
}

// Update modifies a knowledge (UC-06, RG-07, RG-08).
func (s *Service) Update(ctx context.Context, c *Caller, project, key string, expected int, p Patch) (*WriteResult, error) {
	if err := c.require(project, domain.RoleContributor); err != nil {
		return nil, err
	}
	if p.Type != nil || p.Key != nil {
		return nil, domain.Errf(domain.CodeImmutable, "type and key never change; record a new knowledge that supersedes this one")
	}
	if p.Status != nil {
		return nil, domain.Errf(domain.CodeValidation, "use memory_set_status to change the status")
	}
	role := c.role(project)
	return s.mutate(ctx, c, project, key, expected, "knowledge.update",
		func(tx pgx.Tx, cur *domain.Knowledge, row *pgstore.Row) (*domain.Knowledge, []string, map[string]any, error) {
			var warnings []string
			next := *cur.Clone()
			p.apply(&next)
			sc, proj, err := s.scanner(project)
			if err != nil {
				return nil, nil, nil, err
			}
			for i, t := range next.Topics {
				next.Topics[i] = proj.TopicAlias(t)
			}
			next.Normalize()
			if role < domain.RoleCurator {
				if next.Classification != cur.Classification {
					return nil, nil, nil, domain.Errf(domain.CodeForbidden, "only curators change the classification")
				}
				if cur.Status == domain.Confirmed && !p.additiveOnly() {
					next.Status, next.StatusReason = domain.Proposed, "content changed by "+c.Principal
					warnings = append(warnings, "status_reset_to_proposed")
				}
				if cur.Status != domain.Confirmed && cur.Status != domain.Proposed && cur.Status != domain.Disputed {
					return nil, nil, nil, domain.Errf(domain.CodeForbidden, "only curators edit "+string(cur.Status)+" knowledge")
				}
			}
			if err := next.Validate(); err != nil {
				return nil, nil, nil, err
			}
			if err := checkSecrets(sc, &next); err != nil {
				return nil, nil, nil, err
			}
			if err := s.checkRelations(ctx, tx, &next); err != nil {
				return nil, nil, nil, err
			}
			return &next, warnings, nil, nil
		})
}

// SetStatus applies a status transition (UC-07, RG-04/05/06).
func (s *Service) SetStatus(ctx context.Context, c *Caller, project, key string, expected int, to, reason, resolution string) (*WriteResult, error) {
	if err := c.require(project, domain.RoleContributor); err != nil {
		return nil, err
	}
	target := domain.Status(to)
	if !target.Valid() {
		return nil, domain.Errf(domain.CodeValidation, "unknown status "+to)
	}
	_, proj, err := s.scanner(project)
	if err != nil {
		return nil, err
	}
	return s.mutate(ctx, c, project, key, expected, "status.change",
		func(tx pgx.Tx, cur *domain.Knowledge, row *pgstore.Row) (*domain.Knowledge, []string, map[string]any, error) {
			if err := domain.CheckTransition(cur.Status, target, c.role(project), cur.CreatedBy == c.Principal,
				proj.Governance.AgentSelfConfirm, c.IsAgent(), reason); err != nil {
				return nil, nil, nil, err
			}
			next := *cur.Clone()
			next.Status, next.StatusReason = target, strings.TrimSpace(reason)
			if resolution != "" {
				next.Resolution = resolution
			}
			if err := next.Validate(); err != nil {
				return nil, nil, nil, err
			}
			return &next, nil, map[string]any{"from": string(cur.Status), "to": to}, nil
		})
}

// Delete is the logical deletion of RG-12.
func (s *Service) Delete(ctx context.Context, c *Caller, project, key string, expected int, reason string) (*WriteResult, error) {
	if err := c.require(project, domain.RoleCurator); err != nil {
		return nil, err
	}
	return s.mutate(ctx, c, project, key, expected, "knowledge.delete",
		func(tx pgx.Tx, cur *domain.Knowledge, row *pgstore.Row) (*domain.Knowledge, []string, map[string]any, error) {
			next := *cur.Clone()
			now := s.Now()
			next.DeletedAt, next.DeletedBy, next.StatusReason = &now, c.Principal, reason
			return &next, nil, map[string]any{"reason": reason}, nil
		})
}

// ReadResult is the L2 view (UC-04).
type ReadResult struct {
	Project      string            `json:"project"`
	Knowledge    *domain.Knowledge `json:"knowledge"`
	ETag         string            `json:"etag"`
	SupersededBy string            `json:"superseded_by,omitempty"`
	Incoming     []domain.Relation `json:"incoming_relations,omitempty"`
	Notice       string            `json:"notice"`
}

func (s *Service) Read(ctx context.Context, c *Caller, project, key string, version int) (*ReadResult, error) {
	if err := c.require(project, domain.RoleReader); err != nil {
		return nil, err
	}
	row, err := pgstore.GetRow(ctx, s.DB.Pool, project, key, false)
	if err != nil {
		return nil, err
	}
	if (row.Classification == "restricted" || row.Deleted) && c.role(project) < domain.RoleCurator {
		return nil, domain.NotFound(key)
	}
	var k *domain.Knowledge
	if version > 0 && version != row.Version {
		k, err = s.Docs.ReadVersion(project, row.Key, version)
	} else {
		k, _, err = s.Docs.ReadFile(project, row.FilePath)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, domain.NotFound(key)
	} else if err != nil {
		return nil, err
	}
	res := &ReadResult{Project: project, Knowledge: k, ETag: etag(k), SupersededBy: row.SupersededByKey, Notice: search.Notice}
	rows, err := s.DB.Pool.Query(ctx, `SELECT k.key, r.type FROM knowledge_relations r JOIN knowledge k ON k.id=r.source_id
		WHERE r.project_id=$1 AND r.target_key=$2 AND k.deleted_at IS NULL ORDER BY k.key`, project, row.Key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rel domain.Relation
		if err := rows.Scan(&rel.Target, &rel.Type); err != nil {
			return nil, err
		}
		res.Incoming = append(res.Incoming, rel)
	}
	return res, rows.Err()
}

// RelatedEntry is an L1 view of a related knowledge.
type RelatedEntry struct {
	Relation  string `json:"relation"`
	Direction string `json:"direction"` // outgoing, incoming, shared_component
	Key       string `json:"key"`
	Title     string `json:"title"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Abstract  string `json:"abstract"`
}

func (s *Service) Related(ctx context.Context, c *Caller, project, key string) ([]RelatedEntry, error) {
	if err := c.require(project, domain.RoleReader); err != nil {
		return nil, err
	}
	row, err := pgstore.GetRow(ctx, s.DB.Pool, project, key, false)
	if err != nil {
		return nil, err
	}
	allow := c.role(project) >= domain.RoleCurator
	if (row.Classification == "restricted" && !allow) || row.Deleted {
		return nil, domain.NotFound(key)
	}
	rows, err := s.DB.Pool.Query(ctx, `
		SELECT r.type, 'outgoing', k.key, k.title, k.type, k.status, k.abstract
		  FROM knowledge_relations r JOIN knowledge k ON k.project_id=r.project_id AND k.key=r.target_key
		 WHERE r.source_id=$2
		UNION ALL
		SELECT r.type, 'incoming', k.key, k.title, k.type, k.status, k.abstract
		  FROM knowledge_relations r JOIN knowledge k ON k.id=r.source_id
		 WHERE r.project_id=$1 AND r.target_key=$3
		UNION ALL
		SELECT 'supersedes', 'outgoing', k.key, k.title, k.type, k.status, k.abstract
		  FROM knowledge k WHERE k.project_id=$1 AND k.key=$4
		UNION ALL
		SELECT 'supersedes', 'incoming', k.key, k.title, k.type, k.status, k.abstract
		  FROM knowledge k WHERE k.project_id=$1 AND k.supersedes_key=$3
		UNION ALL
		(SELECT 'shared_component', 'shared_component', k.key, k.title, k.type, k.status, k.abstract
		   FROM knowledge k, knowledge me
		  WHERE me.id=$2 AND k.project_id=$1 AND k.id<>me.id AND k.components && me.components
		    AND k.status NOT IN ('obsolete','rejected','superseded')
		  ORDER BY cardinality(ARRAY(SELECT unnest(k.components) INTERSECT SELECT unnest(me.components))) DESC, k.updated_at DESC
		  LIMIT 10)`, project, row.ID, row.Key, row.SupersedesKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RelatedEntry
	seen := map[string]bool{}
	for rows.Next() {
		var e RelatedEntry
		if err := rows.Scan(&e.Relation, &e.Direction, &e.Key, &e.Title, &e.Type, &e.Status, &e.Abstract); err != nil {
			return nil, err
		}
		id := e.Relation + e.Direction + e.Key
		if seen[id] || (e.Direction == "shared_component" && seen["key:"+e.Key]) {
			continue
		}
		seen[id], seen["key:"+e.Key] = true, true
		out = append(out, e)
	}
	if !allow {
		// Restricted targets are filtered after the fact to keep the query simple.
		var filtered []RelatedEntry
		for _, e := range out {
			if r, err := pgstore.GetRow(ctx, s.DB.Pool, project, e.Key, false); err == nil && r.Classification == "internal" && !r.Deleted {
				filtered = append(filtered, e)
			}
		}
		out = filtered
	}
	return out, rows.Err()
}

// Search runs a search and logs it (UC-03).
func (s *Service) Search(ctx context.Context, c *Caller, req search.Request) (*search.Response, error) {
	if err := c.require(req.Project, domain.RoleReader); err != nil {
		return nil, err
	}
	req.AllowRestricted = c.role(req.Project) >= domain.RoleCurator
	start := s.Now()
	resp, err := search.Run(ctx, s.DB.Pool, req)
	if err != nil {
		return nil, err
	}
	resp.SearchID = ulid.Make().String()
	keys := make([]string, len(resp.Results))
	for i, r := range resp.Results {
		keys[i] = r.Key
	}
	filters, _ := json.Marshal(req.Filters)
	_, _ = s.DB.Pool.Exec(ctx, `INSERT INTO search_log (id, project_id, principal_id, query, filters, result_keys, degraded, latency_ms)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, resp.SearchID, req.Project, c.Principal, req.Query, filters, keys, resp.Degraded,
		int(s.Now().Sub(start).Milliseconds()))
	return resp, nil
}

// Feedback records a verdict on a result (UC-11).
func (s *Service) Feedback(ctx context.Context, c *Caller, project, searchID, key, verdict, comment string) error {
	if err := c.require(project, domain.RoleReader); err != nil {
		return err
	}
	if !slices.Contains([]string{"useful", "not_useful", "wrong", "outdated"}, verdict) {
		return domain.Errf(domain.CodeValidation, "verdict must be useful, not_useful, wrong or outdated")
	}
	_, err := s.DB.Pool.Exec(ctx, `INSERT INTO feedback (search_id, project_id, knowledge_key, principal_id, verdict, comment)
		VALUES (nullif($1,''),$2,$3,$4,$5,nullif($6,''))`, searchID, project, key, c.Principal, verdict, comment)
	return err
}

// IngestResult acknowledges a raw material (UC-09).
type IngestResult struct {
	Project     string `json:"project"`
	IngestionID string `json:"ingestion_id"`
	Redactions  int    `json:"redactions"`
	State       string `json:"state"`
	Message     string `json:"message"`
}

func (s *Service) Ingest(ctx context.Context, c *Caller, project, kind, text string, context map[string]any) (*IngestResult, error) {
	if err := c.require(project, domain.RoleContributor); err != nil {
		return nil, err
	}
	if !slices.Contains([]string{"session", "task_report", "incident_log", "note"}, kind) {
		return nil, domain.Errf(domain.CodeValidation, "kind must be session, task_report, incident_log or note")
	}
	if len(text) > 2<<20 {
		return nil, domain.Errf(domain.CodeValidation, "material is limited to 2 MB")
	}
	sc, p, err := s.scanner(project)
	if err != nil {
		return nil, err
	}
	redacted, n := sc.Redact(text)
	id := ulid.Make().String()
	now := s.Now()
	rel, err := s.Docs.WriteSession(project, id, now, redacted)
	if err != nil {
		return nil, err
	}
	days := p.Retention.SessionsDays
	if days <= 0 {
		days = 30
	}
	if context == nil {
		context = map[string]any{}
	}
	for k, v := range c.Orchestrator {
		context[k] = v
	}
	// Extraction (phase 4) is not wired yet: the material waits for it.
	state := "needs_llm"
	err = pgx.BeginFunc(ctx, s.DB.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO ingestions (id, project_id, principal_id, kind, file_path, redactions, state, context, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, project, c.Principal, kind, rel, n, state, context,
			now.AddDate(0, 0, days)); err != nil {
			return err
		}
		return pgstore.Audit(ctx, tx, project, c.Principal, "ingest", id, 0, s.auditDetails(c, map[string]any{"kind": kind, "redactions": n}))
	})
	if err != nil {
		return nil, err
	}
	return &IngestResult{Project: project, IngestionID: id, Redactions: n, State: state,
		Message: "Stored. Automatic extraction is not enabled yet: record durable knowledge with memory_record."}, nil
}

// sameContent compares two versions ignoring bookkeeping fields.
func sameContent(a, b *domain.Knowledge) bool {
	x, y := a.Clone(), b.Clone()
	for _, k := range []*domain.Knowledge{x, y} {
		k.Version, k.UpdatedAt, k.UpdatedBy = 0, time.Time{}, ""
	}
	bx, _ := docstore.Serialize(x)
	by, _ := docstore.Serialize(y)
	return string(bx) == string(by)
}
