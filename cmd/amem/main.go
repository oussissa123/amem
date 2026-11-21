// Command amem is the single binary of Agent Memory Core: CLI, MCP server
// (stdio) and HTTP server (REST + MCP).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/oussissa/amem/internal/adapter"
	"github.com/oussissa/amem/internal/config"
	"github.com/oussissa/amem/internal/docstore"
	"github.com/oussissa/amem/internal/domain"
	"github.com/oussissa/amem/internal/httpapi"
	"github.com/oussissa/amem/internal/mcpserver"
	"github.com/oussissa/amem/internal/pgstore"
	"github.com/oussissa/amem/internal/search"
	"github.com/oussissa/amem/internal/service"
)

var version = "0.1.0-dev"

type app struct {
	cfg     config.Config
	project string
	asJSON  bool
	svc     *service.Service
	db      *pgstore.DB
}

func main() {
	a := &app{cfg: config.Load()}
	root := &cobra.Command{
		Use:           "amem",
		Short:         "Agent Memory Core — durable project memory for AI agents",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	root.PersistentFlags().StringVarP(&a.project, "project", "p", a.cfg.Project, "project id (default: resolved from the git repository)")
	root.PersistentFlags().BoolVar(&a.asJSON, "json", false, "JSON output")
	root.AddCommand(a.initCmd(), a.projectCmd(), a.resolveCmd(), a.mcpCmd(), a.serveCmd(), a.overviewCmd(),
		a.searchCmd(), a.readCmd(), a.recordCmd(), a.updateCmd(), a.statusCmd(), a.relatedCmd(), a.ingestCmd(),
		a.feedbackCmd(), a.indexCmd(), a.tokenCmd(), a.grantCmd(), a.adapterCmd())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := root.ExecuteContext(ctx)
	if a.db != nil {
		a.db.Close()
	}
	if err != nil {
		code := 1
		if e, ok := domain.AsError(err); ok {
			b, _ := json.MarshalIndent(e, "", "  ")
			fmt.Fprintln(os.Stderr, string(b))
			switch e.Code {
			case domain.CodeVersionConflict:
				code = 3
			case domain.CodeNotFound:
				code = 4
			}
		} else {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(code)
	}
}

func (a *app) open(ctx context.Context) error {
	if a.svc != nil {
		return nil
	}
	db, err := pgstore.Open(ctx, a.cfg.DatabaseURL)
	if err != nil {
		return err
	}
	a.db = db
	a.svc = service.New(db, docstore.New(a.cfg.Root))
	return nil
}

func (a *app) caller(forceAgent bool) *service.Caller { return config.LocalCaller(a.project, forceAgent) }

// target resolves the project for CLI commands.
func (a *app) target(ctx context.Context, c *service.Caller) (string, error) {
	res, err := a.svc.Resolve(ctx, c, "", "")
	if err != nil {
		return "", err
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s (project %s, rule %s)\n", w, res.Project, res.Rule)
	}
	return res.Project, nil
}

func (a *app) print(v any, text string) error {
	if a.asJSON || text == "" {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	fmt.Println(text)
	return nil
}

// run wraps a command needing the database and a resolved project.
func (a *app) run(fn func(ctx context.Context, c *service.Caller, project string, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if err := a.open(ctx); err != nil {
			return err
		}
		c := a.caller(false)
		p, err := a.target(ctx, c)
		if err != nil {
			return err
		}
		return fn(ctx, c, p, args)
	}
}

// ---- projects

func (a *app) initCmd() *cobra.Command {
	var name, description string
	var repos []string
	cmd := &cobra.Command{
		Use:   "init <project-id>",
		Short: "Create a project memory (UC-01)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.open(ctx); err != nil {
				return err
			}
			p := domain.NewProject(args[0], name)
			p.Description = description
			for _, r := range repos {
				p.Repositories = append(p.Repositories, domain.Repository{URL: r})
			}
			if err := a.svc.InitProject(ctx, a.caller(false), p); err != nil {
				return err
			}
			fmt.Printf("Project %s created in %s\n", p.ID, a.svc.Docs.ProjectDir(p.ID))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "display name")
	cmd.Flags().StringVar(&description, "description", "", "purpose of the project (shown in the map)")
	cmd.Flags().StringSliceVar(&repos, "repo", nil, "git remote URL of a repository of the project (repeatable)")
	return cmd
}

func (a *app) projectCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Manage projects"}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List accessible projects",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.open(cmd.Context()); err != nil {
				return err
			}
			list, err := a.svc.Projects(cmd.Context(), a.caller(false))
			if err != nil {
				return err
			}
			var b strings.Builder
			for _, p := range list {
				fmt.Fprintf(&b, "%-20s %-12s %s\n", p.ID, p.Role, p.Name)
				for _, r := range p.Repositories {
					fmt.Fprintf(&b, "%-20s   repo %s\n", "", domain.NormalizeRemote(r.URL))
				}
			}
			return a.print(list, strings.TrimRight(b.String(), "\n"))
		},
	})
	var role string
	addRepo := &cobra.Command{
		Use: "add-repo <project> <remote-url>", Short: "Attach a repository to a project", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.open(cmd.Context()); err != nil {
				return err
			}
			return a.svc.UpdateProject(cmd.Context(), a.caller(false), args[0], func(p *domain.Project) error {
				norm := domain.NormalizeRemote(args[1])
				for _, r := range p.Repositories {
					if domain.NormalizeRemote(r.URL) == norm {
						return nil
					}
				}
				p.Repositories = append(p.Repositories, domain.Repository{URL: args[1], Role: role})
				return nil
			})
		},
	}
	addRepo.Flags().StringVar(&role, "role", "", "role of the repository (backend, mobile…)")
	cmd.AddCommand(addRepo)
	cmd.AddCommand(&cobra.Command{
		Use: "link <project> <system>:<workspace>/<project>", Short: "Link a project to an external reference (e.g. multica:acme/7d1c…)", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.open(cmd.Context()); err != nil {
				return err
			}
			system, ref, ok := strings.Cut(args[1], ":")
			if !ok || ref == "" {
				return errors.New("expected <system>:<ref>")
			}
			ws, proj, found := strings.Cut(ref, "/")
			if !found {
				ws, proj = "", ref
			}
			return a.svc.UpdateProject(cmd.Context(), a.caller(false), args[0], func(p *domain.Project) error {
				p.ExternalRefs = append(p.ExternalRefs, domain.ExternalRef{System: system, Workspace: ws, Project: proj})
				return nil
			})
		},
	})
	return cmd
}

func (a *app) resolveCmd() *cobra.Command {
	var cwd, repo string
	cmd := &cobra.Command{
		Use: "resolve", Short: "Show which project a call made from a directory resolves to (UC-19)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.open(cmd.Context()); err != nil {
				return err
			}
			c := a.caller(false)
			if cwd != "" {
				c.Cwd, _ = filepath.Abs(cwd)
			}
			res, err := a.svc.Resolve(cmd.Context(), c, "", repo)
			if err != nil {
				return err
			}
			return a.print(res, fmt.Sprintf("project %s (rule %s) remotes=%v principal=%s", res.Project, res.Rule, res.Remotes, c.Principal))
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "directory to resolve from")
	cmd.Flags().StringVar(&repo, "repo-url", "", "resolve from a remote URL instead")
	return cmd
}

// ---- servers

func (a *app) mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use: "mcp", Short: "Run the MCP server on stdio (launched by the agent)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.open(cmd.Context()); err != nil {
				return err
			}
			srv := mcpserver.New(a.svc, a.caller(true), version)
			return srv.Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
}

func (a *app) serveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "serve", Short: "Run the HTTP server: REST /v1, MCP /mcp (Bearer tokens)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.open(cmd.Context()); err != nil {
				return err
			}
			log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
			srv := &http.Server{Addr: a.cfg.Listen, Handler: httpapi.New(a.svc, version, log), ReadHeaderTimeout: 10 * time.Second}
			go func() {
				<-cmd.Context().Done()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				srv.Shutdown(ctx)
			}()
			log.Info("amem listening", "addr", a.cfg.Listen, "version", version)
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&a.cfg.Listen, "listen", a.cfg.Listen, "listen address")
	return cmd
}

// ---- memory operations

func (a *app) overviewCmd() *cobra.Command {
	return &cobra.Command{
		Use: "overview", Short: "Show the project map (L0)",
		RunE: a.run(func(ctx context.Context, c *service.Caller, p string, _ []string) error {
			o, err := a.svc.Overview(ctx, c, p)
			if err != nil {
				return err
			}
			return a.print(o, o.Map)
		}),
	}
}

func (a *app) searchCmd() *cobra.Command {
	var f search.Filters
	var sc search.Context
	var limit, maxTokens int
	var weak bool
	cmd := &cobra.Command{
		Use: "search <text>", Short: "Search project memory", Args: cobra.MaximumNArgs(1),
		RunE: a.run(func(ctx context.Context, c *service.Caller, p string, args []string) error {
			q := ""
			if len(args) == 1 {
				q = args[0]
			}
			resp, err := a.svc.Search(ctx, c, search.Request{Project: p, Query: q, Context: &sc, Filters: f,
				Limit: limit, MaxTokens: maxTokens, IncludeWeak: weak})
			if err != nil {
				return err
			}
			return a.print(resp, mcpserver.RenderSearch(resp))
		}),
	}
	cmd.Flags().StringSliceVar(&f.Types, "type", nil, "filter by type")
	cmd.Flags().StringSliceVar(&f.Statuses, "status", nil, "filter by status")
	cmd.Flags().StringSliceVar(&f.Topics, "topic", nil, "filter by topic")
	cmd.Flags().StringSliceVar(&f.Components, "component", nil, "filter by component")
	cmd.Flags().StringSliceVar(&f.Technologies, "technology", nil, "filter by technology")
	cmd.Flags().StringVar(&sc.Error, "error", "", "exact error message")
	cmd.Flags().IntVar(&limit, "limit", 5, "maximum results")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 1500, "token budget")
	cmd.Flags().BoolVar(&weak, "include-weak", false, "include results below the relevance threshold")
	return cmd
}

func (a *app) readCmd() *cobra.Command {
	var v int
	cmd := &cobra.Command{
		Use: "read <key>", Short: "Read a knowledge (L2)", Args: cobra.ExactArgs(1),
		RunE: a.run(func(ctx context.Context, c *service.Caller, p string, args []string) error {
			r, err := a.svc.Read(ctx, c, p, args[0], v)
			if err != nil {
				return err
			}
			return a.print(r, mcpserver.RenderRead(r))
		}),
	}
	cmd.Flags().IntVar(&v, "version", 0, "read a previous version")
	return cmd
}

// readInput decodes a Markdown file with front matter (or a YAML/JSON file)
// into v, using the JSON field names. The Markdown body goes to "body".
func readInput(path string, v any) error {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return err
	}
	m := map[string]any{}
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	if strings.HasPrefix(s, "---\n") {
		front, body, _ := strings.Cut(s[4:], "\n---")
		if err := yaml.Unmarshal([]byte(front), &m); err != nil {
			return err
		}
		if b := strings.TrimSpace(strings.TrimPrefix(body, "\n")); b != "" {
			m["body"] = b
		}
	} else if err := yaml.Unmarshal(data, &m); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (a *app) recordCmd() *cobra.Command {
	var in service.RecordInput
	var from string
	cmd := &cobra.Command{
		Use:   "record [type]",
		Short: "Record a knowledge (from flags or a Markdown file with front matter)",
		Args:  cobra.MaximumNArgs(1),
		RunE: a.run(func(ctx context.Context, c *service.Caller, p string, args []string) error {
			if from != "" {
				var fileIn service.RecordInput
				if err := readInput(from, &fileIn); err != nil {
					return err
				}
				in = fileIn
			}
			if len(args) == 1 {
				in.Type = args[0]
			}
			r, err := a.svc.Record(ctx, c, p, in)
			if err != nil {
				return err
			}
			text := fmt.Sprintf("%s recorded (version %d, %s)", r.Key, r.Version, r.Status)
			if len(r.PossibleDuplicates) > 0 {
				text += "\npossible duplicates: " + strings.Join(r.PossibleDuplicates, ", ")
			}
			return a.print(r, text)
		}),
	}
	f := cmd.Flags()
	f.StringVar(&from, "from", "", "Markdown file with front matter, YAML or JSON (- for stdin)")
	f.StringVar(&in.Title, "title", "", "")
	f.StringVar(&in.Abstract, "abstract", "", "")
	f.StringVar(&in.Overview, "overview", "", "")
	f.StringVar(&in.Body, "body", "", "")
	f.StringSliceVar(&in.Topics, "topic", nil, "")
	f.StringSliceVar(&in.Components, "component", nil, "")
	f.StringSliceVar(&in.Technologies, "technology", nil, "")
	f.StringArrayVar(&in.Keywords, "keyword", nil, "exact term (repeatable)")
	f.StringArrayVar(&in.SourceRefs, "source", nil, "source reference (repeatable)")
	f.StringVar(&in.Status, "status", "", "proposed or confirmed")
	f.StringVar(&in.Severity, "severity", "", "")
	f.StringVar(&in.Resolution, "resolution", "", "")
	f.StringVar(&in.Confidence, "confidence", "", "")
	f.StringVar(&in.Supersedes, "supersedes", "", "")
	return cmd
}

func (a *app) updateCmd() *cobra.Command {
	var from string
	var expected int
	var addSources, addKeywords []string
	cmd := &cobra.Command{
		Use: "update <key>", Short: "Update a knowledge (patch from a YAML/JSON file or flags)", Args: cobra.ExactArgs(1),
		RunE: a.run(func(ctx context.Context, c *service.Caller, p string, args []string) error {
			var patch service.Patch
			if from != "" {
				if err := readInput(from, &patch); err != nil {
					return err
				}
			}
			patch.AddSourceRefs = append(patch.AddSourceRefs, addSources...)
			patch.AddKeywords = append(patch.AddKeywords, addKeywords...)
			if expected == 0 {
				cur, err := a.svc.Read(ctx, c, p, args[0], 0)
				if err != nil {
					return err
				}
				expected = cur.Knowledge.Version
			}
			r, err := a.svc.Update(ctx, c, p, args[0], expected, patch)
			if err != nil {
				return err
			}
			return a.print(r, fmt.Sprintf("%s → version %d (%s) %s", r.Key, r.Version, r.Status, strings.Join(r.Warnings, " ")))
		}),
	}
	cmd.Flags().StringVar(&from, "from", "", "patch file (YAML/JSON, field names of memory_update)")
	cmd.Flags().IntVar(&expected, "expected-version", 0, "expected version (default: current version)")
	cmd.Flags().StringArrayVar(&addSources, "add-source", nil, "")
	cmd.Flags().StringArrayVar(&addKeywords, "add-keyword", nil, "")
	return cmd
}

func (a *app) statusCmd() *cobra.Command {
	var reason, resolution string
	var expected int
	cmd := &cobra.Command{
		Use: "status <key> <status>", Short: "Change the status of a knowledge (UC-07)", Args: cobra.ExactArgs(2),
		RunE: a.run(func(ctx context.Context, c *service.Caller, p string, args []string) error {
			if expected == 0 {
				cur, err := a.svc.Read(ctx, c, p, args[0], 0)
				if err != nil {
					return err
				}
				expected = cur.Knowledge.Version
			}
			r, err := a.svc.SetStatus(ctx, c, p, args[0], expected, args[1], reason, resolution)
			if err != nil {
				return err
			}
			return a.print(r, fmt.Sprintf("%s is %s (version %d)", r.Key, r.Status, r.Version))
		}),
	}
	cmd.Flags().StringVar(&reason, "reason", "", "reason (required)")
	cmd.Flags().StringVar(&resolution, "resolution", "", "incident resolution")
	cmd.Flags().IntVar(&expected, "expected-version", 0, "expected version (default: current version)")
	return cmd
}

func (a *app) relatedCmd() *cobra.Command {
	return &cobra.Command{
		Use: "related <key>", Short: "List related knowledge", Args: cobra.ExactArgs(1),
		RunE: a.run(func(ctx context.Context, c *service.Caller, p string, args []string) error {
			r, err := a.svc.Related(ctx, c, p, args[0])
			if err != nil {
				return err
			}
			var b strings.Builder
			for _, e := range r {
				fmt.Fprintf(&b, "%-17s %-16s %s [%s] %s\n", e.Relation, e.Direction, e.Key, e.Status, e.Title)
			}
			return a.print(r, strings.TrimRight(b.String(), "\n"))
		}),
	}
}

func (a *app) ingestCmd() *cobra.Command {
	var kind string
	cmd := &cobra.Command{
		Use: "ingest <file|->", Short: "Store raw material for extraction (UC-09)", Args: cobra.ExactArgs(1),
		RunE: a.run(func(ctx context.Context, c *service.Caller, p string, args []string) error {
			var data []byte
			var err error
			if args[0] == "-" {
				data, err = io.ReadAll(os.Stdin)
			} else {
				data, err = os.ReadFile(args[0])
			}
			if err != nil {
				return err
			}
			r, err := a.svc.Ingest(ctx, c, p, kind, string(data), nil)
			if err != nil {
				return err
			}
			return a.print(r, fmt.Sprintf("ingestion %s stored (%d redactions, %s)", r.IngestionID, r.Redactions, r.State))
		}),
	}
	cmd.Flags().StringVar(&kind, "kind", "session", "session, task_report, incident_log or note")
	return cmd
}

func (a *app) feedbackCmd() *cobra.Command {
	var searchID, comment string
	cmd := &cobra.Command{
		Use: "feedback <key> <useful|not_useful|wrong|outdated>", Short: "Give feedback on a result", Args: cobra.ExactArgs(2),
		RunE: a.run(func(ctx context.Context, c *service.Caller, p string, args []string) error {
			return a.svc.Feedback(ctx, c, p, searchID, args[0], args[1], comment)
		}),
	}
	cmd.Flags().StringVar(&searchID, "search-id", "", "")
	cmd.Flags().StringVar(&comment, "comment", "", "")
	return cmd
}

func (a *app) indexCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "index", Short: "Manage derived indexes"}
	cmd.AddCommand(&cobra.Command{
		Use: "rebuild", Short: "Rebuild the index of a project from its files (UC-13)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.open(cmd.Context()); err != nil {
				return err
			}
			if a.project == "" {
				return errors.New("--project is required")
			}
			n, err := a.svc.Rebuild(cmd.Context(), a.caller(false), a.project)
			if err != nil {
				return err
			}
			fmt.Printf("%s: %d documents indexed\n", a.project, n)
			return nil
		},
	})
	return cmd
}

// ---- access

func (a *app) tokenCmd() *cobra.Command {
	var ttl time.Duration
	cmd := &cobra.Command{Use: "token", Short: "Manage API tokens"}
	create := &cobra.Command{
		Use: "create <principal>", Short: "Create a token for a principal (agent:… or human:…); shown once", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.open(cmd.Context()); err != nil {
				return err
			}
			if a.caller(false).IsAgent() {
				return domain.Errf(domain.CodeForbidden, "agents cannot create tokens")
			}
			kind, _, _ := strings.Cut(args[0], ":")
			if kind != "agent" && kind != "human" {
				return errors.New("principal must start with agent: or human:")
			}
			if err := pgstore.EnsurePrincipal(cmd.Context(), a.db.Pool, args[0], kind); err != nil {
				return err
			}
			t, err := pgstore.CreateToken(cmd.Context(), a.db.Pool, args[0], ttl)
			if err != nil {
				return err
			}
			fmt.Println(t)
			return nil
		},
	}
	create.Flags().DurationVar(&ttl, "ttl", 90*24*time.Hour, "validity (0 = no expiry)")
	cmd.AddCommand(create)
	return cmd
}

func (a *app) grantCmd() *cobra.Command {
	return &cobra.Command{
		Use: "grant <principal> <project> <reader|contributor|curator|admin>", Short: "Grant a role on a project", Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.open(cmd.Context()); err != nil {
				return err
			}
			c := a.caller(false)
			if c.IsAgent() || c.LocalRole < domain.RoleAdmin {
				return domain.Errf(domain.CodeForbidden, "only a local administrator can grant roles")
			}
			role, err := domain.ParseRole(args[2])
			if err != nil {
				return err
			}
			kind, _, _ := strings.Cut(args[0], ":")
			if err := pgstore.EnsurePrincipal(cmd.Context(), a.db.Pool, args[0], kind); err != nil {
				return err
			}
			if _, err := pgstore.GetProject(cmd.Context(), a.db.Pool, args[1]); err != nil {
				return err
			}
			return pgstore.Grant(cmd.Context(), a.db.Pool, args[0], args[1], role)
		},
	}
}

// ---- integrations (spec 02 §12.2)

func (a *app) adapterCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "adapter", Short: "Connect amem to Multica, Claude Code or Codex"}
	var agent, mode, server, dir, binary string
	install := &cobra.Command{
		Use: "install <multica|claude-code|codex>", Short: "Generate the integration configuration", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if binary == "" {
				exe, err := os.Executable()
				if err != nil {
					return err
				}
				binary = exe
			}
			switch args[0] {
			case "claude-code":
				files, err := adapter.InstallClaudeCode(dir, binary)
				if err != nil {
					return err
				}
				fmt.Println("Updated:", strings.Join(files, ", "))
			case "codex":
				files, err := adapter.InstallCodex(dir, binary)
				if err != nil {
					return err
				}
				fmt.Println("Updated:", strings.Join(files, ", "))
			case "multica":
				return a.installMultica(cmd.Context(), agent, mode, server, binary)
			default:
				return fmt.Errorf("unknown tool %q", args[0])
			}
			if a.cfg.DatabaseURL != config.DefaultDatabaseURL || os.Getenv("AMEM_ROOT") != "" {
				fmt.Println("Note: AMEM_DATABASE_URL / AMEM_ROOT differ from the defaults; export them in the environment the agent is launched from.")
			}
			return nil
		},
	}
	install.Flags().StringVar(&agent, "agent", "", "Multica agent name (multica)")
	install.Flags().StringVar(&mode, "mode", "stdio", "stdio or http (multica)")
	install.Flags().StringVar(&server, "server", "", "amem server URL for --mode http")
	install.Flags().StringVar(&dir, "dir", ".", "repository directory (claude-code, codex)")
	install.Flags().StringVar(&binary, "binary", "", "path of the amem binary seen by the agent (default: this binary)")
	cmd.AddCommand(install)

	var checkDir string
	check := &cobra.Command{
		Use: "check", Short: "Start `amem mcp` like an agent would and call memory_projects + memory_overview",
		RunE: func(cmd *cobra.Command, _ []string) error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			c := exec.Command(exe, "mcp")
			c.Dir = checkDir
			if a.project != "" {
				c.Env = append(os.Environ(), "AMEM_PROJECT="+a.project)
			}
			client := mcp.NewClient(&mcp.Implementation{Name: "amem-adapter-check", Version: version}, nil)
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			session, err := client.Connect(ctx, &mcp.CommandTransport{Command: c}, nil)
			if err != nil {
				return err
			}
			defer session.Close()
			for _, tool := range []string{"memory_projects", "memory_overview"} {
				res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: map[string]any{}})
				if err != nil {
					return fmt.Errorf("%s: %w", tool, err)
				}
				fmt.Printf("== %s%s\n", tool, map[bool]string{true: " (error)", false: ""}[res.IsError])
				for _, ct := range res.Content {
					if t, ok := ct.(*mcp.TextContent); ok {
						fmt.Println(t.Text)
					}
				}
				if res.IsError {
					return errors.New(tool + " failed")
				}
			}
			return nil
		},
	}
	check.Flags().StringVar(&checkDir, "dir", ".", "directory the agent runs in")
	cmd.AddCommand(check)
	return cmd
}

func (a *app) installMultica(ctx context.Context, agent, mode, server, binary string) error {
	if agent == "" {
		return errors.New("--agent is required (name of the Multica agent)")
	}
	if err := a.open(ctx); err != nil {
		return err
	}
	c := a.caller(false)
	if c.IsAgent() {
		return domain.Errf(domain.CodeForbidden, "agents cannot install integrations")
	}
	principal := "agent:multica/" + config.Slug(agent)
	if err := pgstore.EnsurePrincipal(ctx, a.db.Pool, principal, "agent"); err != nil {
		return err
	}
	var cfg map[string]any
	switch mode {
	case "stdio":
		env := map[string]string{"AMEM_PRINCIPAL": principal}
		if a.cfg.DatabaseURL != config.DefaultDatabaseURL {
			env["AMEM_DATABASE_URL"] = a.cfg.DatabaseURL
		}
		if os.Getenv("AMEM_ROOT") != "" {
			env["AMEM_ROOT"] = a.cfg.Root
		}
		if a.project != "" {
			env["AMEM_PROJECT"] = a.project
		}
		cfg = adapter.MulticaStdio(binary, env)
	case "http":
		if server == "" {
			return errors.New("--server is required with --mode http")
		}
		if a.project == "" {
			return errors.New("--project is required with --mode http (the server cannot see the task's repositories)")
		}
		if err := pgstore.Grant(ctx, a.db.Pool, principal, a.project, domain.RoleContributor); err != nil {
			return err
		}
		token, err := pgstore.CreateToken(ctx, a.db.Pool, principal, 90*24*time.Hour)
		if err != nil {
			return err
		}
		cfg = adapter.MulticaHTTP(server, token, a.project)
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Printf("# 1. mcp_config of the Multica agent %q (agent settings → MCP), principal %s:\n%s\n\n", agent, principal, b)
	fmt.Printf("# 2. Add to the agent instructions:\n%s\n", adapter.Instructions)
	if mode == "stdio" {
		fmt.Printf("\n# amem must be installed at %s on every machine running a Multica daemon for this agent.\n", binary)
	} else {
		fmt.Println("\n# The token above is shown once; it is stored hashed. Revoke it by deleting the principal's tokens.")
	}
	return nil
}
