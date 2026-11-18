package evidence

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
)

var sanitizeRE = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// Export is the redacted evidence record suitable for public repository commit.
type Export struct {
	Redacted                 bool                                  `json:"redacted"`
	RedactionRule            string                                `json:"redaction_rule"`
	EndpointName             string                                `json:"endpoint_name"`
	DeclaredLocation         string                                `json:"declared_location"`
	DeclaredExecution        string                                `json:"declared_execution"`
	DeclaredBilling          string                                `json:"declared_billing"`
	NetworkDependency        string                                `json:"network_dependency"`
	Host                     string                                `json:"host,omitempty"`
	Port                     string                                `json:"port,omitempty"`
	Model                    string                                `json:"model"`
	AuthMode                 string                                `json:"auth_mode"`
	AuthSecret               string                                `json:"auth_secret,omitempty"`
	Identity                 transport.ModelIdentity               `json:"identity"`
	ReportedModel            string                                `json:"reported_model"`
	ContextBound             *int                                  `json:"context_bound,omitempty"`
	OutputBound              *int                                  `json:"output_bound,omitempty"`
	Streaming                transport.Tri                         `json:"streaming"`
	CancelObservable         transport.Tri                         `json:"cancel_observable"`
	UsageAvailable           transport.Tri                         `json:"usage_available"`
	ModelsEndpoint           transport.Tri                         `json:"models_endpoint"`
	ModelListed              transport.Tri                         `json:"model_listed"`
	SystemRoleHonored        transport.Tri                         `json:"system_role_honored"`
	EligibleRequiredReviewer transport.Tri                         `json:"eligible_required_reviewer"`
	MaxTokensField           string                                `json:"max_tokens_field"`
	FinishReasons            map[string]transport.CompletionStatus `json:"finish_reasons"`
	OverLength               string                                `json:"over_length"`
	Latency                  *int                                  `json:"latency_millis,omitempty"`
	ToolRestrictions         []string                              `json:"tool_restrictions,omitempty"`
	AdapterVersion           string                                `json:"adapter_version"`
	ObservedAt               time.Time                             `json:"observed_at"`
	ExtraHeaders             map[string]string                     `json:"extra_headers,omitempty"`
}

// Redact builds an Export from a CapabilityRecord and EndpointProfile.
func Redact(record transport.CapabilityRecord, profile config.EndpointProfile) (Export, error) {
	if record.Endpoint != profile.Name {
		return Export{}, fmt.Errorf("record endpoint %q does not match profile name %q", record.Endpoint, profile.Name)
	}

	export := Export{
		Redacted:                 true,
		RedactionRule:            "evidence/v1",
		EndpointName:             profile.Name,
		DeclaredLocation:         profile.DeclaredLocation,
		DeclaredExecution:        profile.DeclaredExecution,
		DeclaredBilling:          profile.DeclaredBilling,
		NetworkDependency:        profile.NetworkDependency,
		Model:                    record.Model,
		AuthMode:                 record.AuthMode,
		Identity:                 record.Identity,
		ReportedModel:            record.ReportedModel,
		ContextBound:             record.ContextBound,
		OutputBound:              record.OutputBound,
		Streaming:                record.Streaming,
		CancelObservable:         record.CancelObservable,
		UsageAvailable:           record.UsageAvailable,
		ModelsEndpoint:           record.ModelsEndpoint,
		ModelListed:              record.ModelListed,
		SystemRoleHonored:        record.SystemRoleHonored,
		EligibleRequiredReviewer: record.EligibleRequiredReviewer,
		MaxTokensField:           record.MaxTokensField,
		FinishReasons:            record.FinishReasons,
		OverLength:               record.OverLength,
		Latency:                  record.LatencyMillis,
		ToolRestrictions:         record.ToolRestrictions,
		AdapterVersion:           record.AdapterVersion,
		ObservedAt:               record.ObservedAt,
	}

	// Extract host from base URL only for local or lan locations
	if profile.DeclaredLocation == "local" || profile.DeclaredLocation == "lan" {
		host, port, err := extractHostPort(profile.BaseURL)
		if err != nil {
			return Export{}, err
		}
		export.Host = host
		export.Port = port
	}

	// Add auth secret reference if present
	if profile.Auth != nil {
		export.AuthSecret = profile.Auth.String()
	}

	// Add extra headers secret references
	if len(profile.ExtraHeaders) > 0 {
		export.ExtraHeaders = make(map[string]string)
		for name, ref := range profile.ExtraHeaders {
			export.ExtraHeaders[name] = ref.String()
		}
	}

	return export, nil
}

// extractHostPort extracts the host and port from a URL.
func extractHostPort(baseURL string) (host, port string, err error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", "", err
	}

	host = u.Hostname()
	port = u.Port()
	return host, port, nil
}

// WriteRaw writes the complete record as JSON to a file.
func WriteRaw(record transport.CapabilityRecord, dir string) (string, error) {
	// Create directory if it doesn't exist
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}

	filename := generateFilename(record.Endpoint, record.Model, ".raw.json")
	filePath := filepath.Join(dir, filename)

	// Marshal the record
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", err
	}

	// Open file with O_EXCL to prevent overwriting
	file, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	defer file.Close()

	if _, err := file.Write(data); err != nil {
		return "", err
	}

	return filePath, nil
}

// WriteExport writes the export as indented JSON to a file.
func WriteExport(export Export, dir string) (string, error) {
	// Create directory if it doesn't exist
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}

	filename := generateFilename(export.EndpointName, export.Model, ".json")
	filePath := filepath.Join(dir, filename)

	// Marshal the export
	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		return "", err
	}

	// Open file with O_EXCL to prevent overwriting
	file, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	defer file.Close()

	if _, err := file.Write(data); err != nil {
		return "", err
	}

	return filePath, nil
}

// generateFilename generates a filename from endpoint, model, and suffix.
// Replaces non-alphanumeric characters (except dots and dashes) with dashes.
func generateFilename(endpoint, model, suffix string) string {
	now := time.Now().UTC()
	timestamp := now.Format("20060102T150405Z")

	endpoint = sanitizeRE.ReplaceAllString(endpoint, "-")
	model = sanitizeRE.ReplaceAllString(model, "-")

	return fmt.Sprintf("%s-%s-%s%s", endpoint, model, timestamp, suffix)
}
