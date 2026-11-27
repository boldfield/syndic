package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalText(text []byte) error {
	var err error
	d.Duration, err = time.ParseDuration(string(text))
	return err
}

func (d *Duration) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

type Role string

const (
	RoleAuthor   Role = "author"
	RoleReviewer Role = "reviewer"
)

var (
	ErrUnknownPreset       = errors.New("unknown preset")
	ErrUnknownQuickProfile = errors.New("unknown quick profile")
)

type SecretRef struct {
	Source string `toml:"source"` // "env" or "file"
	Name   string `toml:"name"`
}

func (s SecretRef) String() string {
	if s.Source == "env" {
		return "env:" + s.Name
	}
	if s.Source == "file" {
		return "file:" + s.Name
	}
	return "unknown:" + s.Name
}

type Deadlines struct {
	Attempt     *Duration `toml:"attempt"`
	Turn        *Duration `toml:"turn"`
	Delivery    *Duration `toml:"delivery"`
	Provisional *bool     `toml:"provisional"`
}

// Defaults of the deliberate-mode output bounds (v8 section 4).
const (
	DefaultProposerMaxOutputTokens   = 4096
	DefaultCritiqueMaxOutputTokens   = 2048
	DefaultIntegratorMaxOutputTokens = 6144
)

type Budgets struct {
	CalibrationSafety         *float64 `toml:"calibration_safety"`
	MessageOverheadTokens     *int     `toml:"message_overhead_tokens"`
	RequestOverheadTokens     *int     `toml:"request_overhead_tokens"`
	ContextMargin             *float64 `toml:"context_margin"`
	AuthorMaxOutputTokens     *int     `toml:"author_max_output_tokens"`
	ReviewerMaxOutputTokens   *int     `toml:"reviewer_max_output_tokens"`
	ProposerMaxOutputTokens   *int     `toml:"proposer_max_output_tokens"`
	CritiqueMaxOutputTokens   *int     `toml:"critique_max_output_tokens"`
	IntegratorMaxOutputTokens *int     `toml:"integrator_max_output_tokens"`
	DeliveryMaxTokens         *int     `toml:"delivery_max_tokens"`
	TurnMaxEstimatedTokens    *int     `toml:"turn_max_estimated_tokens"`
}

type Revision struct {
	MaxRounds        *int `toml:"max_rounds"`
	MaxRoundsCeiling *int `toml:"max_rounds_ceiling"`
}

type EngineSettings struct {
	StopGrace *Duration `toml:"stop_grace"`
}

type Pool struct {
	Concurrency           *int      `toml:"concurrency"`
	UncertainReleaseAfter *Duration `toml:"uncertain_release_after"`
}

type Participant struct {
	ID                      string   `toml:"id"`
	Model                   string   `toml:"model"`
	Route                   string   `toml:"route"` // "api" or "cli"
	Endpoint                string   `toml:"endpoint"`
	Binary                  string   `toml:"binary"`
	EligibleRoles           []Role   `toml:"eligible_roles"`
	Pool                    string   `toml:"pool"`
	ContextTokens           *int     `toml:"context_tokens"`
	BytesPerToken           *float64 `toml:"bytes_per_token"`
	AcceptUnknownTruncation *bool    `toml:"accept_unknown_truncation"`
}

type EndpointProfile struct {
	Name              string               `toml:"name"`
	BaseURL           string               `toml:"base_url"`
	Auth              *SecretRef           `toml:"auth"`
	ExtraHeaders      map[string]SecretRef `toml:"extra_headers"`
	DeclaredLocation  string               `toml:"declared_location"`  // "local", "lan", "corporate", "cloud"
	DeclaredExecution string               `toml:"declared_execution"` // "local", "remote", "unknown"
	DeclaredBilling   string               `toml:"declared_billing"`   // "none", "flat", "metered", "unknown"
	SpendingControl   string               `toml:"spending_control"`   // "", "external"
	NetworkDependency string               `toml:"network_dependency"` // "none", "vpn", "other"
	Dialect           string               `toml:"dialect"`            // "chat_completions_v1"
	MaxTokensField    string               `toml:"max_tokens_field"`   // "max_tokens", "max_completion_tokens", ""
	Models            []string             `toml:"models"`
	TermsNote         string               `toml:"terms_note"`
	TokensPerByteMax  *float64             `toml:"tokens_per_byte_max"`
	RefereeModel      string               `toml:"referee_model"`    // model for web tool referee
	RefereeEndpoint   string               `toml:"referee_endpoint"` // optional local endpoint for referee
}

// ModeDeliberate is the preset mode in which proposers draft and an
// integrator merges (v8 section 1).
const ModeDeliberate = "deliberate"

type Preset struct {
	Mode              string     `toml:"mode"` // "solo", "review" or "deliberate"
	Author            string     `toml:"author"`
	Proposers         []string   `toml:"proposers"`  // deliberate only
	Integrator        string     `toml:"integrator"` // deliberate only
	RequiredReviewers []string   `toml:"required_reviewers"`
	Tools             []string   `toml:"tools"`               // subset of ["read", "web", "write"] for author
	ReviewerTools     []string   `toml:"reviewer_tools"`      // subset of ["read", "web"] for reviewers
	AllowWebWithRead  *bool      `toml:"allow_web_with_read"` // per-preset override for allow_web_with_read
	Deadlines         *Deadlines `toml:"deadlines"`
	Budgets           *Budgets   `toml:"budgets"`
	Revision          *Revision  `toml:"revision"`
}

// EffectiveRequiredReviewers is the explicit required_reviewers, or, when
// absent, every proposer other than the integrator in proposer order.
func (p Preset) EffectiveRequiredReviewers() []string {
	if p.RequiredReviewers != nil {
		return slices.Clone(p.RequiredReviewers)
	}
	out := []string{}
	for _, id := range p.Proposers {
		if id != p.Integrator {
			out = append(out, id)
		}
	}
	return out
}

// DeliberateProposers returns the proposers in roster order.
func (p Preset) DeliberateProposers() []string { return slices.Clone(p.Proposers) }

// DeliberateIntegrator returns the integrator id.
func (p Preset) DeliberateIntegrator() string { return p.Integrator }

// ProposersWithoutVote returns the proposers that are not required reviewers.
func (p Preset) ProposersWithoutVote() []string {
	reviewers := p.EffectiveRequiredReviewers()
	out := []string{}
	for _, id := range p.Proposers {
		if !slices.Contains(reviewers, id) {
			out = append(out, id)
		}
	}
	return out
}

// Content kinds that travel between the participants of a deliberate preset.
const (
	TextProposal    = "proposal"
	TextCritique    = "critique"
	TextDisposition = "disposition"
	TextCandidate   = "candidate"
)

// TextRoute is one ordered pair of participants between which text travels
// in a deliberate turn, and what travels.
type TextRoute struct {
	From, To string
	Content  string
}

// DeliberateTextRoutes returns every ordered pair of participants between
// which text travels (v8 section 3): each proposal goes to every other
// proposer, to the integrator and to every required reviewer; each critique
// goes to the integrator and to every required reviewer; each disposition and
// the candidate go from the integrator to every required reviewer.
func (p Preset) DeliberateTextRoutes() []TextRoute {
	reviewers := p.EffectiveRequiredReviewers()
	var out []TextRoute
	add := func(from, to, content string) {
		if from != to {
			out = append(out, TextRoute{From: from, To: to, Content: content})
		}
	}
	for _, from := range p.Proposers {
		for _, to := range p.Proposers {
			add(from, to, TextProposal)
		}
		for _, to := range reviewers {
			if !slices.Contains(p.Proposers, to) {
				add(from, to, TextProposal)
			}
		}
	}
	for _, from := range p.Proposers {
		add(from, p.Integrator, TextCritique)
		for _, to := range reviewers {
			add(from, to, TextCritique)
		}
	}
	for _, to := range reviewers {
		add(p.Integrator, to, TextDisposition)
		add(p.Integrator, to, TextCandidate)
	}
	return out
}

type QuickProfile struct {
	Preset                    string   `toml:"preset"`
	Drafts                    bool     `toml:"drafts"`
	Budgets                   *Budgets `toml:"budgets"`
	EligibleRequestCategories any      `toml:"eligible_request_categories"`
	ProtectedPaths            any      `toml:"protected_paths"`
	RequiredReviewTriggers    any      `toml:"required_review_triggers"`
	OutputEscalationRules     any      `toml:"output_escalation_rules"`
}

type Effective struct {
	PresetName   string
	Preset       Preset
	QuickProfile string
	Drafts       bool
	Deadlines    Deadlines
	Budgets      Budgets
	Revision     Revision
}

type RoutingPolicy struct {
	LocalOnly           bool     `toml:"local_only"`
	AllowedDestinations []string `toml:"allowed_destinations"`
}

type WebConfig struct {
	AllowedHosts []string `toml:"allowed_hosts"` // allowlist patterns for web fetch
}

type RefereeConfig struct {
	MaxOutputTokens     *int `toml:"max_output_tokens"`      // default 64
	ExecMaxOutputTokens *int `toml:"exec_max_output_tokens"` // default 256
	MaxReads            *int `toml:"max_reads"`              // default 6, per command decision
}

type ToolsConfig struct {
	AllowWebWithRead       *bool          `toml:"allow_web_with_read"`      // default true
	ProtectedPaths         []string       `toml:"protected_paths"`          // patterns to protect
	FileEgressDestinations []string       `toml:"file_egress_destinations"` // endpoints where file content can go
	MaxFetchesPerTurn      *int           `toml:"max_fetches_per_turn"`     // default 20
	MaxCallsPerTurn        *int           `toml:"max_calls_per_turn"`       // default 20
	MaxReviewerCalls       *int           `toml:"max_reviewer_calls"`       // default 10
	MaxResultBytes         *int           `toml:"max_result_bytes"`         // default 65536
	Web                    *WebConfig     `toml:"web"`
	Referee                *RefereeConfig `toml:"referee"`
}

type Roster struct {
	DefaultPreset string                  `toml:"default_preset"`
	Participants  []Participant           `toml:"participants"`
	Endpoints     []EndpointProfile       `toml:"endpoints"`
	Presets       map[string]Preset       `toml:"presets"`
	Policy        RoutingPolicy           `toml:"policy"`
	Deadlines     Deadlines               `toml:"deadlines"`
	Budgets       Budgets                 `toml:"budgets"`
	Revision      Revision                `toml:"revision"`
	Engine        EngineSettings          `toml:"engine"`
	Pools         map[string]Pool         `toml:"pools"`
	Quick         map[string]QuickProfile `toml:"quick"`
	Tools         *ToolsConfig            `toml:"tools"`
}

// String returns a string representation that never contains resolved secret values.
func (r *Roster) String() string {
	return fmt.Sprintf("Roster{DefaultPreset: %q, Participants: %d, Endpoints: %d, Presets: %d}",
		r.DefaultPreset, len(r.Participants), len(r.Endpoints), len(r.Presets))
}

// DefaultMaxReviewerCalls is tools.max_reviewer_calls when the roster sets none.
const DefaultMaxReviewerCalls = 10

// ApplyDefaults fills every absent value in Deadlines, Budgets and Engine with defaults.
func (r *Roster) ApplyDefaults() {
	// Deadlines defaults
	if r.Deadlines.Attempt == nil {
		d := 300 * time.Second
		r.Deadlines.Attempt = &Duration{d}
	}
	if r.Deadlines.Turn == nil {
		d := 900 * time.Second
		r.Deadlines.Turn = &Duration{d}
	}
	if r.Deadlines.Delivery == nil {
		d := 60 * time.Second
		r.Deadlines.Delivery = &Duration{d}
	}
	if r.Deadlines.Provisional == nil {
		prov := true
		r.Deadlines.Provisional = &prov
	}

	// Budgets defaults
	if r.Budgets.CalibrationSafety == nil {
		cs := 0.75
		r.Budgets.CalibrationSafety = &cs
	}
	if r.Budgets.MessageOverheadTokens == nil {
		mot := 8
		r.Budgets.MessageOverheadTokens = &mot
	}
	if r.Budgets.RequestOverheadTokens == nil {
		rot := 16
		r.Budgets.RequestOverheadTokens = &rot
	}
	if r.Budgets.ContextMargin == nil {
		cm := 0.10
		r.Budgets.ContextMargin = &cm
	}
	if r.Budgets.AuthorMaxOutputTokens == nil {
		amot := 4096
		r.Budgets.AuthorMaxOutputTokens = &amot
	}
	if r.Budgets.ReviewerMaxOutputTokens == nil {
		rmot := 2048
		r.Budgets.ReviewerMaxOutputTokens = &rmot
	}
	if r.Budgets.ProposerMaxOutputTokens == nil {
		v := DefaultProposerMaxOutputTokens
		r.Budgets.ProposerMaxOutputTokens = &v
	}
	if r.Budgets.CritiqueMaxOutputTokens == nil {
		v := DefaultCritiqueMaxOutputTokens
		r.Budgets.CritiqueMaxOutputTokens = &v
	}
	if r.Budgets.IntegratorMaxOutputTokens == nil {
		v := DefaultIntegratorMaxOutputTokens
		r.Budgets.IntegratorMaxOutputTokens = &v
	}
	if r.Budgets.DeliveryMaxTokens == nil {
		dmt := 64
		r.Budgets.DeliveryMaxTokens = &dmt
	}
	if r.Budgets.TurnMaxEstimatedTokens == nil {
		tmet := 250000
		r.Budgets.TurnMaxEstimatedTokens = &tmet
	}

	// Engine defaults
	if r.Engine.StopGrace == nil {
		sg := 5 * time.Second
		r.Engine.StopGrace = &Duration{sg}
	}

	// Revision defaults
	if r.Revision.MaxRounds == nil {
		mr := 2
		r.Revision.MaxRounds = &mr
	}
	if r.Revision.MaxRoundsCeiling == nil {
		mrc := 5
		r.Revision.MaxRoundsCeiling = &mrc
	}

	// Tools defaults
	if r.Tools == nil {
		r.Tools = &ToolsConfig{}
	}
	if r.Tools.AllowWebWithRead == nil {
		awr := true
		r.Tools.AllowWebWithRead = &awr
	}
	if len(r.Tools.ProtectedPaths) == 0 {
		r.Tools.ProtectedPaths = []string{
			".git/",
			".syndic/",
			".env*",
			"**/*.key",
			"**/*.pem",
			"**/id_rsa",
			"**/id_dsa",
			"**/id_ecdsa",
			"**/id_ed25519",
			"**/id_ecdsa_sk",
			"**/id_ed25519_sk",
			"syndic.toml",
		}
	}
	if r.Tools.MaxFetchesPerTurn == nil {
		mfpt := 20
		r.Tools.MaxFetchesPerTurn = &mfpt
	}
	if r.Tools.MaxCallsPerTurn == nil {
		mcpt := 20
		r.Tools.MaxCallsPerTurn = &mcpt
	}
	if r.Tools.MaxReviewerCalls == nil {
		mrc := DefaultMaxReviewerCalls
		r.Tools.MaxReviewerCalls = &mrc
	}
	if r.Tools.MaxResultBytes == nil {
		mrb := 65536
		r.Tools.MaxResultBytes = &mrb
	}
	if r.Tools.Referee == nil {
		r.Tools.Referee = &RefereeConfig{}
	}
	if r.Tools.Referee.MaxOutputTokens == nil {
		mot := 64
		r.Tools.Referee.MaxOutputTokens = &mot
	}
	if r.Tools.Referee.ExecMaxOutputTokens == nil {
		emot := 256
		r.Tools.Referee.ExecMaxOutputTokens = &emot
	}
	if r.Tools.Referee.MaxReads == nil {
		mr := 6
		r.Tools.Referee.MaxReads = &mr
	}
}

// Resolve resolves deadlines and budgets for a given preset and quick profile.
// presetName takes precedence; if empty, uses the quick profile's preset; if that's also empty, uses the default preset.
// quickProfile can be empty.
func (r *Roster) Resolve(presetName, quickProfile string) (Effective, error) {
	// Determine which preset to use
	effectivePresetName := presetName
	var quickProf QuickProfile
	var hasQuickProf bool

	if effectivePresetName == "" {
		// Try to get preset from quick profile
		if quickProfile != "" {
			qp, ok := r.Quick[quickProfile]
			if !ok {
				return Effective{}, fmt.Errorf("quick profile %q: %w", quickProfile, ErrUnknownQuickProfile)
			}
			quickProf = qp
			hasQuickProf = true
			effectivePresetName = qp.Preset
		}
		if effectivePresetName == "" {
			effectivePresetName = r.DefaultPreset
		}
	} else if quickProfile != "" {
		// Explicit preset takes precedence, but we still need the quick profile for overrides
		qp, ok := r.Quick[quickProfile]
		if !ok {
			return Effective{}, fmt.Errorf("quick profile %q: %w", quickProfile, ErrUnknownQuickProfile)
		}
		quickProf = qp
		hasQuickProf = true
	}

	// Look up the preset
	preset, ok := r.Presets[effectivePresetName]
	if !ok {
		return Effective{}, fmt.Errorf("preset %q: %w", effectivePresetName, ErrUnknownPreset)
	}

	// Build the Effective result by applying overrides in order
	result := Effective{
		PresetName: effectivePresetName,
		Preset:     preset,
		Drafts:     false,
	}

	if hasQuickProf {
		result.QuickProfile = quickProfile
		result.Drafts = quickProf.Drafts
	}

	// Copy roster-level deadlines, budgets, and revision
	result.Deadlines = r.Deadlines
	result.Budgets = r.Budgets
	result.Revision = r.Revision

	// Apply preset overrides to deadlines
	if preset.Deadlines != nil {
		if preset.Deadlines.Attempt != nil {
			result.Deadlines.Attempt = preset.Deadlines.Attempt
		}
		if preset.Deadlines.Turn != nil {
			result.Deadlines.Turn = preset.Deadlines.Turn
		}
		if preset.Deadlines.Delivery != nil {
			result.Deadlines.Delivery = preset.Deadlines.Delivery
		}
		if preset.Deadlines.Provisional != nil {
			result.Deadlines.Provisional = preset.Deadlines.Provisional
		}
	}

	// Apply preset overrides to budgets
	if preset.Budgets != nil {
		if preset.Budgets.CalibrationSafety != nil {
			result.Budgets.CalibrationSafety = preset.Budgets.CalibrationSafety
		}
		if preset.Budgets.MessageOverheadTokens != nil {
			result.Budgets.MessageOverheadTokens = preset.Budgets.MessageOverheadTokens
		}
		if preset.Budgets.RequestOverheadTokens != nil {
			result.Budgets.RequestOverheadTokens = preset.Budgets.RequestOverheadTokens
		}
		if preset.Budgets.ContextMargin != nil {
			result.Budgets.ContextMargin = preset.Budgets.ContextMargin
		}
		if preset.Budgets.AuthorMaxOutputTokens != nil {
			result.Budgets.AuthorMaxOutputTokens = preset.Budgets.AuthorMaxOutputTokens
		}
		if preset.Budgets.ReviewerMaxOutputTokens != nil {
			result.Budgets.ReviewerMaxOutputTokens = preset.Budgets.ReviewerMaxOutputTokens
		}
		if preset.Budgets.ProposerMaxOutputTokens != nil {
			result.Budgets.ProposerMaxOutputTokens = preset.Budgets.ProposerMaxOutputTokens
		}
		if preset.Budgets.CritiqueMaxOutputTokens != nil {
			result.Budgets.CritiqueMaxOutputTokens = preset.Budgets.CritiqueMaxOutputTokens
		}
		if preset.Budgets.IntegratorMaxOutputTokens != nil {
			result.Budgets.IntegratorMaxOutputTokens = preset.Budgets.IntegratorMaxOutputTokens
		}
		if preset.Budgets.DeliveryMaxTokens != nil {
			result.Budgets.DeliveryMaxTokens = preset.Budgets.DeliveryMaxTokens
		}
		if preset.Budgets.TurnMaxEstimatedTokens != nil {
			result.Budgets.TurnMaxEstimatedTokens = preset.Budgets.TurnMaxEstimatedTokens
		}
	}

	// Apply preset overrides to revision
	if preset.Revision != nil {
		if preset.Revision.MaxRounds != nil {
			result.Revision.MaxRounds = preset.Revision.MaxRounds
		}
		if preset.Revision.MaxRoundsCeiling != nil {
			result.Revision.MaxRoundsCeiling = preset.Revision.MaxRoundsCeiling
		}
	}

	// Apply quick profile budget overrides (quick profile has no deadline overrides)
	if hasQuickProf && quickProf.Budgets != nil {
		if quickProf.Budgets.CalibrationSafety != nil {
			result.Budgets.CalibrationSafety = quickProf.Budgets.CalibrationSafety
		}
		if quickProf.Budgets.MessageOverheadTokens != nil {
			result.Budgets.MessageOverheadTokens = quickProf.Budgets.MessageOverheadTokens
		}
		if quickProf.Budgets.RequestOverheadTokens != nil {
			result.Budgets.RequestOverheadTokens = quickProf.Budgets.RequestOverheadTokens
		}
		if quickProf.Budgets.ContextMargin != nil {
			result.Budgets.ContextMargin = quickProf.Budgets.ContextMargin
		}
		if quickProf.Budgets.AuthorMaxOutputTokens != nil {
			result.Budgets.AuthorMaxOutputTokens = quickProf.Budgets.AuthorMaxOutputTokens
		}
		if quickProf.Budgets.ReviewerMaxOutputTokens != nil {
			result.Budgets.ReviewerMaxOutputTokens = quickProf.Budgets.ReviewerMaxOutputTokens
		}
		if quickProf.Budgets.ProposerMaxOutputTokens != nil {
			result.Budgets.ProposerMaxOutputTokens = quickProf.Budgets.ProposerMaxOutputTokens
		}
		if quickProf.Budgets.CritiqueMaxOutputTokens != nil {
			result.Budgets.CritiqueMaxOutputTokens = quickProf.Budgets.CritiqueMaxOutputTokens
		}
		if quickProf.Budgets.IntegratorMaxOutputTokens != nil {
			result.Budgets.IntegratorMaxOutputTokens = quickProf.Budgets.IntegratorMaxOutputTokens
		}
		if quickProf.Budgets.DeliveryMaxTokens != nil {
			result.Budgets.DeliveryMaxTokens = quickProf.Budgets.DeliveryMaxTokens
		}
		if quickProf.Budgets.TurnMaxEstimatedTokens != nil {
			result.Budgets.TurnMaxEstimatedTokens = quickProf.Budgets.TurnMaxEstimatedTokens
		}
	}

	return result, nil
}

// QuickDefinitionSHA256 returns the SHA-256 hash of the quick profile's fully resolved definition.
// It hashes {"preset": ..., "drafts": ..., "budgets": {...}} with budget keys using TOML names.
func (r *Roster) QuickDefinitionSHA256(name string) (string, error) {
	resolved, err := r.Resolve("", name)
	if err != nil {
		return "", err
	}

	// Build budget object using TOML key names
	budgetsMap := make(map[string]interface{})
	if resolved.Budgets.CalibrationSafety != nil {
		budgetsMap["calibration_safety"] = *resolved.Budgets.CalibrationSafety
	}
	if resolved.Budgets.MessageOverheadTokens != nil {
		budgetsMap["message_overhead_tokens"] = *resolved.Budgets.MessageOverheadTokens
	}
	if resolved.Budgets.RequestOverheadTokens != nil {
		budgetsMap["request_overhead_tokens"] = *resolved.Budgets.RequestOverheadTokens
	}
	if resolved.Budgets.ContextMargin != nil {
		budgetsMap["context_margin"] = *resolved.Budgets.ContextMargin
	}
	if resolved.Budgets.AuthorMaxOutputTokens != nil {
		budgetsMap["author_max_output_tokens"] = *resolved.Budgets.AuthorMaxOutputTokens
	}
	if resolved.Budgets.ReviewerMaxOutputTokens != nil {
		budgetsMap["reviewer_max_output_tokens"] = *resolved.Budgets.ReviewerMaxOutputTokens
	}
	// Omitted at its default so a definition that sets none of the deliberate
	// bounds keeps the hash it had before they existed.
	if resolved.Budgets.ProposerMaxOutputTokens != nil && *resolved.Budgets.ProposerMaxOutputTokens != DefaultProposerMaxOutputTokens {
		budgetsMap["proposer_max_output_tokens"] = *resolved.Budgets.ProposerMaxOutputTokens
	}
	// Omitted at its default so a definition that sets none of the deliberate
	// bounds keeps the hash it had before they existed.
	if resolved.Budgets.CritiqueMaxOutputTokens != nil && *resolved.Budgets.CritiqueMaxOutputTokens != DefaultCritiqueMaxOutputTokens {
		budgetsMap["critique_max_output_tokens"] = *resolved.Budgets.CritiqueMaxOutputTokens
	}
	// Omitted at its default so a definition that sets none of the deliberate
	// bounds keeps the hash it had before they existed.
	if resolved.Budgets.IntegratorMaxOutputTokens != nil && *resolved.Budgets.IntegratorMaxOutputTokens != DefaultIntegratorMaxOutputTokens {
		budgetsMap["integrator_max_output_tokens"] = *resolved.Budgets.IntegratorMaxOutputTokens
	}
	if resolved.Budgets.DeliveryMaxTokens != nil {
		budgetsMap["delivery_max_tokens"] = *resolved.Budgets.DeliveryMaxTokens
	}
	if resolved.Budgets.TurnMaxEstimatedTokens != nil {
		budgetsMap["turn_max_estimated_tokens"] = *resolved.Budgets.TurnMaxEstimatedTokens
	}

	// Build the JSON object to hash with TOML key names
	hashObj := map[string]interface{}{
		"preset":  resolved.PresetName,
		"drafts":  resolved.Drafts,
		"budgets": budgetsMap,
	}

	// json.Marshal sorts map keys, producing canonical form
	jsonBytes, err := json.Marshal(hashObj)
	if err != nil {
		return "", fmt.Errorf("failed to marshal quick profile definition: %w", err)
	}

	hash := sha256.Sum256(jsonBytes)
	return hex.EncodeToString(hash[:]), nil
}

// PoolFor returns the named pool's concurrency and uncertain_release_after values,
// or 1 and 0 for a pool with no table or with absent members.
func (r *Roster) PoolFor(name string) (concurrency int, uncertainReleaseAfter time.Duration) {
	if name == "" {
		name = "default"
	}

	pool, exists := r.Pools[name]
	if !exists {
		return 1, 0
	}

	concurrency = 1
	if pool.Concurrency != nil {
		concurrency = *pool.Concurrency
	}

	uncertainReleaseAfter = 0
	if pool.UncertainReleaseAfter != nil {
		uncertainReleaseAfter = pool.UncertainReleaseAfter.Duration
	}

	return concurrency, uncertainReleaseAfter
}

type SecretResolver interface {
	Resolve(ref SecretRef) (string, error)
}

type EnvFileResolver struct {
	fileDir string
}

func NewEnvFileResolver(fileDir string) *EnvFileResolver {
	return &EnvFileResolver{fileDir: fileDir}
}

func (r *EnvFileResolver) Resolve(ref SecretRef) (string, error) {
	switch ref.Source {
	case "env":
		return resolveEnv(ref.Name)
	case "file":
		return resolveFile(r.fileDir, ref.Name)
	default:
		return "", fmt.Errorf("unknown secret source: %s", ref.Source)
	}
}
