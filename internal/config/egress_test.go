package config

import (
	"reflect"
	"strings"
	"testing"
)

// egressBaseRoster is makeBaseRoster with the reviewer on a LAN endpoint with
// remote execution, outside any file egress destination.
func egressBaseRoster() *Roster {
	r := makeBaseRoster()
	r.Endpoints = append(r.Endpoints, EndpointProfile{
		Name: "lan", Dialect: "chat_completions_v1", DeclaredLocation: "lan",
		DeclaredExecution: "remote", DeclaredBilling: "none", NetworkDependency: "none",
		RefereeModel: "ref",
	})
	r.Participants[1].Endpoint = "lan"
	return r
}

func TestFileRoutes(t *testing.T) {
	base := Preset{Mode: "review", Author: "a", RequiredReviewers: []string{"r1", "r2"}}
	with := func(author, reviewer []string) Preset {
		p := base
		p.Tools, p.ReviewerTools = author, reviewer
		return p
	}
	routes := func(author string, reviewers ...string) []FileRoute {
		out := []FileRoute{{"a", author}}
		for i, r := range reviewers {
			out = append(out, FileRoute{base.RequiredReviewers[i], r})
		}
		return out
	}
	tests := []struct {
		name    string
		preset  Preset
		tainted bool
		want    []FileRoute
	}{
		{"no tools", base, false, nil},
		{"web only", with([]string{"web"}, []string{"web"}), false, nil},
		{"author read", with([]string{"read"}, nil), false, routes(EgressRouteRead, EgressRouteEvidence, EgressRouteEvidence)},
		{"write-only author", with([]string{"write"}, nil), false, routes(EgressRouteWrite, EgressRouteDiff, EgressRouteDiff)},
		{"author read and write", with([]string{"read", "write"}, nil), false, routes(EgressRouteRead, EgressRouteDiff, EgressRouteDiff)},
		{"reviewer read", with(nil, []string{"read"}), false, routes(EgressRouteVerdict, EgressRouteRead, EgressRouteRead)},
		{"tainted, no tools", base, true, routes(EgressRouteHistory, EgressRouteHistory, EgressRouteHistory)},
		{"tainted, web only", with([]string{"web"}, nil), true, routes(EgressRouteHistory, EgressRouteHistory, EgressRouteHistory)},
		{"tainted, author write", with([]string{"write"}, nil), true, routes(EgressRouteHistory, EgressRouteHistory, EgressRouteHistory)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FileRoutes(tt.preset, tt.tainted); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("FileRoutes = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFileEgressAllowed(t *testing.T) {
	r := egressBaseRoster()
	r.Participants = append(r.Participants,
		Participant{ID: "cli", Route: "cli"},
		Participant{ID: "bare", Route: "api"})
	for id, want := range map[string]bool{"author": true, "reviewer": false, "cli": false, "bare": false, "undeclared": true} {
		if got := r.FileEgressAllowed(id); got != want {
			t.Errorf("FileEgressAllowed(%q) = %v, want %v", id, got, want)
		}
	}
	r.Tools = &ToolsConfig{FileEgressDestinations: []string{"lan"}}
	if !r.FileEgressAllowed("reviewer") {
		t.Error("a participant on a named destination is refused")
	}
}

// A write-only author still sends its diffs to every reviewer, so a reviewer
// outside the destinations is refused at load, named with the diff route.
func TestValidateWriteOnlyAuthorDiffRoute(t *testing.T) {
	r := egressBaseRoster()
	p := r.Presets["review"]
	p.Tools = []string{"write"}
	r.Presets["review"] = p
	err := r.Validate()
	if err == nil {
		t.Fatal("Validate() = nil for a write-only author with a reviewer outside the destinations")
	}
	for _, want := range []string{`preset "review"`, `participant "reviewer"`, `route "diff"`, "requires file egress"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), `participant "author"`) {
		t.Errorf("error names the local author: %v", err)
	}

	r.Tools = &ToolsConfig{FileEgressDestinations: []string{"lan"}}
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate() with the reviewer's endpoint a destination = %v", err)
	}
}

// Roster load and submit hold a preset to one rule: the load check is the
// untainted case of the violations a submit computes.
func TestFileEgressViolationsMatchLoad(t *testing.T) {
	for _, tools := range [][]string{nil, {"web"}, {"read"}, {"write"}} {
		r := egressBaseRoster()
		p := r.Presets["review"]
		p.Tools = tools
		r.Presets["review"] = p
		bad := r.FileEgressViolations(p, false)
		err := r.Validate()
		if (len(bad) > 0) != (err != nil && strings.Contains(err.Error(), "requires file egress")) {
			t.Errorf("tools %v: violations %v, load %v", tools, bad, err)
		}
		tainted := r.FileEgressViolations(p, true)
		if want := []FileRoute{{"reviewer", EgressRouteHistory}}; !reflect.DeepEqual(tainted, want) {
			t.Errorf("tools %v tainted: violations %v, want %v", tools, tainted, want)
		}
	}
}

func TestWebWithFileViolation(t *testing.T) {
	off, on := false, true
	r := makeBaseRoster()
	r.Tools = &ToolsConfig{AllowWebWithRead: &off}
	webOnly := Preset{Mode: "review", Author: "author", RequiredReviewers: []string{"reviewer"}, ReviewerTools: []string{"web"}}
	if got := r.WebWithFileViolation(webOnly, false); got != nil {
		t.Fatalf("web-only untainted = %v, want nil", got)
	}
	if got := r.WebWithFileViolation(webOnly, true); !reflect.DeepEqual(got, []string{"reviewer"}) {
		t.Fatalf("web-only tainted = %v, want [reviewer]", got)
	}
	writeAndWeb := webOnly
	writeAndWeb.Tools = []string{"write"}
	if got := r.WebWithFileViolation(writeAndWeb, false); !reflect.DeepEqual(got, []string{"reviewer"}) {
		t.Fatalf("write author, web reviewer = %v, want [reviewer]", got)
	}
	noWeb := Preset{Mode: "review", Author: "author", RequiredReviewers: []string{"reviewer"}}
	if got := r.WebWithFileViolation(noWeb, true); got != nil {
		t.Fatalf("no web tainted = %v, want nil", got)
	}
	webOnly.AllowWebWithRead = &on
	if got := r.WebWithFileViolation(webOnly, true); got != nil {
		t.Fatalf("preset override on = %v, want nil", got)
	}
	r.Tools.AllowWebWithRead = nil
	webOnly.AllowWebWithRead = nil
	if got := r.WebWithFileViolation(webOnly, true); got != nil {
		t.Fatalf("default = %v, want nil", got)
	}
}
