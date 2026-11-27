package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
)

// searchOffered reports whether web_search can run: see searchProvider.
func (e *Engine) searchOffered() bool { return e.searchProvider() }

// searchReferee puts one web_search query to the caller's referee as the
// accepted call it answers (v5 delta section 6, pre-send check 6). It keeps
// the error that ended the call's turn budget or the step, which the search
// reports to the caller as a refusal.
type searchReferee struct {
	e      *Engine
	turnID string
	caller string
	callID string
	// recheck re-checks the step after the referee's decision.
	recheck func() error
	err     error
}

func (r *searchReferee) Allow(_ context.Context, query string) (bool, string, error) {
	out, err := r.e.dispatchReferee(refereeCall{turnID: r.turnID, caller: r.caller, callID: r.callID, name: tools.NameWebSearch, target: query})
	if err != nil {
		r.err = err
		return false, "", err
	}
	if r.recheck != nil {
		if err := r.recheck(); err != nil {
			r.err = err
			return false, "", err
		}
	}
	return out.allow, out.reason, nil
}

// exposeToSearch records file-derived bytes a participant of the turn has
// been exposed to, so a query carrying them is refused (the canary).
func (e *Engine) exposeToSearch(t *openTurn, route tools.Route, b []byte) {
	if len(b) == 0 || !e.searchOffered() {
		return
	}
	e.searchScreenerOf(t).AddBytes(route, b)
}

// exposeToSearchLocked is exposeToSearch with e.mu held.
func (e *Engine) exposeToSearchLocked(t *openTurn, route tools.Route, b []byte) {
	if len(b) == 0 || !e.searchOffered() {
		return
	}
	e.searchScreenerLocked(t).AddBytes(route, b)
}

// exposeStagedBases records the base content of every path the turn has
// staged, read from the journal, as exposed. Each base is added once.
func (e *Engine) exposeStagedBases(t *openTurn) {
	if !e.searchOffered() {
		return
	}
	entries, err := e.store.Staging(t.id)
	if err != nil {
		log.Printf("engine: turn %s: staged bases unreadable for the search canary: %v", t.id, err)
		return
	}
	for _, s := range entries {
		if s.BaseSHA256 == "" {
			continue
		}
		e.mu.Lock()
		seen := t.searchBases[s.BaseSHA256]
		if t.searchBases == nil {
			t.searchBases = map[string]bool{}
		}
		t.searchBases[s.BaseSHA256] = true
		e.mu.Unlock()
		if seen {
			continue
		}
		base, err := e.store.Content(s.BaseSHA256)
		if err != nil {
			log.Printf("engine: turn %s: staged base unreadable for the search canary: %v", t.id, err)
			e.mu.Lock()
			delete(t.searchBases, s.BaseSHA256)
			e.mu.Unlock()
			continue
		}
		e.exposeToSearch(t, tools.RouteBase, base)
	}
}

// searchScreenerOf is the turn's screener for queries, with the roster's
// allowlist set.
func (e *Engine) searchScreenerOf(t *openTurn) *tools.URLScreener {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.searchScreenerLocked(t)
}

// searchScreenerLocked is searchScreenerOf with e.mu held. The screener is
// created seeded with the answer and change fields of the turn's history
// document, which is frozen at submit; its input fields are the user's own
// words and are not canary material.
func (e *Engine) searchScreenerLocked(t *openTurn) *tools.URLScreener {
	if t.searchScreener == nil {
		max := 0
		if e.roster != nil && e.roster.Tools != nil && e.roster.Tools.MaxFetchesPerTurn != nil {
			max = *e.roster.Tools.MaxFetchesPerTurn
		}
		s := tools.NewURLScreener(max)
		if e.roster != nil && e.roster.Tools != nil {
			// A bad allowlist already leaves search unoffered.
			policy, _ := tools.HostPolicyFromConfig(e.roster.Tools.Web)
			s.SetHostPolicy(policy)
		}
		if t.history != nil {
			for _, h := range t.history.Included() {
				s.AddBytes(tools.RouteHistory, h.Answer)
				for _, c := range h.Changes {
					s.AddBytes(tools.RouteHistory, []byte(c.Outcome+" "+c.Path))
				}
			}
		}
		t.searchScreener = s
	}
	return t.searchScreener
}

// searchClient builds the client of one call: the pinned searcher over the
// injected provider, the system resolver (or the injected one) and the
// credential resolver. A failure to build it is a client that fails, so the
// call is still answered.
func (e *Engine) searchClient(scr *tools.URLScreener, ref tools.Referee) tools.SearchClient {
	var (
		c   tools.SearchClient
		err error
	)
	if e.searchClientFn != nil {
		c, err = e.searchClientFn(scr, ref)
	} else {
		c, err = e.pinnedSearcher(scr, ref)
	}
	if err != nil {
		return failedSearchClient{err}
	}
	return c
}

func (e *Engine) pinnedSearcher(scr *tools.URLScreener, ref tools.Referee) (tools.SearchClient, error) {
	cfg := e.roster.Tools.Web.Search
	var res tools.Resolver = net.DefaultResolver
	if e.searchResolver != nil {
		res = e.searchResolver
	}
	var secrets config.SecretResolver = config.NewEnvFileResolver(e.secretsDir)
	if e.searchSecrets != nil {
		secrets = e.searchSecrets
	}
	return tools.NewPinnedSearcher(scr, ref, res, e.searchProv, *cfg.Auth, secrets)
}

type failedSearchClient struct{ err error }

func (f failedSearchClient) Search(context.Context, string) (*http.Response, error) {
	return nil, f.err
}

// webSearchEffect runs web_search calls of the turn's author. The search is
// bounded by the turn's deadline. A turn budget ended by, or a step ended
// during, the referee's decision abandons the call: nothing is sent and the
// call has no result.
func (e *Engine) webSearchEffect(t *openTurn, attemptID string, hook func(turnID, attemptID, name string)) toolEffect {
	scr := e.searchScreenerOf(t)
	return func(call journal.ToolCall, args json.RawMessage, recheck func() error) (tools.Result, error) {
		if hook != nil {
			hook(t.id, attemptID, call.Name)
			if err := recheck(); err != nil {
				return tools.Result{}, err
			}
		}
		// The turn's deadline is on the engine's clock, so the time left is
		// measured there and not against the wall clock.
		ctx, cancel := context.WithTimeout(context.Background(), t.turnDeadline.Sub(e.clock.Now()))
		defer cancel()
		ref := &searchReferee{e: e, turnID: t.id, caller: t.author, callID: call.ID, recheck: recheck}
		client := &abandoningClient{inner: e.searchClient(scr, ref), ref: ref, cancel: cancel}
		res, err := tools.NewWebSearchTools(client, e.store, t.id, attemptID, e.maxResultBytes()).WithRecheck(recheck).WebSearch(ctx, call.ID, args)
		if err != nil && ref.err != nil && errors.Is(ref.err, errToolAbandoned) {
			return tools.Result{}, ref.err
		}
		return res, err
	}
}

// abandoningClient cancels the call's context when the referee's decision
// could not be made because the step or the turn's budget ended, so that the
// search returns without journaling a result.
type abandoningClient struct {
	inner  tools.SearchClient
	ref    *searchReferee
	cancel context.CancelFunc
}

func (c *abandoningClient) Search(ctx context.Context, query string) (*http.Response, error) {
	resp, err := c.inner.Search(ctx, query)
	if err != nil && c.ref.err != nil && errors.Is(c.ref.err, errToolAbandoned) {
		c.cancel()
		return nil, ctx.Err()
	}
	return resp, err
}
