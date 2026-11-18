package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/manifest"
	"github.com/boldfield/syndic/internal/prompt"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

// Clock abstracts time operations for testing.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

// realClock implements Clock using the standard library.
type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now()
}

func (realClock) AfterFunc(d time.Duration, f func()) func() bool {
	t := time.AfterFunc(d, f)
	return t.Stop
}

// Options holds the configuration for creating an Engine.
type Options struct {
	Store         *journal.Store
	Hub           *hub.Hub
	RosterPath    string
	EngineVersion string
	Clock         Clock
	Rand          io.Reader
	NewAdapter    func(p config.Participant, ep config.EndpointProfile) (transport.Adapter, string, error)
}

// Engine manages the review engine lifecycle and state.
type Engine struct {
	store         *journal.Store
	hub           *hub.Hub
	rosterPath    string
	engineVersion string
	clock         Clock
	rand          io.Reader
	newAdapter    func(p config.Participant, ep config.EndpointProfile) (transport.Adapter, string, error)

	mu               sync.Mutex
	roster           *config.Roster
	rosterHash       string
	rosterFileSHA256 string
	manifest         json.RawMessage
	manifestHash     string
	runID            string
	runFirstSeq      int64
	participants     map[string]manifest.Participant
	adapters         map[string]transport.Adapter
	startedAt        time.Time
}

// New creates a new Engine with the given options.
func New(o Options) (*Engine, error) {
	if o.Store == nil {
		return nil, errors.New("Store is required")
	}
	if o.Hub == nil {
		return nil, errors.New("Hub is required")
	}
	if o.RosterPath == "" {
		return nil, errors.New("RosterPath is required")
	}
	if o.EngineVersion == "" {
		return nil, errors.New("EngineVersion is required")
	}
	if o.NewAdapter == nil {
		return nil, errors.New("NewAdapter is required")
	}

	clock := o.Clock
	if clock == nil {
		clock = realClock{}
	}

	randReader := o.Rand
	if randReader == nil {
		randReader = rand.Reader
	}

	return &Engine{
		store:         o.Store,
		hub:           o.Hub,
		rosterPath:    o.RosterPath,
		engineVersion: o.EngineVersion,
		clock:         clock,
		rand:          randReader,
		newAdapter:    o.NewAdapter,
		participants:  make(map[string]manifest.Participant),
		adapters:      make(map[string]transport.Adapter),
		startedAt:     clock.Now(),
	}, nil
}

// RosterError is returned by Start when the roster fails to load or validate.
type RosterError struct {
	Err error
}

func (e *RosterError) Error() string {
	return e.Err.Error()
}

func (e *RosterError) Unwrap() error {
	return e.Err
}

// Start reads the roster, validates it, builds adapters and manifest, and writes the engine run.
func (e *Engine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Read roster file
	rosterData, err := os.ReadFile(e.rosterPath)
	if err != nil {
		return &RosterError{fmt.Errorf("read roster file: %w", err)}
	}

	// Load roster
	roster, err := config.Load(e.rosterPath)
	if err != nil {
		return &RosterError{fmt.Errorf("load roster: %w", err)}
	}

	// Validate roster
	if err := roster.Validate(); err != nil {
		return &RosterError{fmt.Errorf("validate roster: %w", err)}
	}

	// Build manifest
	builtManifest, err := manifest.Build(manifest.Input{
		Store:         e.store,
		Roster:        roster,
		RosterPath:    e.rosterPath,
		RosterFile:    rosterData,
		EngineVersion: e.engineVersion,
		StartedAt:     e.startedAt,
		AdapterVersion: func(p config.Participant) string {
			endpoint := findEndpoint(roster, p.Endpoint)
			if endpoint == nil {
				return ""
			}
			_, version, err := e.newAdapter(p, *endpoint)
			if err != nil {
				return ""
			}
			return version
		},
	})
	if err != nil {
		return &RosterError{fmt.Errorf("build manifest: %w", err)}
	}

	// Build adapters for each participant
	for _, p := range roster.Participants {
		endpoint := findEndpoint(roster, p.Endpoint)
		if endpoint == nil {
			continue
		}
		adapter, _, err := e.newAdapter(p, *endpoint)
		if err != nil {
			return fmt.Errorf("create adapter for %s: %w", p.ID, err)
		}
		if adapter != nil {
			e.adapters[p.ID] = adapter
		}
	}

	// Write engine run through hub transaction
	var runID, manifestHash string
	err = e.hub.Tx(func(tx *journal.Tx, emit hub.Emit) error {
		var txErr error
		runID, manifestHash, txErr = tx.CreateEngineRun(builtManifest.JSON)
		if txErr != nil {
			return txErr
		}

		// Emit engine_started event
		return emit("", wire.EventKindEngineStarted, wire.EvEngineStarted{
			RunID:             runID,
			ManifestHash:      manifestHash,
			RosterHash:        builtManifest.RosterHash,
			RecoveredAttempts: 0,
			UncertainCapacity: []string{},
			OrphanedHolds:     []wire.Hold{},
			Unprobed:          []string{},
		})
	})
	if err != nil {
		return fmt.Errorf("write engine run: %w", err)
	}

	// Store manifest and other info
	e.roster = roster
	e.rosterHash = builtManifest.RosterHash
	e.rosterFileSHA256 = builtManifest.RosterFileSHA256
	e.manifest = builtManifest.JSON
	e.manifestHash = manifestHash
	e.runID = runID
	e.participants = builtManifest.Participants

	// Get the seq of the engine_started event (which is the max seq after writing the run)
	maxSeq, err := e.store.MaxSeq()
	if err != nil {
		return fmt.Errorf("get max seq: %w", err)
	}
	e.runFirstSeq = maxSeq

	return nil
}

// Hello returns the hello reply with engine info.
func (e *Engine) Hello() wire.HelloReply {
	e.mu.Lock()
	defer e.mu.Unlock()

	return wire.HelloReply{
		EngineVersion: e.engineVersion,
		Protocol:      wire.ProtocolVersion,
		RunID:         e.runID,
		RunFirstSeq:   e.runFirstSeq,
		RosterHash:    e.rosterHash,
		ManifestHash:  e.manifestHash,
	}
}

// Status returns the status of the engine.
func (e *Engine) Status() (wire.StatusReply, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.roster == nil {
		return wire.StatusReply{}, errors.New("engine not started")
	}

	// Build participants array
	participants := make([]wire.Participant, 0, len(e.roster.Participants))
	for _, rp := range e.roster.Participants {
		mp := e.participants[rp.ID]

		state := "unprobed"
		detail := mp.Detail
		if mp.Usable {
			state = "idle"
		}

		par := wire.Participant{
			ID:                       rp.ID,
			Model:                    rp.Model,
			ReportedModel:            mp.Expected.ReportedModel,
			Identity:                 mp.Expected.Identity.Kind,
			State:                    state,
			Detail:                   &detail,
			Pool:                     rp.Pool,
			EligibleRoles:            []string{},
			ContextBound:             convertInt(mp.ContextBound),
			OutputBound:              convertInt(mp.OutputBound),
			DeclaredBilling:          findEndpointBilling(e.roster, rp.Endpoint),
			BytesPerToken:            mp.BytesPerToken,
			BytesPerTokenSource:      mp.BytesPerTokenSource,
			ContextSource:            mp.ContextSource,
			EligibleRequiredReviewer: string(mp.EligibleRequiredReviewer),
			Estimate:                 "",
		}

		if mp.ProbeObservedAt != nil {
			t := mp.ProbeObservedAt.Format(time.RFC3339)
			par.ProbeObservedAt = &t
		}

		for _, role := range rp.EligibleRoles {
			par.EligibleRoles = append(par.EligibleRoles, string(role))
		}

		participants = append(participants, par)
	}

	// Build pools array
	pools := make([]wire.Pool, 0)
	poolNames := make([]string, 0, len(e.roster.Pools))
	for name := range e.roster.Pools {
		poolNames = append(poolNames, name)
	}
	sort.Strings(poolNames)

	for _, poolName := range poolNames {
		pool := e.roster.Pools[poolName]
		concurrency := 1
		if pool.Concurrency != nil {
			concurrency = *pool.Concurrency
		}
		pools = append(pools, wire.Pool{
			Name:        poolName,
			Concurrency: concurrency,
			Holds:       []wire.PoolHold{},
		})
	}

	// Build quick profiles array
	quickProfiles := make([]wire.QuickProfile, 0)
	profileNames := make([]string, 0, len(e.roster.Quick))
	for name := range e.roster.Quick {
		profileNames = append(profileNames, name)
	}
	sort.Strings(profileNames)

	for _, profileName := range profileNames {
		qp := e.roster.Quick[profileName]
		defSHA, _ := e.roster.QuickDefinitionSHA256(profileName)
		quickProfiles = append(quickProfiles, wire.QuickProfile{
			Name:             profileName,
			Preset:           qp.Preset,
			Drafts:           qp.Drafts,
			DefinitionSHA256: defSHA,
		})
	}

	// Check if roster file changed
	currentRosterData, err := os.ReadFile(e.rosterPath)
	rosterFileChanged := false
	if err == nil {
		currentHash := computeSHA256(currentRosterData)
		rosterFileChanged = currentHash != e.rosterFileSHA256
	}

	defaultPreset := e.roster.DefaultPreset
	if defaultPreset == "" {
		defaultPreset = "solo"
	}

	deadlinesProvisional := false
	if e.roster.Deadlines.Provisional != nil {
		deadlinesProvisional = *e.roster.Deadlines.Provisional
	}

	return wire.StatusReply{
		RunID:                e.runID,
		RunStartedAt:         e.startedAt.Format(time.RFC3339),
		RosterPath:           e.rosterPath,
		RosterHash:           e.rosterHash,
		RosterFileChanged:    rosterFileChanged,
		DefaultPreset:        defaultPreset,
		DeadlinesProvisional: deadlinesProvisional,
		Participants:         participants,
		Pools:                pools,
		OrphanedHolds:        []wire.Hold{},
		QuickProfiles:        quickProfiles,
		TurnsOpen:            []string{},
		EngineStartedAt:      e.startedAt.Format(time.RFC3339),
	}, nil
}

// Submit processes a submission request and creates a turn.
func (e *Engine) Submit(req wire.SubmitRequest) (wire.SubmitReply, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.roster == nil {
		return wire.SubmitReply{}, errors.New("engine not started")
	}

	// Check for existing submission
	existingTurn, found, err := e.store.FindSubmission(req.SubmissionID)
	if err == nil && found && existingTurn != nil {
		// Submission already exists; return the original reply
		return wire.SubmitReply{
			TurnID:          existingTurn.ID,
			Preset:          existingTurn.Preset,
			EstimatedTokens: 0,
		}, nil
	}

	// Check for submission conflict (same ID with different content)
	if err == nil && found && existingTurn != nil {
		// Check if submission hash matches
		submissionData := map[string]interface{}{
			"input":             req.Input,
			"preset":            req.Preset,
			"quick_profile":     req.QuickProfile,
			"allow_provisional": req.AllowProvisional,
		}
		submissionJSON, _ := json.Marshal(submissionData)
		submissionHash := computeSHA256(submissionJSON)

		if existingTurn.SubmissionHash != submissionHash {
			return wire.SubmitReply{}, &wire.Error{
				Code:   wire.ErrorCodeSubmissionConflict,
				TurnID: &existingTurn.ID,
			}
		}
	}

	// Resolve preset
	presetName := e.roster.DefaultPreset
	if presetName == "" {
		presetName = "solo"
	}
	if req.Preset != nil {
		presetName = *req.Preset
	}

	preset, hasPreset := e.roster.Presets[presetName]
	if !hasPreset {
		return wire.SubmitReply{}, &wire.Error{
			Code: wire.ErrorCodePresetInvalid,
		}
	}

	// Check if quick profile is unknown
	if req.QuickProfile != nil {
		_, hasQuick := e.roster.Quick[*req.QuickProfile]
		if !hasQuick {
			return wire.SubmitReply{}, &wire.Error{
				Code:         wire.ErrorCodeQuickProfileUnknown,
				QuickProfile: req.QuickProfile,
			}
		}
	}

	// Find author participant
	var author config.Participant
	authorFound := false
	for _, p := range e.roster.Participants {
		if p.ID == preset.Author {
			author = p
			authorFound = true
			break
		}
	}
	if !authorFound {
		return wire.SubmitReply{}, &wire.Error{
			Code: wire.ErrorCodePresetInvalid,
		}
	}

	// Check required reviewers exist and are eligible
	for _, reviewer := range preset.RequiredReviewers {
		reviewerFound := false
		for _, p := range e.roster.Participants {
			if p.ID == reviewer {
				reviewerFound = true
				mp := e.participants[reviewer]
				if mp.EligibleRequiredReviewer == transport.TriNo {
					detail := fmt.Sprintf("ineligible_required_reviewer:%s", reviewer)
					return wire.SubmitReply{}, &wire.Error{
						Code:   wire.ErrorCodePresetInvalid,
						Detail: &detail,
					}
				}
				break
			}
		}
		if !reviewerFound {
			return wire.SubmitReply{}, &wire.Error{
				Code: wire.ErrorCodePresetInvalid,
			}
		}
	}

	// Check provisional deadlines
	deadlinesProvisional := false
	if e.roster.Deadlines.Provisional != nil {
		deadlinesProvisional = *e.roster.Deadlines.Provisional
	}
	if deadlinesProvisional && (req.AllowProvisional == nil || !*req.AllowProvisional) {
		return wire.SubmitReply{}, &wire.Error{
			Code: wire.ErrorCodeProvisional,
		}
	}

	// Create turn through transaction
	var turnID string
	const estimated int64 = 0

	submissionData := map[string]interface{}{
		"input":             req.Input,
		"preset":            req.Preset,
		"quick_profile":     req.QuickProfile,
		"allow_provisional": req.AllowProvisional,
	}
	submissionJSON, _ := json.Marshal(submissionData)
	submissionHash := computeSHA256(submissionJSON)

	err = e.hub.Tx(func(tx *journal.Tx, emit hub.Emit) error {
		params := journal.TurnParams{
			RunID:          e.runID,
			Mode:           preset.Mode,
			Preset:         presetName,
			SubmissionID:   req.SubmissionID,
			SubmissionHash: submissionHash,
		}
		var txErr error
		turnID, txErr = tx.CreateEngineTurn(params)
		if txErr != nil {
			return txErr
		}

		// Emit turn_submitted event
		return emit(turnID, wire.EventKindTurnSubmitted, wire.EvTurnSubmitted{
			TurnID:          turnID,
			SubmissionID:    req.SubmissionID,
			Preset:          presetName,
			QuickProfile:    req.QuickProfile,
			EstimatedTokens: estimated,
		})
	})

	if err != nil {
		return wire.SubmitReply{}, err
	}

	// Execute solo path if preset is solo mode
	if preset.Mode == "solo" {
		e.executeSolo(turnID, presetName, req.Input, author)
	}

	return wire.SubmitReply{
		TurnID:          turnID,
		Preset:          presetName,
		QuickProfile:    req.QuickProfile,
		EstimatedTokens: estimated,
	}, nil
}

// executeSolo executes the solo turn path: append message, dispatch author, complete turn.
func (e *Engine) executeSolo(turnID, presetName, input string, author config.Participant) {
	// Build author prompt
	messages := prompt.Author(input)

	// Append request message in a transaction
	err := e.hub.Tx(func(tx *journal.Tx, emit hub.Emit) error {
		msgID, sha256, byteLen, err := tx.AppendMessage(turnID, "author", "user", nil, []byte(input), nil)
		if err != nil {
			return err
		}
		return emit(turnID, wire.EventKindMessageAppended, wire.EvMessageAppended{
			MsgID:   msgID,
			TurnID:  turnID,
			Phase:   "author",
			Sender:  "user",
			SHA256:  sha256,
			ByteLen: int64(byteLen),
		})
	})
	if err != nil {
		return
	}

	// Begin attempt for author
	deadline := time.Now().Add(5 * time.Minute)
	attemptID, err := e.store.BeginAttemptWith(journal.AttemptParams{
		TurnID:                turnID,
		Participant:           author.ID,
		Phase:                 "author",
		Epoch:                 0,
		Deadline:              &deadline,
		MaxOutputTokens:       4096,
		EstimatedPromptTokens: 0,
	})
	if err != nil {
		return
	}

	// Emit attempt_begun event
	e.hub.Tx(func(tx *journal.Tx, emit hub.Emit) error {
		deadline := time.Now().Add(5 * time.Minute)
		return emit(turnID, wire.EventKindAttemptBegun, wire.EvAttemptBegun{
			AttemptID:             attemptID,
			Participant:           author.ID,
			Phase:                 "author",
			Epoch:                 0,
			Deadline:              deadline.Format(time.RFC3339),
			MaxOutputTokens:       4096,
			EstimatedPromptTokens: 0,
		})
	})

	// Call adapter to get result
	adapter := e.adapters[author.ID]
	if adapter == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	result, err := adapter.Complete(ctx, transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   author.ID,
			TurnID:        turnID,
			Phase:         "author",
			Epoch:         0,
			AttemptID:     attemptID,
			CandidateHash: "",
		},
		Model:           author.Model,
		Messages:        messages,
		MaxOutputTokens: 4096,
		Deadline:        time.Now().Add(5 * time.Minute),
	})
	if err != nil {
		return
	}

	// Handle result
	e.hub.Tx(func(tx *journal.Tx, emit hub.Emit) error {
		accepted, err := tx.AcceptResult(attemptID, 0, "", journal.AttemptOutput{
			RawScrubbed:     result.RawOutput,
			ScrubCount:      result.ScrubCount,
			Identity:        result.Identity.Kind,
			Status:          string(result.Status),
			NativeFinish:    result.NativeFinish,
			TransportStatus: result.TransportStatus,
		})
		if err != nil {
			return err
		}

		if !accepted {
			// Result quarantined
			return emit(turnID, wire.EventKindAttemptCompleted, wire.EvAttemptCompleted{
				AttemptID:       attemptID,
				Status:          "error",
				ScrubCount:      0,
				TransportStatus: "quarantined",
			})
		}

		// Emit attempt_completed
		usage := (*wire.ReportedUsage)(nil)
		if result.Usage != nil {
			pt := int64(result.Usage.Prompt)
			ct := int64(result.Usage.Completion)
			usage = &wire.ReportedUsage{
				PromptTokens:     &pt,
				CompletionTokens: &ct,
			}
		}

		if txErr := emit(turnID, wire.EventKindAttemptCompleted, wire.EvAttemptCompleted{
			AttemptID:       attemptID,
			Status:          string(result.Status),
			ScrubCount:      result.ScrubCount,
			TransportStatus: result.TransportStatus,
			Usage:           usage,
		}); txErr != nil {
			return txErr
		}

		// End turn with appropriate state
		terminalReason := "solo_ok"
		terminalState := "solo_complete"
		if result.Status != transport.StatusComplete {
			terminalReason = fmt.Sprintf("author_failed:%s", result.Status)
			terminalState = "incomplete"
		}

		return tx.EndTurn(turnID, terminalState, terminalReason)
	})

	// Emit turn_state event
	e.hub.Tx(func(tx *journal.Tx, emit hub.Emit) error {
		terminalReason := "solo_ok"
		terminalState := "solo_complete"
		if result.Status != transport.StatusComplete {
			terminalReason = fmt.Sprintf("author_failed:%s", result.Status)
			terminalState = "incomplete"
		}

		reason := terminalReason
		return emit(turnID, wire.EventKindTurnState, wire.EvTurnState{
			TurnID: turnID,
			State:  terminalState,
			Reason: &reason,
		})
	})
}

// Cancel is not implemented in this task.
func (e *Engine) Cancel(turnID string) error {
	return errors.New("not implemented")
}

// Override is not implemented in this task.
func (e *Engine) Override(turnID, reason string) error {
	return errors.New("not implemented")
}

// Release is not implemented in this task.
func (e *Engine) Release(req wire.ReleaseRequest) (wire.ReleaseReply, error) {
	return wire.ReleaseReply{}, errors.New("not implemented")
}

// Stop waits for nothing and returns nil in this task.
func (e *Engine) Stop(ctx context.Context) error {
	return nil
}

// Helper functions

func findEndpoint(roster *config.Roster, name string) *config.EndpointProfile {
	for i := range roster.Endpoints {
		if roster.Endpoints[i].Name == name {
			return &roster.Endpoints[i]
		}
	}
	return nil
}

func findEndpointBilling(roster *config.Roster, endpointName string) string {
	ep := findEndpoint(roster, endpointName)
	if ep == nil {
		return "unknown"
	}
	return ep.DeclaredBilling
}

func convertInt(v *int) *int64 {
	if v == nil {
		return nil
	}
	i64 := int64(*v)
	return &i64
}

func computeSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:])
}
