package config

import (
	"fmt"
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
	return []byte(d.Duration.String()), nil
}

type Role string

const (
	RoleAuthor   Role = "author"
	RoleReviewer Role = "reviewer"
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

type Budgets struct {
	CalibrationSafety       *float64 `toml:"calibration_safety"`
	MessageOverheadTokens   *int     `toml:"message_overhead_tokens"`
	RequestOverheadTokens   *int     `toml:"request_overhead_tokens"`
	ContextMargin           *float64 `toml:"context_margin"`
	AuthorMaxOutputTokens   *int     `toml:"author_max_output_tokens"`
	ReviewerMaxOutputTokens *int     `toml:"reviewer_max_output_tokens"`
	DeliveryMaxTokens       *int     `toml:"delivery_max_tokens"`
	TurnMaxEstimatedTokens  *int     `toml:"turn_max_estimated_tokens"`
}

type EngineSettings struct {
	StopGrace *Duration `toml:"stop_grace"`
}

type Pool struct {
	Concurrency           *int      `toml:"concurrency"`
	UncertainReleaseAfter *Duration `toml:"uncertain_release_after"`
}

type Participant struct {
	ID            string   `toml:"id"`
	Model         string   `toml:"model"`
	Route         string   `toml:"route"` // "api" or "cli"
	Endpoint      string   `toml:"endpoint"`
	Binary        string   `toml:"binary"`
	EligibleRoles []Role   `toml:"eligible_roles"`
	Pool          string   `toml:"pool"`
	ContextTokens *int     `toml:"context_tokens"`
	BytesPerToken *float64 `toml:"bytes_per_token"`
}

type EndpointProfile struct {
	Name              string               `toml:"name"`
	BaseURL           string               `toml:"base_url"`
	Auth              *SecretRef           `toml:"auth"`
	ExtraHeaders      map[string]SecretRef `toml:"extra_headers"`
	DeclaredLocation  string               `toml:"declared_location"`  // "local", "lan", "corporate", "cloud"
	DeclaredExecution string               `toml:"declared_execution"` // "local", "remote", "unknown"
	DeclaredBilling   string               `toml:"declared_billing"`   // "none", "flat", "metered", "unknown"
	NetworkDependency string               `toml:"network_dependency"` // "none", "vpn", "other"
	Dialect           string               `toml:"dialect"`            // "chat_completions_v1"
	MaxTokensField    string               `toml:"max_tokens_field"`   // "max_tokens", "max_completion_tokens", ""
	Models            []string             `toml:"models"`
	TermsNote         string               `toml:"terms_note"`
	TokensPerByteMax  *float64             `toml:"tokens_per_byte_max"`
}

type Preset struct {
	Mode              string   `toml:"mode"` // "solo" or "review"
	Author            string   `toml:"author"`
	RequiredReviewers []string `toml:"required_reviewers"`
}

type RoutingPolicy struct {
	LocalOnly           bool     `toml:"local_only"`
	AllowedDestinations []string `toml:"allowed_destinations"`
}

type Roster struct {
	DefaultPreset string            `toml:"default_preset"`
	Participants  []Participant     `toml:"participants"`
	Endpoints     []EndpointProfile `toml:"endpoints"`
	Presets       map[string]Preset `toml:"presets"`
	Policy        RoutingPolicy     `toml:"policy"`
	Deadlines     Deadlines         `toml:"deadlines"`
	Budgets       Budgets           `toml:"budgets"`
	Engine        EngineSettings    `toml:"engine"`
	Pools         map[string]Pool   `toml:"pools"`
}

// String returns a string representation that never contains resolved secret values.
func (r *Roster) String() string {
	return fmt.Sprintf("Roster{DefaultPreset: %q, Participants: %d, Endpoints: %d, Presets: %d}",
		r.DefaultPreset, len(r.Participants), len(r.Endpoints), len(r.Presets))
}

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
