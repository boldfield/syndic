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

// Tool error codes for a call of an accepted tool step that fails a check
// before its effect (v5 delta section 6). Arguments that do not validate use
// tools.ErrCodeBadArguments, as the tools themselves do.
const (
	toolErrNotOffered = "not_offered"    // the caller's role is not given the tool
	toolErrCallLimit  = "call_limit"     // the caller has no calls remaining
	toolErrMalformed  = "malformed_call" // not a function call, or names no tool
)

// toolCallType is the only call type a tool step's call may have an effect
// under.
const toolCallType = "function"

// errToolAbandoned is returned by a step's re-check, and so by an effect that
// calls it after a wait, when the step may no longer have effects. An effect
// that returns it has journaled nothing; the executor journals the call
// abandoned.
var errToolAbandoned = errors.New("tool call abandoned")

// errToolStepIDs reports a tool step whose calls do not carry distinct,
// non-empty ids: no result could be matched to its call, so nothing of the
// step is journaled or executed.
var errToolStepIDs = errors.New("tool step calls lack distinct ids")

// toolEffect performs one accepted, checked call's effect and journals its
// executed or error result against call, as the tools package does. recheck
// re-checks the step and must be called after every wait inside the effect,
// including a referee decision; when it fails the effect returns it at once
// with no effect and nothing journaled.
type toolEffect func(call journal.ToolCall, args json.RawMessage, recheck func() error) (tools.Result, error)

// toolStep is one accepted tool step to execute.
type toolStep struct {
	attemptID string
	calls     []transport.ToolCall
	// granted is the tools the caller's role is given in the preset, as
	// tools.ForGroups returns them; effects holds an effect for each.
	granted []transport.Tool
	effects map[string]toolEffect
	// remaining is the caller's calls left under its bound.
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
// the calls that counted against the bound, and, when the re-check failed,
// the reason the attempt ends (journal.StepEndedTurn or StepEndedEpoch).
type toolStepOutcome struct {
	results []toolCallOutcome
	used    int
	ended   string
}

// runToolStep executes an accepted tool step one call at a time, in the order
// returned. Every call is journaled as accepted intent first, in one
// transaction; a step that is not accepted (late, quarantined, failed by the
// gate) is refused with journal.ErrNotToolStep and nothing runs. Each call is
// then checked in order: it must be a function call naming a tool, the tool
// must be granted to the role, its arguments
// must validate against the tool's schema, and the caller must have a call
// remaining. A call failing a check is answered with a tool error and the
// rest are still checked. Immediately before each effect, and after any wait
// inside it, the step is re-checked against the journal; a call failing the
// re-check is journaled abandoned and has no effect, and so has every call
// after it.
func runToolStep(store *journal.Store, step toolStep) (toolStepOutcome, error) {
	granted := make(map[string]transport.Tool, len(step.granted))
	for _, tool := range step.granted {
		name := tool.Function.Name
		if step.effects[name] == nil {
			return toolStepOutcome{}, fmt.Errorf("granted tool %q has no effect", name)
		}
		granted[name] = tool
	}
	seen := make(map[string]bool, len(step.calls))
	stepCalls := make([]journal.StepCall, 0, len(step.calls))
	for _, c := range step.calls {
		if c.ID == "" || seen[c.ID] {
			return toolStepOutcome{}, fmt.Errorf("%w: attempt %s", errToolStepIDs, step.attemptID)
		}
		seen[c.ID] = true
		class := journal.ToolClassNone
		if _, group, ok := tools.Lookup(c.Function.Name); ok && c.Type == toolCallType {
			class = group
		}
		stepCalls = append(stepCalls, journal.StepCall{
			ProviderCallID: c.ID,
			Name:           c.Function.Name,
			Class:          class,
			Args:           []byte(c.Function.Arguments),
		})
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
	fail := func(call journal.ToolCall, code, format string, args ...any) error {
		return answer(call, journal.ToolCallError, []byte(fmt.Sprintf(format, args...)), code)
	}

	for i, call := range recorded {
		if out.ended != "" {
			if err := answer(call, journal.ToolCallAbandoned, nil, ""); err != nil {
				return out, err
			}
			continue
		}
		raw := step.calls[i].Function.Arguments
		if typ := step.calls[i].Type; typ != toolCallType || call.Name == "" {
			if err := fail(call, toolErrMalformed, "malformed tool call: type %q, name %q; only a function call naming a tool can run", typ, call.Name); err != nil {
				return out, err
			}
			continue
		}
		tool, ok := granted[call.Name]
		if !ok {
			if err := fail(call, toolErrNotOffered, "tool %q is not available to you", call.Name); err != nil {
				return out, err
			}
			continue
		}
		args, err := validateToolArgs(tool.Function.Parameters, raw)
		if err != nil {
			if err := fail(call, tools.ErrCodeBadArguments, "invalid arguments: %v", err); err != nil {
				return out, err
			}
			continue
		}
		if out.used >= step.remaining {
			if err := fail(call, toolErrCallLimit, "no tool calls remain for you in this turn"); err != nil {
				return out, err
			}
			continue
		}
		out.used++
		if err := recheck(); err != nil {
			if !errors.Is(err, errToolAbandoned) {
				return out, err
			}
			if err := answer(call, journal.ToolCallAbandoned, nil, ""); err != nil {
				return out, err
			}
			continue
		}
		res, err := step.effects[call.Name](call, args, recheck)
		switch {
		case errors.Is(err, errToolAbandoned):
			if err := answer(call, journal.ToolCallAbandoned, nil, ""); err != nil {
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

// validateToolArgs parses raw as the arguments object of a call and checks it
// against the tool's parameter schema: the subset tools' definitions use, an
// object of typed properties with required names, minimums and no additional
// properties. It returns the arguments as the effect receives them.
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
	var required []string
	switch r := schema["required"].(type) {
	case []string:
		required = r
	case []interface{}:
		for _, name := range r {
			s, _ := name.(string)
			required = append(required, s)
		}
	}
	for _, name := range required {
		if _, ok := obj[name]; !ok {
			return nil, fmt.Errorf("missing required argument %q", name)
		}
	}
	names := make([]string, 0, len(obj))
	for name := range obj {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p, ok := props[name].(map[string]interface{})
		if !ok {
			if extra, _ := schema["additionalProperties"].(bool); !extra {
				return nil, fmt.Errorf("unknown argument %q", name)
			}
			continue
		}
		if err := validateToolValue(p, obj[name]); err != nil {
			return nil, fmt.Errorf("argument %q: %v", name, err)
		}
	}
	return json.RawMessage(bytes.TrimSpace([]byte(raw))), nil
}

// validateToolValue checks one argument against its property schema.
func validateToolValue(p map[string]interface{}, v any) error {
	switch typ, _ := p["type"].(string); typ {
	case "string":
		if _, ok := v.(string); !ok {
			return errors.New("must be a string")
		}
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return errors.New("must be an integer")
		}
		i, err := n.Int64()
		if err != nil {
			// JSON does not tell 2 from 2.0; a whole number is an integer.
			f, ferr := n.Float64()
			if ferr != nil || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
				return errors.New("must be an integer")
			}
			i = int64(f)
		}
		if m, ok := p["minimum"]; ok {
			var lo int64
			switch m := m.(type) {
			case int:
				lo = int64(m)
			case int64:
				lo = m
			case float64:
				lo = int64(m)
			default:
				return fmt.Errorf("unsupported minimum %v", m)
			}
			if i < lo {
				return fmt.Errorf("must be at least %d", lo)
			}
		}
	default:
		return fmt.Errorf("unsupported property type %q", typ)
	}
	return nil
}
