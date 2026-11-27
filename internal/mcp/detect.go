package mcp

// This file decides which revision a server speaks (v9 delta, section 3,
// "Choosing the revision"). With protocol = "auto" it sends server/discover as
// a current-revision request and nothing else; with a named revision it sends
// nothing. The decision is a value; keeping it, and acting on a later
// contradiction, is the caller's.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// The two revisions detection chooses between. This package carries no
// network code, so it does not import the clients that send; package client
// has DiscoverHTTP and DiscoverStdio, which do.
const (
	CurrentRevision = "2026-07-28"
	LegacyRevision  = "2025-11-25"
)

// ProtocolAuto is the protocol setting that asks for detection.
const ProtocolAuto = "auto"

// ReasonNoCommonRevision is the reason shown when a server lists no revision
// this client speaks.
const ReasonNoCommonRevision = "no common revision"

// Outcome is what detection decided.
type Outcome int

const (
	// OutcomeCurrent means the server is used under the current revision.
	OutcomeCurrent Outcome = iota + 1
	// OutcomeLegacy means the legacy initialize is to be sent next, over the
	// same stream or to the same URL, and the server used under the legacy
	// revision.
	OutcomeLegacy
	// OutcomeUnoffered means the server is not offered for the turn. No
	// initialize follows.
	OutcomeUnoffered
)

func (o Outcome) String() string {
	switch o {
	case OutcomeCurrent:
		return "current"
	case OutcomeLegacy:
		return "legacy"
	case OutcomeUnoffered:
		return "unoffered"
	}
	return "unknown"
}

// Decision is the result of choosing a revision.
type Decision struct {
	Outcome Outcome
	// Revision is the revision to speak when Outcome is current or legacy.
	Revision string
	// Reason is why the server is unoffered: ReasonNoCommonRevision, or the
	// text of Err.
	Reason string
	// Err is the failure as it is, when the server is unoffered because of
	// one. It is nil for ReasonNoCommonRevision.
	Err error
	// Detected is false when the protocol named a revision and nothing was
	// sent.
	Detected bool
}

func current() Decision {
	return Decision{Outcome: OutcomeCurrent, Revision: CurrentRevision, Detected: true}
}

func legacy() Decision {
	return Decision{Outcome: OutcomeLegacy, Revision: LegacyRevision, Detected: true}
}

func noCommon() Decision {
	return Decision{Outcome: OutcomeUnoffered, Reason: ReasonNoCommonRevision, Detected: true}
}

func failed(err error) Decision {
	return Decision{Outcome: OutcomeUnoffered, Reason: err.Error(), Err: err, Detected: true}
}

// Named returns the decision for a protocol setting that names a revision,
// detecting nothing. ok is false for "auto", which needs detection; an
// error is returned for anything else.
func Named(protocol string) (d Decision, ok bool, err error) {
	switch protocol {
	case ProtocolAuto:
		return Decision{}, false, nil
	case CurrentRevision:
		return Decision{Outcome: OutcomeCurrent, Revision: protocol}, true, nil
	case LegacyRevision:
		return Decision{Outcome: OutcomeLegacy, Revision: protocol}, true, nil
	}
	return Decision{}, false, fmt.Errorf("mcp: unknown protocol %q", protocol)
}

// JSON-RPC error codes the current revision defines.
const (
	codeInvalidParams      = -32602
	codeMethodNotFound     = -32601
	codeHeaderMismatch     = -32020
	codeMissingCapability  = -32021
	codeUnsupportedVersion = -32022
)

// Answer is what came back for server/discover.
type Answer struct {
	// Status is the HTTP status; zero over stdio.
	Status int
	// Result is the JSON-RPC result, when the answer is one.
	Result json.RawMessage
	// Error is the JSON-RPC error, when the answer is one.
	Error *AnswerError
	// Err says why no JSON-RPC answer could be read from a response that
	// carried a status, when that is known. It may be nil.
	Err error
}

// AnswerError is a JSON-RPC error object.
type AnswerError struct {
	Code    int
	Message string
	Data    json.RawMessage
}

func (e *AnswerError) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

// StatusFailure is the failure reported for an HTTP status that is no
// evidence of era.
type StatusFailure struct {
	Status int
	Err    error
}

func (e *StatusFailure) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("mcp: HTTP status %d: %v", e.Status, e.Err)
	}
	return fmt.Sprintf("mcp: HTTP status %d", e.Status)
}

func (e *StatusFailure) Unwrap() error { return e.Err }

// SendError wraps a failure to send server/discover at all. Over stdio every
// other error shows a legacy server; this one does not.
type SendError struct{ Err error }

func (e *SendError) Error() string { return "mcp: sending server/discover: " + e.Err.Error() }
func (e *SendError) Unwrap() error { return e.Err }

// currentError reports whether e is an error the current revision defines.
func currentError(e *AnswerError) bool {
	if e == nil {
		return false
	}
	switch e.Code {
	case codeInvalidParams, codeMethodNotFound, codeHeaderMismatch, codeMissingCapability, codeUnsupportedVersion:
		return true
	}
	return false
}

// fromVersions decides from the versions an answer lists.
func fromVersions(versions []string) Decision {
	has := func(v string) bool {
		for _, s := range versions {
			if s == v {
				return true
			}
		}
		return false
	}
	switch {
	case has(CurrentRevision):
		return current()
	case has(LegacyRevision):
		return legacy()
	}
	return noCommon()
}

// fromAnswer decides from an answer that is a discovery result or a
// current-revision error. ok is false when it is neither.
func fromAnswer(a Answer) (Decision, bool) {
	if a.Error != nil {
		if !currentError(a.Error) {
			return Decision{}, false
		}
		var data struct {
			Supported []string `json:"supported"`
		}
		if len(a.Error.Data) > 0 {
			_ = json.Unmarshal(a.Error.Data, &data)
		}
		return fromVersions(data.Supported), true
	}
	if a.Result == nil {
		return Decision{}, false
	}
	var res struct {
		Supported []string `json:"supportedVersions"`
	}
	if err := json.Unmarshal(a.Result, &res); err != nil || res.Supported == nil {
		return failed(errors.New("mcp: malformed server/discover result")), true
	}
	return fromVersions(res.Supported), true
}

// legacyStatus reports whether a status alone shows a legacy server when the
// body is not an answer the current revision defines.
func legacyStatus(s int) bool {
	return s == 400 || s == 404 || s == 405
}

// DetectHTTP chooses the revision of an HTTP server. With protocol "auto" it
// calls send once, which must post one current-revision server/discover and
// nothing else; with a named revision it calls nothing. send returns an
// Answer for any response that carried a status, whether or not a JSON-RPC
// message could be read from it (then Result and Error are empty), and an
// error for a transport failure or a malformed or oversized success.
func DetectHTTP(ctx context.Context, protocol string, send func(context.Context) (Answer, error)) Decision {
	if d, ok, err := Named(protocol); err != nil {
		return failed(err)
	} else if ok {
		return d
	}
	a, err := send(ctx)
	if err != nil {
		return failed(err)
	}
	// A 401, a 403 and a 5xx are no evidence of era, whatever their body
	// says. Any other status carries an answer the current revision defines
	// as well as a 2xx does.
	if a.Status == 401 || a.Status == 403 || a.Status >= 500 {
		return failed(&StatusFailure{Status: a.Status, Err: statusCause(a)})
	}
	if d, ok := fromAnswer(a); ok {
		return d
	}
	if legacyStatus(a.Status) {
		return legacy()
	}
	if cause := statusCause(a); cause != nil {
		return failed(&StatusFailure{Status: a.Status, Err: cause})
	}
	return failed(&StatusFailure{Status: a.Status, Err: errors.New("no server/discover answer")})
}

// statusCause is what to report with a status that shows nothing.
func statusCause(a Answer) error {
	if a.Error != nil {
		return a.Error
	}
	return a.Err
}

// DetectStdio chooses the revision of a stdio server. With protocol "auto"
// it calls exchange in the background, which must write one current-revision
// server/discover and read the answer; with a named revision it calls
// nothing. Any answer that is not a discovery result or a current-revision
// error, any read error, and no answer within timeout show a legacy server.
// A failure wrapped in SendError shows nothing. A zero timeout waits without
// bound.
//
// Of the current revision's errors, unknown method (-32601) shows a legacy
// server here, since a legacy server answers an unknown method that way.
//
// After a timeout the exchange's read may still be pending. client.DiscoverStdio
// hands the stream on safely; a caller of DetectStdio must do so itself.
func DetectStdio(protocol string, timeout time.Duration, exchange func() (Answer, error)) Decision {
	if d, ok, err := Named(protocol); err != nil {
		return failed(err)
	} else if ok {
		return d
	}
	type result struct {
		a   Answer
		err error
	}
	ch := make(chan result, 1)
	go func() {
		a, err := exchange()
		ch <- result{a, err}
	}()
	var expire <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		expire = t.C
	}
	select {
	case r := <-ch:
		var se *SendError
		if errors.As(r.err, &se) {
			return failed(r.err)
		}
		if r.err != nil || (r.a.Error != nil && r.a.Error.Code == codeMethodNotFound) {
			return legacy()
		}
		if d, ok := fromAnswer(r.a); ok {
			return d
		}
		return legacy()
	case <-expire:
		return legacy()
	}
}
