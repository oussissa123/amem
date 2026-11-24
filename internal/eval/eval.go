// Package eval measures search quality on a set of queries with expected
// keys (UC-16, EF-074): Recall@k, Precision@k, MRR, nDCG@10 and latencies.
package eval

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/oussissa/amem/internal/search"
)

// Set is the YAML evaluation file.
type Set struct {
	Project string `yaml:"project,omitempty"`
	K       int    `yaml:"k,omitempty"` // cut-off of Recall@k / Precision@k, default 3
	Cases   []Case `yaml:"cases"`
}

type Case struct {
	ID       string         `yaml:"id,omitempty"`
	Query    string         `yaml:"query"`
	Error    string         `yaml:"error,omitempty"`
	Filters  search.Filters `yaml:"filters,omitempty"`
	Expected []string       `yaml:"expected"` // relevant keys, most relevant first; empty: no result expected
}

func Load(path string) (*Set, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Set
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(s.Cases) == 0 {
		return nil, fmt.Errorf("%s: no cases", path)
	}
	if s.K <= 0 {
		s.K = 3
	}
	return &s, nil
}

// SearchFunc runs one query; the evaluation asks for 10 results.
type SearchFunc func(ctx context.Context, c Case) (*search.Response, error)

type CaseResult struct {
	ID        string   `json:"id"`
	Query     string   `json:"query"`
	Expected  []string `json:"expected"`
	Got       []string `json:"got"`
	Recall    float64  `json:"recall"`
	Precision float64  `json:"precision"`
	RR        float64  `json:"rr"`
	NDCG10    float64  `json:"ndcg10"`
	LatencyMS float64  `json:"latency_ms"`
	Degraded  []string `json:"degraded,omitempty"`
	// NoResultOK is set for cases expecting nothing: true when the search
	// reported no_relevant_result.
	NoResultOK *bool `json:"no_result_ok,omitempty"`
}

type Report struct {
	Set          string       `json:"set"`
	Project      string       `json:"project"`
	Model        string       `json:"model,omitempty"`
	Weights      any          `json:"weights,omitempty"`
	At           time.Time    `json:"at"`
	K            int          `json:"k"`
	Cases        int          `json:"cases"`
	RecallAtK    float64      `json:"recall_at_k"`
	PrecisionAtK float64      `json:"precision_at_k"`
	MRR          float64      `json:"mrr"`
	NDCG10       float64      `json:"ndcg10"`
	NoResultAcc  *float64     `json:"no_result_accuracy,omitempty"`
	LatencyP50   float64      `json:"latency_p50_ms"`
	LatencyP95   float64      `json:"latency_p95_ms"`
	Degraded     int          `json:"degraded_queries"`
	Results      []CaseResult `json:"results"`
}

// Run evaluates every case. Cases with expected keys feed the ranking
// metrics; cases expecting nothing feed NoResultAcc.
func Run(ctx context.Context, s *Set, run SearchFunc) (*Report, error) {
	rep := &Report{Project: s.Project, K: s.K, Cases: len(s.Cases), At: time.Now().UTC()}
	var lat []float64
	ranked, empty, emptyOK := 0, 0, 0
	for i, c := range s.Cases {
		if c.ID == "" {
			c.ID = fmt.Sprintf("q%03d", i+1)
		}
		start := time.Now()
		resp, err := run(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", c.ID, err)
		}
		r := CaseResult{ID: c.ID, Query: c.Query, Expected: c.Expected, Degraded: resp.Degraded,
			LatencyMS: float64(time.Since(start).Microseconds()) / 1000}
		for _, res := range resp.Results {
			r.Got = append(r.Got, res.Key)
		}
		lat = append(lat, r.LatencyMS)
		if len(resp.Degraded) > 0 {
			rep.Degraded++
		}
		if len(c.Expected) == 0 {
			ok := resp.NoRelevantResult
			r.NoResultOK = &ok
			empty++
			if ok {
				emptyOK++
			}
		} else {
			r.Recall, r.Precision, r.RR, r.NDCG10 = Metrics(c.Expected, r.Got, s.K)
			rep.RecallAtK += r.Recall
			rep.PrecisionAtK += r.Precision
			rep.MRR += r.RR
			rep.NDCG10 += r.NDCG10
			ranked++
		}
		rep.Results = append(rep.Results, r)
	}
	if ranked > 0 {
		n := float64(ranked)
		rep.RecallAtK, rep.PrecisionAtK, rep.MRR, rep.NDCG10 = rep.RecallAtK/n, rep.PrecisionAtK/n, rep.MRR/n, rep.NDCG10/n
	}
	if empty > 0 {
		acc := float64(emptyOK) / float64(empty)
		rep.NoResultAcc = &acc
	}
	rep.LatencyP50, rep.LatencyP95 = percentile(lat, 0.5), percentile(lat, 0.95)
	return rep, nil
}

// Metrics computes Recall@k, Precision@k, reciprocal rank and nDCG@10 with
// binary relevance.
func Metrics(expected, got []string, k int) (recall, precision, rr, ndcg float64) {
	rel := map[string]bool{}
	for _, e := range expected {
		rel[e] = true
	}
	hits := 0
	for i, g := range got {
		if !rel[g] {
			continue
		}
		if i < k {
			hits++
		}
		if rr == 0 {
			rr = 1 / float64(i+1)
		}
		if i < 10 {
			ndcg += 1 / math.Log2(float64(i+2))
		}
	}
	ideal := 0.0
	for i := 0; i < min(len(expected), 10); i++ {
		ideal += 1 / math.Log2(float64(i+2))
	}
	if ideal > 0 {
		ndcg /= ideal
	}
	recall = float64(hits) / float64(min(len(expected), k))
	precision = float64(hits) / float64(k)
	return
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	i := int(math.Ceil(p*float64(len(s)))) - 1
	return s[max(0, min(i, len(s)-1))]
}

// Summary renders a report, with the deltas against prev when given.
func Summary(r, prev *Report) string {
	var b strings.Builder
	line := func(name string, v float64, pv func(*Report) float64, higherBetter bool) {
		fmt.Fprintf(&b, "%-16s %8.3f", name, v)
		if prev != nil {
			d := v - pv(prev)
			mark := ""
			if (d > 0.0005 && !higherBetter) || (d < -0.0005 && higherBetter) {
				mark = "  ▼ regression"
			}
			fmt.Fprintf(&b, "  (%+.3f)%s", d, mark)
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "project %s · %d cases · model %s\n", r.Project, r.Cases, orNone(r.Model))
	line(fmt.Sprintf("Recall@%d", r.K), r.RecallAtK, func(p *Report) float64 { return p.RecallAtK }, true)
	line(fmt.Sprintf("Precision@%d", r.K), r.PrecisionAtK, func(p *Report) float64 { return p.PrecisionAtK }, true)
	line("MRR", r.MRR, func(p *Report) float64 { return p.MRR }, true)
	line("nDCG@10", r.NDCG10, func(p *Report) float64 { return p.NDCG10 }, true)
	if r.NoResultAcc != nil {
		line("no-result acc.", *r.NoResultAcc, func(p *Report) float64 {
			if p.NoResultAcc == nil {
				return 0
			}
			return *p.NoResultAcc
		}, true)
	}
	line("latency p50 ms", r.LatencyP50, func(p *Report) float64 { return p.LatencyP50 }, false)
	line("latency p95 ms", r.LatencyP95, func(p *Report) float64 { return p.LatencyP95 }, false)
	if r.Degraded > 0 {
		fmt.Fprintf(&b, "degraded queries: %d (semantic channel unavailable)\n", r.Degraded)
	}
	for _, c := range r.Results {
		if len(c.Expected) > 0 && c.Recall < 1 {
			fmt.Fprintf(&b, "miss %s %q: expected %v, got %v\n", c.ID, c.Query, c.Expected, c.Got)
		}
		if c.NoResultOK != nil && !*c.NoResultOK {
			fmt.Fprintf(&b, "noise %s %q: expected no result, got %v\n", c.ID, c.Query, c.Got)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func orNone(s string) string {
	if s == "" {
		return "none (lexical)"
	}
	return s
}
