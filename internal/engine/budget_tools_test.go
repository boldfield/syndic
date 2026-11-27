package engine_test

import (
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/prompt"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// withPresetTools gives the named preset of RosterTOML the author and
// reviewer tool groups. An empty list leaves the field unset.
func withPresetTools(t *testing.T, toml, preset string, author, reviewer []string) string {
	t.Helper()
	header := "[presets." + preset + "]\n"
	if !strings.Contains(toml, header) {
		t.Fatalf("roster has no preset %q", preset)
	}
	lines := header
	if len(author) > 0 {
		lines += `tools = ["` + strings.Join(author, `", "`) + "\"]\n"
	}
	if len(reviewer) > 0 {
		lines += `reviewer_tools = ["` + strings.Join(reviewer, `", "`) + "\"]\n"
	}
	return strings.Replace(toml, header, lines, 1)
}

func definitions(t *testing.T, groups ...string) []transport.Tool {
	t.Helper()
	defs, err := tools.ForGroups(groups, false)
	if err != nil || len(defs) == 0 {
		t.Fatalf("ForGroups(%v) = %v, %v", groups, defs, err)
	}
	return defs
}

func definitionsEstimate(defs []transport.Tool) int {
	return budget.RequestEstimateTokens(nil, defs, testBytesPerToken, budget.Overheads{})
}

func toolSubmit(h *enginetest.Harness, preset string) (wire.SubmitReply, error) {
	return h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: testInput, Workspace: "/work", Preset: ptrStr(preset)})
}

// The definitions alone can cause a refusal: the author's messages fit the
// context bound and the messages plus the read definitions do not.
func TestBudgetToolDefinitionsRefusedAtSubmit(t *testing.T) {
	const authorOut = 4096
	defs := definitions(t, tools.GroupRead)
	msgs := prompt.Author(testInput)
	bare := budget.AdmissionTokens(msgs, testTokensPerByte, testOverheads)
	defsAdm := budget.RequestAdmissionTokens(nil, defs, testTokensPerByte, budget.Overheads{})
	if got := budget.RequestAdmissionTokens(msgs, defs, testTokensPerByte, testOverheads); got != bare+defsAdm {
		t.Fatalf("admission with definitions = %d, want %d", got, bare+defsAdm)
	}
	// No margin, so the context bound is the usable bound.
	base := withBudgets(enginetest.RosterTOML, "context_margin = 0.0")
	withRead := withPresetTools(t, base, "solo", []string{tools.GroupRead}, nil)
	fits := bare + authorOut

	run := func(roster string, bound int) error {
		h := enginetest.New(t, roster, map[string][]fake.Step{"author": {enginetest.Reply(authorText)}},
			enginetest.WithContextBound("author", bound), enginetest.WithToolCalls("author", transport.TriYes))
		_, err := toolSubmit(h, "solo")
		return err
	}

	if err := run(base, fits); err != nil {
		t.Fatalf("without tools at bound %d: %v", fits, err)
	}
	for _, bound := range []int{fits, fits + defsAdm - 1} {
		we := wantBudgetError(t, run(withRead, bound), "context")
		if we.Participant == nil || *we.Participant != "author" || we.Limit == nil || *we.Limit != int64(bound) ||
			we.Admission == nil || *we.Admission != int64(bare+defsAdm+authorOut) {
			t.Fatalf("bound %d: error = %+v", bound, we)
		}
	}
	if err := run(withRead, fits+defsAdm); err != nil {
		t.Fatalf("with read at bound %d: %v", fits+defsAdm, err)
	}
}

// Each projected author and review dispatch carries its role's definitions;
// a repair and a delivery carry none.
func TestBudgetProjectionCountsRoleDefinitions(t *testing.T) {
	roster := strings.Replace(enginetest.RosterTOML, "max_rounds = 0", "max_rounds = 1", 1)
	withTools := withPresetTools(t, roster, "review", []string{tools.GroupRead, tools.GroupWrite}, []string{tools.GroupRead})
	authorDefs := definitionsEstimate(definitions(t, tools.GroupRead, tools.GroupWrite))
	reviewerDefs := definitionsEstimate(definitions(t, tools.GroupRead))

	project := func(roster string) recordView {
		h := enginetest.New(t, roster, nil,
			enginetest.WithToolCalls("author", transport.TriYes), enginetest.WithToolCalls("reviewer", transport.TriYes))
		reply, err := toolSubmit(h, "review")
		if err != nil {
			t.Fatal(err)
		}
		return budgetOf(t, h, reply.TurnID)
	}
	bare, tooled := project(roster), project(withTools)
	if got, want := tooled.Projected-bare.Projected, authorDefs+reviewerDefs; got != want {
		t.Errorf("first round grew by %d, want %d (author %d + reviewer %d)", got, want, authorDefs, reviewerDefs)
	}
	// One revision round adds one author revision and one review.
	if got, want := tooled.WorstCaseProjected-bare.WorstCaseProjected, 2*(authorDefs+reviewerDefs); got != want {
		t.Errorf("worst case grew by %d, want %d", got, want)
	}
}

// reserve meters the definitions it is given by their estimate.
func TestBudgetReserveChargesDefinitions(t *testing.T) {
	// The far-future clock keeps the hanging author's deadline ahead of the
	// wall clock, so the turn stays open.
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {{Kind: fake.StepKindHang}}},
		enginetest.ClockStart(farFuture))
	reply, err := submitInput(t, h, "s1", testInput)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(h.Adapters["author"].Calls()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("author never dispatched")
		}
		time.Sleep(2 * time.Millisecond)
	}
	before := budgetOf(t, h, reply.TurnID).Charged

	defs := definitions(t, tools.GroupRead, tools.GroupWeb)
	msgs := prompt.Author(testInput)
	const maxOut = 100
	bareEst, bareCharged, err := h.Engine.ReserveOpen("reviewer", msgs, nil, maxOut)
	if err != nil {
		t.Fatal(err)
	}
	est, charged, err := h.Engine.ReserveOpen("reviewer", msgs, defs, maxOut)
	if err != nil {
		t.Fatal(err)
	}
	if bareEst != estimate(msgs) || bareCharged-before != bareEst+maxOut {
		t.Fatalf("without definitions: estimate %d, charged %d; want %d, %d", bareEst, bareCharged-before, estimate(msgs), estimate(msgs)+maxOut)
	}
	if est-bareEst != definitionsEstimate(defs) {
		t.Errorf("estimate with definitions = %d, want %d + %d", est, bareEst, definitionsEstimate(defs))
	}
	if got := charged - bareCharged; got != bareEst+definitionsEstimate(defs)+maxOut {
		t.Errorf("charge with definitions = %d, want %d", got, bareEst+definitionsEstimate(defs)+maxOut)
	}
}

// Each dispatch is reserved at the estimate of exactly the request it sends:
// the author's and a reviewer's first request carry the read definitions their
// preset gives them over the conversation's workspace, and the repair and the
// deliveries carry none.
func TestBudgetDispatchesMeterWhatTheySend(t *testing.T) {
	t.Cleanup(engine.EnableDelivery())
	roster := withPresetTools(t, enginetest.RosterTOML, "review", []string{tools.GroupRead}, []string{tools.GroupRead})
	h := enginetest.New(t, roster, map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText), ack()},
		"reviewer": {enginetest.Reply(fake.VerdictProseApprove()), approveStep(), ack()},
	}, enginetest.WithToolCalls("author", transport.TriYes), enginetest.WithToolCalls("reviewer", transport.TriYes))
	reply, err := toolSubmit(h, "review")
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	waitDeliveries(t, h, reply.TurnID, 2)

	phases := map[string]int{}
	for _, id := range []string{"author", "reviewer"} {
		for _, call := range h.Adapters[id].Calls() {
			wantDefs := 0
			row := getAttempt(t, h, call.Request.Envelope.AttemptID)
			if call.Request.Envelope.Phase == "author" || (row.Phase == "review" && row.RepairOf == nil) {
				wantDefs = len(definitions(t, tools.GroupRead))
			}
			if len(call.Request.Tools) != wantDefs {
				t.Errorf("%s %s request carries %d definitions, want %d", id, call.Request.Envelope.Phase, len(call.Request.Tools), wantDefs)
			}
			want := estimate(call.Request.Messages)
			if len(call.Request.Tools) > 0 {
				want += definitionsEstimate(call.Request.Tools)
			}
			if row.EstimatedPromptTokens == nil || *row.EstimatedPromptTokens != want {
				t.Errorf("%s %s estimate = %v, want %d", id, row.Phase, row.EstimatedPromptTokens, want)
			}
			phases[row.Phase]++
		}
	}
	if phases["author"] != 1 || phases["review"] != 2 || phases[phaseDelivery] != 2 {
		t.Fatalf("dispatches by phase = %v", phases)
	}
}
