package domain

import "testing"

func TestNormalizeRemote(t *testing.T) {
	cases := map[string]string{
		"git@github.com:acme/mra-api.git":            "github.com/acme/mra-api",
		"https://github.com/acme/mra-api":            "github.com/acme/mra-api",
		"https://github.com/ACME/Mra-Api.git":        "github.com/acme/mra-api",
		"https://user@GitHub.com:443/acme/mra-api/":  "github.com/acme/mra-api",
		"ssh://git@gitlab.example.com:2222/a/b/c.git": "gitlab.example.com/a/b/c",
	}
	for in, want := range cases {
		if got := NormalizeRemote(in); got != want {
			t.Errorf("NormalizeRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTransitions(t *testing.T) {
	ok := "validated by integration test"
	long := "the root cause analysis contradicts the logs"
	cases := []struct {
		from, to          Status
		role              Role
		creator, agent    bool
		reason            string
		wantErr           string
	}{
		{Proposed, Confirmed, RoleCurator, false, false, ok, ""},
		{Proposed, Confirmed, RoleContributor, false, true, ok, CodeForbidden},
		{Proposed, Confirmed, RoleCurator, true, true, ok, CodeForbidden},
		{Proposed, Disputed, RoleContributor, false, true, long, ""},
		{Proposed, Disputed, RoleContributor, false, true, "short", CodeValidation},
		{Confirmed, Superseded, RoleAdmin, false, false, ok, CodeInvalidTransition},
		{Rejected, Confirmed, RoleAdmin, false, false, ok, CodeInvalidTransition},
	}
	for _, c := range cases {
		err := CheckTransition(c.from, c.to, c.role, c.creator, false, c.agent, c.reason)
		got := ""
		if e, ok := AsError(err); ok {
			got = e.Code
		}
		if got != c.wantErr {
			t.Errorf("%s→%s role=%v: got %q, want %q", c.from, c.to, c.role, got, c.wantErr)
		}
	}
}

func TestValidateIncidentNeedsSeverity(t *testing.T) {
	k := &Knowledge{ProjectID: "mra", Type: Incident, Title: "t", Abstract: "a", Overview: "o", Status: Proposed}
	k.Normalize()
	if err := k.Validate(); err == nil {
		t.Fatal("expected validation error for incident without severity/resolution")
	}
	k.Severity, k.Resolution = "high", "open"
	if err := k.Validate(); err != nil {
		t.Fatal(err)
	}
}
