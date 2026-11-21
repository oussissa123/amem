package service_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/oussissa/amem/internal/docstore"
	"github.com/oussissa/amem/internal/domain"
	"github.com/oussissa/amem/internal/pgstore"
	"github.com/oussissa/amem/internal/search"
	"github.com/oussissa/amem/internal/service"
)

// newService creates a throw-away database. Tests are skipped unless
// AMEM_TEST_DATABASE_URL points to a PostgreSQL superuser connection.
func newService(t *testing.T) *service.Service {
	t.Helper()
	admin := os.Getenv("AMEM_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("AMEM_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("amem_test_%d", rand.Uint32())
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	cfg, _ := pgx.ParseConfig(admin)
	url := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", cfg.User, cfg.Password, cfg.Host, cfg.Port, name)
	db, err := pgstore.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		conn.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		conn.Close(ctx)
	})
	return service.New(db, docstore.New(t.TempDir()))
}

var admin = &service.Caller{Principal: "human:admin", LocalRole: domain.RoleAdmin}

func mustInit(t *testing.T, s *service.Service, id string, repos ...string) {
	t.Helper()
	p := domain.NewProject(id, "")
	for _, r := range repos {
		p.Repositories = append(p.Repositories, domain.Repository{URL: r})
	}
	if err := s.InitProject(context.Background(), admin, p); err != nil {
		t.Fatal(err)
	}
}

func incident(title, keyword string) service.RecordInput {
	return service.RecordInput{
		Type: "incident", Title: title, Abstract: title + ".", Overview: "Overview of " + title + ".",
		Keywords: []string{keyword}, Severity: "high", Resolution: "resolved", SourceRefs: []string{"ticket:multica/1"},
	}
}

func code(err error) string {
	if e, ok := domain.AsError(err); ok {
		return e.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestIsolationBetweenProjects(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	mustInit(t, s, "alpha")
	mustInit(t, s, "beta")
	r, err := s.Record(ctx, admin, "alpha", incident("Redis refused at startup", "ECONNREFUSED 127.0.0.1:6379"))
	if err != nil {
		t.Fatal(err)
	}
	betaOnly := &service.Caller{Principal: "agent:x", Grants: map[string]domain.Role{"beta": domain.RoleContributor}}

	if _, err := s.Read(ctx, betaOnly, "alpha", r.Key, 0); code(err) != domain.CodeNotFound {
		t.Errorf("read across projects: got %q, want not_found", code(err))
	}
	if _, err := s.Read(ctx, betaOnly, "beta", r.Key, 0); code(err) != domain.CodeNotFound {
		t.Errorf("key of alpha read in beta: got %q, want not_found", code(err))
	}
	resp, err := s.Search(ctx, betaOnly, search.Request{Project: "beta", Query: "ECONNREFUSED 127.0.0.1:6379 Redis"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 0 || !resp.NoRelevantResult {
		t.Errorf("search in beta returned alpha content: %+v", resp.Results)
	}
	if _, err := s.Resolve(ctx, betaOnly, "alpha", ""); code(err) != domain.CodeNotFound {
		t.Errorf("explicit resolution to an ungranted project: got %q", code(err))
	}
}

func TestResolutionFromGitRemote(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	mustInit(t, s, "mra", "git@github.com:acme/mra-api.git", "https://github.com/acme/mra-mobile")
	mustInit(t, s, "shop", "https://github.com/acme/shop")

	task := t.TempDir()
	for name, url := range map[string]string{"api": "https://github.com/ACME/mra-api", "mobile": "git@github.com:acme/mra-mobile.git"} {
		dir := filepath.Join(task, name, ".git")
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "config"), []byte("[remote \"origin\"]\n\turl = "+url+"\n"), 0o644)
	}
	agent := &service.Caller{Principal: "agent:multica/dev", LocalRole: domain.RoleContributor, Cwd: task}
	res, err := s.Resolve(ctx, agent, "", "")
	if err != nil || res.Project != "mra" || res.Rule != "git_remote" {
		t.Fatalf("multi-repo task dir: %+v %v", res, err)
	}
	agent.Cwd = filepath.Join(task, "api", "src") // nested directory of a clone
	os.MkdirAll(agent.Cwd, 0o755)
	if res, err := s.Resolve(ctx, agent, "", ""); err != nil || res.Project != "mra" {
		t.Fatalf("nested dir: %+v %v", res, err)
	}
	res, err = s.Resolve(ctx, agent, "shop", "")
	if err != nil || res.Project != "shop" || len(res.Warnings) == 0 {
		t.Fatalf("explicit wins with a mismatch warning: %+v %v", res, err)
	}
	agent.Cwd = t.TempDir()
	if _, err := s.Resolve(ctx, agent, "", ""); code(err) != domain.CodeProjectRequired {
		t.Fatalf("no hint: got %q, want project_required", code(err))
	}
	other := domain.NewProject("other", "")
	other.Repositories = []domain.Repository{{URL: "https://github.com/acme/shop.git"}}
	if err := s.InitProject(ctx, admin, other); code(err) != domain.CodeRepositoryTaken {
		t.Fatalf("repository shared by two projects: got %q", code(err))
	}
}

func TestConcurrentUpdatesNeverLoseWrites(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	mustInit(t, s, "alpha")
	r, err := s.Record(ctx, admin, "alpha", incident("Concurrent target", "concurrency-keyword"))
	if err != nil {
		t.Fatal(err)
	}
	const writers = 12
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := &service.Caller{Principal: fmt.Sprintf("agent:a%d", i), LocalRole: domain.RoleContributor}
			_, errs[i] = s.Update(ctx, c, "alpha", r.Key, 1, service.Patch{AddSourceRefs: []string{fmt.Sprintf("git:commit/%040d", i)}})
		}()
	}
	wg.Wait()
	ok, conflicts := 0, 0
	for _, err := range errs {
		switch code(err) {
		case "":
			ok++
		case domain.CodeVersionConflict:
			conflicts++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || conflicts != writers-1 {
		t.Fatalf("got %d successes and %d conflicts", ok, conflicts)
	}
	got, err := s.Read(ctx, admin, "alpha", r.Key, 0)
	if err != nil || got.Knowledge.Version != 2 {
		t.Fatalf("version after race: %+v %v", got, err)
	}
	entries, _ := os.ReadDir(filepath.Join(s.Docs.ProjectDir("alpha"), "history", r.Key))
	if len(entries) != 2 {
		t.Fatalf("history should hold exactly v1 and v2, got %d files", len(entries))
	}
}

func TestStatusRulesAndSupersede(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	mustInit(t, s, "alpha")
	agent := &service.Caller{Principal: "agent:dev", LocalRole: domain.RoleCurator}
	old, err := s.Record(ctx, agent, "alpha", service.RecordInput{Type: "decision", Title: "Amounts stored as floats",
		Abstract: "Monetary amounts are stored as floats.", Overview: "Legacy decision on amounts storage.", SourceRefs: []string{"ticket:t/1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(ctx, agent, "alpha", old.Key, 1, "confirmed", "looks right to me", ""); code(err) != domain.CodeForbidden {
		t.Fatalf("agent self-confirmation: got %q, want forbidden (RG-04)", code(err))
	}
	if _, err := s.SetStatus(ctx, admin, "alpha", old.Key, 1, "confirmed", "validated in review", ""); err != nil {
		t.Fatal(err)
	}
	nw, err := s.Record(ctx, admin, "alpha", service.RecordInput{Type: "decision", Title: "Amounts stored as integer cents",
		Abstract: "Monetary amounts are stored as integer cents.", Overview: "Replaces the float storage of amounts.",
		Supersedes: old.Key, Status: "confirmed", SourceRefs: []string{"ticket:t/2"}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.Search(ctx, admin, search.Request{Project: "alpha", Query: "monetary amounts stored"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Key != nw.Key {
		t.Fatalf("only the replacement should be returned, got %+v", resp.Results)
	}
	got, _ := s.Read(ctx, admin, "alpha", old.Key, 0)
	if got.Knowledge.Status != domain.Superseded || got.SupersededBy != nw.Key {
		t.Fatalf("old decision: status %s superseded_by %q", got.Knowledge.Status, got.SupersededBy)
	}
}

func TestSecretsAndRebuild(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	mustInit(t, s, "alpha")
	in := incident("Leaked key", "leak")
	in.Body = "the key is AKIAIOSFODNN7EXAMPLE"
	_, err := s.Record(ctx, admin, "alpha", in)
	if code(err) != domain.CodeSecret || strings.Contains(err.Error(), "AKIA") {
		t.Fatalf("secret: got %v", err)
	}
	r, err := s.Record(ctx, admin, "alpha", incident("Kafka consumer lag after deploy", "consumer group rebalancing"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Pool.Exec(ctx, `DELETE FROM knowledge`); err != nil {
		t.Fatal(err)
	}
	n, err := s.Rebuild(ctx, admin, "alpha")
	if err != nil || n != 1 {
		t.Fatalf("rebuild: %d %v", n, err)
	}
	resp, err := s.Search(ctx, admin, search.Request{Project: "alpha", Query: "consumer group rebalancing"})
	if err != nil || len(resp.Results) == 0 || resp.Results[0].Key != r.Key {
		t.Fatalf("search after rebuild: %+v %v", resp, err)
	}
	next, err := s.Record(ctx, admin, "alpha", incident("Another one", "other keyword"))
	if err != nil || next.Key == r.Key {
		t.Fatalf("keys must never be reused after rebuild: %v %v", next, err)
	}
}
