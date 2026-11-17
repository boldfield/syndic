package config

import "fmt"

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

type Participant struct {
	ID            string `toml:"id"`
	Model         string `toml:"model"`
	Route         string `toml:"route"` // "api" or "cli"
	Endpoint      string `toml:"endpoint"`
	Binary        string `toml:"binary"`
	EligibleRoles []Role `toml:"eligible_roles"`
	Pool          string `toml:"pool"`
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
}

// String returns a string representation that never contains resolved secret values.
func (r *Roster) String() string {
	return fmt.Sprintf("Roster{DefaultPreset: %q, Participants: %d, Endpoints: %d, Presets: %d}",
		r.DefaultPreset, len(r.Participants), len(r.Endpoints), len(r.Presets))
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
