package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/history"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
)

// Engine-side wiring of web_search (v5 delta section 6, "The tools" and
// pre-send check 6). The tool is offered only when the roster configures a
// provider under [tools.web.search], a provider implementation of that name
// was injected through Options.SearchProviders, and the provider's host
// passes the pre-send checks and the allowlist. The query route is the
// fetch route: the turn's screener (canary, shape, allowlist, counts), the
// referee of the caller's endpoint through the engine's accounting, with the
// decision accounted to the caller and its payload listing only the caller's
// own earlier web calls this turn, the step's re-checks,
// then resolution of the provider host under the address rule and a pinned
// connection. The credential is resolved at send time, for that request
// only, and never journaled; the bounded raw result is journaled against the
// call.

// Reasons web_search is not offered. A detail follows a colon where one is
// given.
const (
	searchOffNoConfig    = "no_search_provider"          // [tools.web.search] is not set
	searchOffUnsupported = "search_provider_unsupported" // :<provider> no implementation of that name was injected
	searchOffHost        = "search_host_refused"         // :<rule> the host fails a pre-send check or the allowlist
)

// searchConfig is [tools.web.search], nil when the roster sets none.
func (e *Engine) searchConfig() *config.SearchConfig {
	if e.roster == nil || e.roster.Tools == nil || e.roster.Tools.Web == nil {
		return nil
	}
	return e.roster.Tools.Web.Search
}

// searchOffReason is why web_search is not offered, or empty when it may be:
// the roster must configure a provider, an implementation of that name must
// have been injected, and the provider's host must pass the shape rules and
// the allowlist. It reads the roster only, so it holds whether or not e.mu
// is.
func (e *Engine) searchOffReason() string {
	sc := e.searchConfig()
	if sc == nil {
		return searchOffNoConfig
	}
	if e.searchProviders[sc.Provider] == nil {
		return searchOffUnsupported + ":" + sc.Provider
	}
	policy, err := tools.HostPolicyFromConfig(e.roster.Tools.Web)
	if err != nil {
		return searchOffHost + ":" + tools.RuleAllowlist
	}
	if r := tools.SearchHostAllowed(sc.Host, policy); r != nil {
		return searchOffHost + ":" + r.Rule
	}
	return ""
}

// searchOffered reports whether web_search is among the definitions a role
// with the web group receives.
func (e *Engine) searchOffered() bool { return e.searchOffReason() == "" }

// groupDefs returns the definitions a role holding the given groups is
// offered: tools.ForGroups with web_search included only when it is offered,
// and without web_fetch, which has no effect in the engine yet and so must
// not be advertised.
func (e *Engine) groupDefs(groups []string) ([]transport.Tool, error) {
	defs, err := tools.ForGroups(groups, e.searchOffered())
	if err != nil {
		return nil, err
	}
	out := defs[:0]
	for _, d := range defs {
		if d.Function.Name == tools.NameWebFetch {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// newTurnScreener returns the turn's screener: one per turn, shared by every
// call of the turn, with the roster's fetch bound and allowlist. e.mu is held.
func (e *Engine) newTurnScreener() (*tools.URLScreener, error) {
	maxPerTurn := 0
	var web *config.WebConfig
	if e.roster != nil && e.roster.Tools != nil {
		if e.roster.Tools.MaxFetchesPerTurn != nil {
			maxPerTurn = *e.roster.Tools.MaxFetchesPerTurn
		}
		web = e.roster.Tools.Web
	}
	s := tools.NewURLScreener(maxPerTurn)
	policy, err := tools.HostPolicyFromConfig(web)
	if err != nil {
		return nil, err
	}
	s.SetHostPolicy(policy)
	return s, nil
}

// seedHistoryCorpus adds to the screener the file-derived bytes the history
// document carries: the answer of every included turn that is itself
// file-tainted, and the change fields of such a turn (the design's canary
// corpus names "the answer and change fields of the history document", and
// only a tainted turn's can carry workspace bytes). Input fields are the
// user's own words and are never added. The change fields are read from the
// journal's change set of the turn, in the form history writes them, so
// they are covered whether or not the rendered document carries them yet.
// It runs in the submit transaction.
func seedHistoryCorpus(tx *journal.Tx, s *tools.URLScreener, doc *history.Document, tainted map[string]bool) error {
	for _, turn := range doc.Included() {
		if !tainted[turn.ID] {
			continue
		}
		s.AddBytes(tools.RouteHistory, turn.Answer)
		for _, c := range turn.Changes {
			s.AddBytes(tools.RouteHistory, history.ChangeField(c))
		}
		cs, err := tx.ChangeSetForTurn(turn.ID)
		if errors.Is(err, journal.ErrNoChangeSet) {
			continue
		}
		if err != nil {
			return err
		}
		for _, p := range cs.Paths {
			s.AddBytes(tools.RouteHistory, history.ChangeField(history.Change{Path: p.Path, Outcome: historyOutcome(p.Outcome)}))
		}
	}
	return nil
}

// historyOutcome maps a change-set path's journaled outcome to the form
// history writes: pending is "not applied".
func historyOutcome(journaled string) string {
	switch journaled {
	case journal.PathApplied:
		return history.OutcomeApplied
	case journal.PathConflict:
		return history.OutcomeConflict
	}
	return history.OutcomeNotApplied
}

// exposeFileBytes adds to the turn's screener the file-derived bytes an
// executed file-tool call carried to the model: what a read tool returned,
// the content a write_file staged, the old and new text of an edit_file, and
// the base content of every staged path. Model-authored arguments that are
// not file content (paths, patterns, unstage) are not canary material. It
// runs after the call's effect, with e.mu not held. An error reading the
// staged bases is returned as the step's error: the canary must not fail
// open by leaving bytes out of the corpus.
func (e *Engine) exposeFileBytes(t *openTurn, name string, args json.RawMessage, res tools.Result) error {
	if t.screener == nil || res.ErrorCode != "" {
		return nil
	}
	switch name {
	case tools.NameReadFile, tools.NameListDir, tools.NameSearchFiles:
		t.screener.AddBytes(tools.RouteSnapshotRead, res.Content)
		return nil
	case tools.NameWriteFile:
		var a struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(args, &a) == nil {
			t.screener.AddBytes(tools.RouteStagedWrite, []byte(a.Content))
		}
	case tools.NameEditFile:
		var a struct {
			Old string `json:"old"`
			New string `json:"new"`
		}
		if json.Unmarshal(args, &a) == nil {
			t.screener.AddBytes(tools.RouteStagedWrite, []byte(a.Old))
			t.screener.AddBytes(tools.RouteStagedWrite, []byte(a.New))
		}
	default:
		return nil
	}
	// A staged path's base: the diff a reviewer is given shows it, and the
	// design lists it as corpus material whether or not the author read it.
	err := e.store.Tx(func(tx *journal.Tx) error {
		staged, err := tx.Staging(t.id)
		if err != nil {
			return err
		}
		for _, s := range staged {
			if s.BaseSHA256 == "" {
				continue
			}
			base, err := tx.Content(s.BaseSHA256)
			if err != nil {
				return err
			}
			t.screener.AddBytes(tools.RouteBase, base)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("engine: canary corpus of staged bases after %s: %w", name, err)
	}
	return nil
}

// callReferee is the tools.Referee of one web call: it puts the call to the
// endpoint referee through the engine's accounting (dispatchReferee), with
// the caller's slot held. An error from the dispatch is returned as is, so an
// ended turn abandons the call rather than denying it.
type callReferee struct {
	e *Engine
	c refereeCall
}

func (r callReferee) Allow(_ context.Context, target string) (bool, string, error) {
	c := r.c
	c.target = target
	out, err := r.e.dispatchReferee(c)
	if err != nil {
		return false, "", err
	}
	return out.allow, out.reason, nil
}

// webSearchEffect is the effect of web_search for the attempt's step of
// caller, the author or a required reviewer: it builds the searcher over the
// turn's screener, the referee of the caller's endpoint (the call is put,
// accounted and journaled as the caller's, with the caller's own earlier
// web calls as its prior calls), the configured provider and the credential
// source, bounds the request by the turn's remaining time on the engine's
// clock, and runs the tool, which journals the result. The hook stands in
// for a wait inside the effect, as for the file tools.
func (e *Engine) webSearchEffect(t *openTurn, caller, attemptID string, hook func(turnID, attemptID, name string)) toolEffect {
	return func(call journal.ToolCall, args json.RawMessage, recheck func() error) (tools.Result, error) {
		if hook != nil {
			hook(t.id, attemptID, call.Name)
			if err := recheck(); err != nil {
				return tools.Result{}, err
			}
		}
		sc := e.searchConfig()
		if sc == nil || sc.Auth == nil || t.screener == nil {
			return tools.Result{}, errors.New("engine: web_search effect without a configured provider")
		}
		provider := e.searchProviders[sc.Provider]
		auth := *sc.Auth
		cred := func() (string, error) { return e.secrets.Resolve(auth) }
		ref := callReferee{e: e, c: refereeCall{turnID: t.id, caller: caller, callID: call.ID, name: tools.NameWebSearch}}
		searcher, err := tools.NewPinnedSearcher(t.screener, ref, provider, sc.Host, cred, e.searchNet)
		if err != nil {
			return tools.Result{}, err
		}
		ctx, cancel := e.turnContext(t)
		defer cancel()
		return tools.NewWebSearchTools(e.store, searcher, e.maxResultBytes()).Search(ctx, call.ID, args, recheck)
	}
}

// turnContext is a context that ends with the turn's deadline, measured on
// the engine's clock: the request gets what is left of the turn and no more.
func (e *Engine) turnContext(t *openTurn) (context.Context, context.CancelFunc) {
	remaining := t.turnDeadline.Sub(e.clock.Now())
	if remaining < 0 {
		remaining = 0
	}
	return context.WithTimeout(context.Background(), remaining)
}
