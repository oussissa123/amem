// Package httpapi serves the REST API (/v1), MCP over Streamable HTTP (/mcp)
// and health endpoints (spec 02 §11–12).
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/oussissa/amem/internal/domain"
	"github.com/oussissa/amem/internal/mcpserver"
	"github.com/oussissa/amem/internal/pgstore"
	"github.com/oussissa/amem/internal/search"
	"github.com/oussissa/amem/internal/service"
)

type ctxKey struct{}

type API struct {
	svc     *service.Service
	version string
	log     *slog.Logger
}

func New(svc *service.Service, version string, log *slog.Logger) http.Handler {
	a := &API{svc: svc, version: version, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", a.ready)

	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return mcpserver.New(svc, callerFrom(r.Context()), version)
	}, nil)
	mux.Handle("/mcp", a.auth(mcpHandler))

	v1 := func(pattern string, h func(w http.ResponseWriter, r *http.Request, c *service.Caller) error) {
		mux.Handle(pattern, a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := h(w, r, callerFrom(r.Context())); err != nil {
				a.problem(w, err)
			}
		})))
	}
	v1("GET /v1/projects", a.listProjects)
	v1("GET /v1/resolve", a.resolve)
	v1("GET /v1/projects/{project}/overview", a.overview)
	v1("POST /v1/projects/{project}/search", a.search)
	v1("POST /v1/projects/{project}/knowledge", a.record)
	v1("GET /v1/projects/{project}/knowledge/{key}", a.read)
	v1("PATCH /v1/projects/{project}/knowledge/{key}", a.update)
	v1("DELETE /v1/projects/{project}/knowledge/{key}", a.delete)
	v1("POST /v1/projects/{project}/knowledge/{key}/status", a.setStatus)
	v1("GET /v1/projects/{project}/knowledge/{key}/related", a.related)
	v1("POST /v1/projects/{project}/ingest", a.ingest)
	v1("POST /v1/projects/{project}/feedback", a.feedback)
	return http.MaxBytesHandler(mux, 4<<20)
}

func callerFrom(ctx context.Context) *service.Caller {
	c, _ := ctx.Value(ctxKey{}).(*service.Caller)
	return c
}

// auth resolves the Bearer token to a caller with its grants (spec 02 §14.1).
func (a *API) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			a.problem(w, domain.Errf(domain.CodeUnauthenticated, "missing Bearer token"))
			return
		}
		principal, err := pgstore.Authenticate(r.Context(), a.svc.DB.Pool, strings.TrimSpace(token))
		if err != nil {
			a.problem(w, err)
			return
		}
		grants, err := pgstore.Grants(r.Context(), a.svc.DB.Pool, principal)
		if err != nil {
			a.problem(w, err)
			return
		}
		c := &service.Caller{
			Principal:   principal,
			Grants:      grants,
			Configured:  r.Header.Get("X-Amem-Project"),
			ExternalRef: r.Header.Get("X-Amem-External-Ref"),
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, c)))
	})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.DB.Pool.Ping(r.Context()); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ready\n"))
}

// problem writes an RFC 9457 error.
func (a *API) problem(w http.ResponseWriter, err error) {
	e, ok := domain.AsError(err)
	status := http.StatusInternalServerError
	if ok {
		status = e.HTTPStatus()
	} else {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			e, status = domain.Errf(domain.CodeValidation, "request body too large"), http.StatusRequestEntityTooLarge
		} else {
			a.log.Error("request failed", "err", err)
			e = &domain.Error{Code: "internal_error", Message: "internal error"}
		}
	}
	body := map[string]any{
		"type": "https://amem.dev/errors/" + e.Code, "title": e.Message, "status": status, "code": e.Code,
	}
	if len(e.Fields) > 0 {
		body["errors"] = e.Fields
	}
	if len(e.Details) > 0 {
		body["details"] = e.Details
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return err
		}
		return domain.Errf(domain.CodeValidation, "invalid JSON body: "+err.Error())
	}
	return nil
}

// expectedVersion reads If-Match: "<id>.v<n>" or "<n>".
func expectedVersion(r *http.Request) (int, error) {
	m := strings.Trim(r.Header.Get("If-Match"), `" `)
	if m == "" {
		return 0, domain.Errf(domain.CodePrecondition, "If-Match header with the current ETag is required")
	}
	if i := strings.LastIndex(m, ".v"); i >= 0 {
		m = m[i+2:]
	}
	n, err := strconv.Atoi(m)
	if err != nil || n <= 0 {
		return 0, domain.Errf(domain.CodeValidation, "invalid If-Match value")
	}
	return n, nil
}

// project resolves the project of a /v1/projects/{project}/… call.
func (a *API) project(r *http.Request, c *service.Caller) (string, error) {
	res, err := a.svc.Resolve(r.Context(), c, r.PathValue("project"), "")
	if err != nil {
		return "", err
	}
	return res.Project, nil
}

func (a *API) listProjects(w http.ResponseWriter, r *http.Request, c *service.Caller) error {
	list, err := a.svc.Projects(r.Context(), c)
	if err != nil {
		return err
	}
	if list == nil {
		list = []service.ProjectInfo{}
	}
	return writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (a *API) resolve(w http.ResponseWriter, r *http.Request, c *service.Caller) error {
	res, err := a.svc.Resolve(r.Context(), c, r.URL.Query().Get("project"), r.URL.Query().Get("repo_url"))
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, res)
}

func (a *API) overview(w http.ResponseWriter, r *http.Request, c *service.Caller) error {
	p, err := a.project(r, c)
	if err != nil {
		return err
	}
	o, err := a.svc.Overview(r.Context(), c, p)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, o)
}

type searchBody struct {
	Query       string          `json:"query"`
	Context     *search.Context `json:"context"`
	Filters     *search.Filters `json:"filters"`
	Limit       int             `json:"limit"`
	MaxTokens   int             `json:"max_tokens"`
	IncludeWeak bool            `json:"include_weak"`
}

func (a *API) search(w http.ResponseWriter, r *http.Request, c *service.Caller) error {
	p, err := a.project(r, c)
	if err != nil {
		return err
	}
	var b searchBody
	if err := decode(r, &b); err != nil {
		return err
	}
	req := search.Request{Project: p, Query: b.Query, Context: b.Context, Limit: b.Limit, MaxTokens: b.MaxTokens, IncludeWeak: b.IncludeWeak}
	if b.Filters != nil {
		req.Filters = *b.Filters
	}
	resp, err := a.svc.Search(r.Context(), c, req)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, resp)
}

func (a *API) record(w http.ResponseWriter, r *http.Request, c *service.Caller) error {
	p, err := a.project(r, c)
	if err != nil {
		return err
	}
	var in service.RecordInput
	if err := decode(r, &in); err != nil {
		return err
	}
	if k := r.Header.Get("Idempotency-Key"); k != "" {
		in.IdempotencyKey = k
	}
	res, err := a.svc.Record(r.Context(), c, p, in)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", `"`+res.ETag+`"`)
	w.Header().Set("Location", "/v1/projects/"+p+"/knowledge/"+res.Key)
	return writeJSON(w, http.StatusCreated, res)
}

func (a *API) read(w http.ResponseWriter, r *http.Request, c *service.Caller) error {
	p, err := a.project(r, c)
	if err != nil {
		return err
	}
	version, _ := strconv.Atoi(r.URL.Query().Get("version"))
	res, err := a.svc.Read(r.Context(), c, p, r.PathValue("key"), version)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", `"`+res.ETag+`"`)
	return writeJSON(w, http.StatusOK, res)
}

func (a *API) update(w http.ResponseWriter, r *http.Request, c *service.Caller) error {
	p, err := a.project(r, c)
	if err != nil {
		return err
	}
	v, err := expectedVersion(r)
	if err != nil {
		return err
	}
	var patch service.Patch
	if err := decode(r, &patch); err != nil {
		return err
	}
	res, err := a.svc.Update(r.Context(), c, p, r.PathValue("key"), v, patch)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", `"`+res.ETag+`"`)
	return writeJSON(w, http.StatusOK, res)
}

func (a *API) delete(w http.ResponseWriter, r *http.Request, c *service.Caller) error {
	p, err := a.project(r, c)
	if err != nil {
		return err
	}
	v, err := expectedVersion(r)
	if err != nil {
		return err
	}
	res, err := a.svc.Delete(r.Context(), c, p, r.PathValue("key"), v, r.URL.Query().Get("reason"))
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, res)
}

func (a *API) setStatus(w http.ResponseWriter, r *http.Request, c *service.Caller) error {
	p, err := a.project(r, c)
	if err != nil {
		return err
	}
	v, err := expectedVersion(r)
	if err != nil {
		return err
	}
	var b struct {
		Status     string `json:"status"`
		Reason     string `json:"reason"`
		Resolution string `json:"resolution"`
	}
	if err := decode(r, &b); err != nil {
		return err
	}
	res, err := a.svc.SetStatus(r.Context(), c, p, r.PathValue("key"), v, b.Status, b.Reason, b.Resolution)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", `"`+res.ETag+`"`)
	return writeJSON(w, http.StatusOK, res)
}

func (a *API) related(w http.ResponseWriter, r *http.Request, c *service.Caller) error {
	p, err := a.project(r, c)
	if err != nil {
		return err
	}
	res, err := a.svc.Related(r.Context(), c, p, r.PathValue("key"))
	if err != nil {
		return err
	}
	if res == nil {
		res = []service.RelatedEntry{}
	}
	return writeJSON(w, http.StatusOK, map[string]any{"items": res})
}

func (a *API) ingest(w http.ResponseWriter, r *http.Request, c *service.Caller) error {
	p, err := a.project(r, c)
	if err != nil {
		return err
	}
	var b struct {
		Kind    string         `json:"kind"`
		Text    string         `json:"text"`
		Context map[string]any `json:"context"`
	}
	if err := decode(r, &b); err != nil {
		return err
	}
	res, err := a.svc.Ingest(r.Context(), c, p, b.Kind, b.Text, b.Context)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusAccepted, res)
}

func (a *API) feedback(w http.ResponseWriter, r *http.Request, c *service.Caller) error {
	p, err := a.project(r, c)
	if err != nil {
		return err
	}
	var b struct {
		SearchID string `json:"search_id"`
		Key      string `json:"key"`
		Verdict  string `json:"verdict"`
		Comment  string `json:"comment"`
	}
	if err := decode(r, &b); err != nil {
		return err
	}
	if err := a.svc.Feedback(r.Context(), c, p, b.SearchID, b.Key, b.Verdict, b.Comment); err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
