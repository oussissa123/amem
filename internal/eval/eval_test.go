package eval

import (
	"context"
	"math"
	"testing"

	"github.com/oussissa/amem/internal/search"
)

func TestMetrics(t *testing.T) {
	r, p, rr, ndcg := Metrics([]string{"A", "B"}, []string{"X", "A", "Y", "B"}, 3)
	if r != 0.5 || math.Abs(p-1.0/3) > 1e-9 || rr != 0.5 {
		t.Errorf("recall %v precision %v rr %v", r, p, rr)
	}
	want := (1/math.Log2(3) + 1/math.Log2(5)) / (1 + 1/math.Log2(3))
	if math.Abs(ndcg-want) > 1e-9 {
		t.Errorf("ndcg %v, want %v", ndcg, want)
	}
	if r, _, rr, ndcg := Metrics([]string{"A"}, nil, 3); r != 0 || rr != 0 || ndcg != 0 {
		t.Errorf("no result must score 0")
	}
}

func TestRun(t *testing.T) {
	set := &Set{K: 3, Cases: []Case{{Query: "q1", Expected: []string{"A"}}, {Query: "nothing"}}}
	rep, err := Run(context.Background(), set, func(_ context.Context, c Case) (*search.Response, error) {
		if c.Query == "nothing" {
			return &search.Response{NoRelevantResult: true}, nil
		}
		return &search.Response{Results: []search.Result{{Key: "A"}}, Degraded: []string{"semantic"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.RecallAtK != 1 || rep.MRR != 1 || *rep.NoResultAcc != 1 || rep.Degraded != 1 || rep.Results[1].ID != "q002" {
		t.Fatalf("%+v", rep)
	}
}
