package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
)

// Tool error codes for a call of an accepted tool step that fails one of the
// engine's checks before its effect (v5 delta section 6). Arguments that do
// not validate use tools.ErrCodeBadArguments, as the tools themselves do.
const (
	toolErrMalformed  = "malformed_call" // not a function call, or names no tool
	toolErrNotOffered = "not_offered"    // the caller's role is not given the tool
	toolErrCallLimit  = "call_limit"     // the caller has no calls remaining
)

// toolCallType is the only call type under which a call may have an effect.
const toolCallType = "function"

// errToolAbandoned is what a step's re-check returns, and so what an effect
// returns after a wait inside it found the step may have no further effect.
// An effect returning it has journaled nothing; the executor journals the
// call abandoned.
var errToolAbandoned = errors.New("tool call abandoned")

// errToolStepIDs reports a tool step whose calls do not carry distinct,
// non-empty provider ids: no result could be matched to its call, so nothing
// of the step is journaled or executed.
var errToolStepIDs = errors.New("tool step calls lack distinct ids")

// toolEffect performs one accepted, checked call's effect and journals its
// executed or error result against call, as the tools package's methods do.
// args are the call's arguments, validated and normalized. recheck re-checks
// the step; an effect must call it after every wait (a referee decision, a
// network round trip) and, when it fails, return the error at once, with no
// effect and nothing journaled.
type toolEffect func(call journal.ToolCall, args json.RawMessage, recheck func() error) (tools.Result, error)

// toolStep is one accepted tool step to execute.
type toolStep struct {
	attemptID string
	calls     []transport.ToolCall
	// granted is the tools the caller's role is given in the preset, as
	// tools.ForGroups returns them; effects holds an effect for each.
	granted []transport.Tool
	effects map[string]toolEffect
	// remaining is how many calls the caller has left under its bound.
	remaining int
}

// toolCallOutcome is what one call of the step was answered with.
type toolCallOutcome struct {
	call      journal.ToolCall
	status    string // journal.ToolCallExecuted, ToolCallError or ToolCallAbandoned
	content   []byte // the result or tool error text; nil when abandoned
	errorCode string
}

// toolStepOutcome is the executed step: one outcome per call in step order,
// the number of calls that counted against the bound, and, when a re-check
// failed, why the step ended (journal.StepEndedTurn or StepEndedEpoch). Only
// the results of a step that did not end are the next attempt's input.
type toolStepOutcome struct {
	results []toolCallOutcome
	used    int
	ended   string
}

// runToolStep executes an accepted tool step one call at a time, in the order
// returned. First every call is journaled as accepted intent, in one
// transaction, which also gates the step: an attempt that is not an accepted
// tool step (pending, late and quarantined, failed by the gate, or under any
// other status) is refused with journal.ErrNotToolStep and nothing runs. Each
// call is then checked in order: it must be a function call naming a tool,
// the tool must be granted to the role, its arguments must parse and validate
// against the tool's schema, and the caller must have a call remaining. A call
// failing a check is answered with a tool error, journaled, and the rest are
// still checked. Immediately before each effect, and after any wait inside
// it, the step is re-checked against the journal: the turn open and the
// attempt's epoch current. A call failing the re-check is journaled abandoned
// and has no effect, and so is every call after it.
func runToolStep(store *journal.Store, step toolStep) (toolStepOutcome, error) {
	granted := make(map[string]transport.Tool, len(step.granted))
	for _, tool := range step.granted {
		name := tool.Function.Name
		if step.effects[name] == nil {
			return toolStepOutcome{}, fmt.Errorf("granted tool %q has no effect", name)
		}
		granted[name] = tool
	}
	stepCalls, err := stepCalls(step)
	if err != nil {
		return toolStepOutcome{}, err
	}

	var recorded []journal.ToolCall
	if err := store.Tx(func(tx *journal.Tx) error {
		var err error
		recorded, err = tx.RecordToolStep(step.attemptID, stepCalls)
		return err
	}); err != nil {
		return toolStepOutcome{}, err
	}

	var out toolStepOutcome
	recheck := func() error {
		if out.ended != "" {
			return errToolAbandoned
		}
		var live bool
		var reason string
		if err := store.Tx(func(tx *journal.Tx) error {
			var err error
			live, reason, err = tx.ToolStepLive(step.attemptID)
			return err
		}); err != nil {
			return err
		}
		if !live {
			out.ended = reason
			return errToolAbandoned
		}
		return nil
	}
	answer := func(call journal.ToolCall, status string, content []byte, code string) error {
		if err := store.Tx(func(tx *journal.Tx) error {
			_, err := tx.RecordToolResult(call.ID, status, content, code)
			return err
		}); err != nil {
			return fmt.Errorf("journal result of call %s: %w", call.ID, err)
		}
		out.results = append(out.results, toolCallOutcome{call: call, status: status, content: content, errorCode: code})
		return nil
	}
	toolError := func(call journal.ToolCall, code, format string, args ...any) error {
		return answer(call, journal.ToolCallError, []byte(fmt.Sprintf(format, args...)), code)
	}
	abandon := func(call journal.ToolCall) error {
		return answer(call, journal.ToolCallAbandoned, nil, "")
	}

	for i, call := range recorded {
		if out.ended != "" {
			if err := abandon(call); err != nil {
				return out, err
			}
			continue
		}
		c := step.calls[i]
		if c.Type != toolCallType || call.Name == "" {
			if err := toolError(call, toolErrMalformed, "malformed tool call: type %q, name %q; only a function call naming a tool can run", c.Type, call.Name); err != nil {
				return out, err
			}
			continue
		}
		tool, ok := granted[call.Name]
		if !ok {
			if err := toolError(call, toolErrNotOffered, "tool %q is not available to you", call.Name); err != nil {
				return out, err
			}
			continue
		}
		args, err := validateToolArgs(tool.Function.Parameters, c.Function.Arguments)
		if err != nil {
			if err := toolError(call, tools.ErrCodeBadArguments, "invalid arguments: %v", err); err != nil {
				return out, err
			}
			continue
		}
		if out.used >= step.remaining {
			if err := toolError(call, toolErrCallLimit, "no tool calls remain for you in this turn"); err != nil {
				return out, err
			}
			continue
		}
		out.used++
		if err := recheck(); err != nil {
			if !errors.Is(err, errToolAbandoned) {
				return out, err
			}
			if err := abandon(call); err != nil {
				return out, err
			}
			continue
		}
		res, err := step.effects[call.Name](call, args, recheck)
		switch {
		case errors.Is(err, errToolAbandoned):
			if err := abandon(call); err != nil {
				return out, err
			}
		case err != nil:
			return out, err
		default:
			status := journal.ToolCallExecuted
			if res.ErrorCode != "" {
				status = journal.ToolCallError
			}
			out.results = append(out.results, toolCallOutcome{call: call, status: status, content: res.Content, errorCode: res.ErrorCode})
		}
	}
	return out, nil
}

// stepCalls renders the step's calls as the journal records them: each with
// its provider id, name, raw arguments and the class of the tool it names,
// journal.ToolClassNone for one that can never run. It refuses a step whose
// calls do not carry distinct, non-empty ids.
func stepCalls(step toolStep) ([]journal.StepCall, error) {
	seen := make(map[string]bool, len(step.calls))
	out := make([]journal.StepCall, 0, len(step.calls))
	for _, c := range step.calls {
		if c.ID == "" || seen[c.ID] {
			return nil, fmt.Errorf("%w: attempt %s", errToolStepIDs, step.attemptID)
		}
		seen[c.ID] = true
		class := journal.ToolClassNone
		if _, group, ok := tools.Lookup(c.Function.Name); ok && c.Type == toolCallType {
			class = group
		}
		out = append(out, journal.StepCall{
			ProviderCallID: c.ID,
			Name:           c.Function.Name,
			Class:          class,
			Args:           []byte(c.Function.Arguments),
		})
	}
	return out, nil
}

// validateToolArgs parses raw as a call's arguments object and checks it
// against the tool's parameter schema: the subset the tools' definitions use,
// an object of string and integer properties with required names, minimums
// and no additional properties. It returns the arguments normalized for the
// effect: the same object re-encoded with every integer property written as
// a plain integer, so a schema-valid spelling such as 2.0 or 2e0 decodes
// into the effect's integer fields exactly as 2 does.
func validateToolArgs(schema map[string]interface{}, raw string) (json.RawMessage, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("not JSON: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the arguments object")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("arguments must be a JSON object")
	}
	if typ, _ := schema["type"].(string); typ != "object" {
		return nil, fmt.Errorf("unsupported schema type %q", typ)
	}
	props, _ := schema["properties"].(map[string]interface{})
	for _, name := range requiredNames(schema["required"]) {
		if _, ok := obj[name]; !ok {
			return nil, fmt.Errorf("missing required argument %q", name)
		}
	}
	names := make([]string, 0, len(obj))
	for name := range obj {
		names = append(names, name)
	}
	sort.Strings(names)
	normalized := make(map[string]any, len(obj))
	for _, name := range names {
		p, ok := props[name].(map[string]interface{})
		if !ok {
			if extra, _ := schema["additionalProperties"].(bool); !extra {
				return nil, fmt.Errorf("unknown argument %q", name)
			}
			normalized[name] = obj[name]
			continue
		}
		value, err := validateToolValue(p, obj[name])
		if err != nil {
			return nil, fmt.Errorf("argument %q: %v", name, err)
		}
		normalized[name] = value
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(normalized); err != nil {
		return nil, fmt.Errorf("encode arguments: %v", err)
	}
	return json.RawMessage(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), nil
}

// requiredNames reads a schema's required list, as the definitions build it
// or as a decoded JSON schema carries it.
func requiredNames(v any) []string {
	switch r := v.(type) {
	case []string:
		return r
	case []interface{}:
		out := make([]string, 0, len(r))
		for _, name := range r {
			if s, ok := name.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// validateToolValue checks one argument against its property schema and
// returns its normalized value: the string itself, or an int64 for an
// integer property.
func validateToolValue(p map[string]interface{}, v any) (any, error) {
	switch typ, _ := p["type"].(string); typ {
	case "string":
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("must be a string")
		}
		return s, nil
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return nil, errors.New("must be an integer")
		}
		i, err := n.Int64()
		if err != nil {
			// JSON does not tell 2 from 2.0 or 2e0: a whole number within
			// the exactly representable range is an integer.
			f, ferr := n.Float64()
			if ferr != nil || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
				return nil, errors.New("must be an integer")
			}
			i = int64(f)
		}
		if m, ok := p["minimum"]; ok {
			lo, err := schemaInt(m)
			if err != nil {
				return nil, err
			}
			if i < lo {
				return nil, fmt.Errorf("must be at least %d", lo)
			}
		}
		return i, nil
	default:
		return nil, fmt.Errorf("unsupported property type %q", typ)
	}
}

// schemaInt reads an integer bound from a schema, as the definitions build it
// or as a decoded JSON schema carries it.
func schemaInt(v any) (int64, error) {
	switch m := v.(type) {
	case int:
		return int64(m), nil
	case int64:
		return m, nil
	case float64:
		return int64(m), nil
	case json.Number:
		return m.Int64()
	}
	return 0, fmt.Errorf("unsupported schema bound %v", v)
}
