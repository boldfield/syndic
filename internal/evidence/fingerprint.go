package evidence

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
)

const FingerprintRule = "fingerprint/v1"

// ConfigFingerprint computes a SHA-256 fingerprint of an endpoint profile.
// It returns lowercase hex of the hash over canonical JSON of
// {profile: <full profile>, model: <model>, adapter_version: <version>}.
func ConfigFingerprint(profile config.EndpointProfile, model, adapterVersion string) (string, error) {
	// Build the fingerprint input with the full profile details.
	input := map[string]interface{}{
		"profile": map[string]interface{}{
			"name":                profile.Name,
			"base_url":            profile.BaseURL,
			"auth":                profileAuth(profile.Auth),
			"extra_headers":       profileExtraHeaders(profile.ExtraHeaders),
			"declared_location":   profile.DeclaredLocation,
			"declared_execution":  profile.DeclaredExecution,
			"declared_billing":    profile.DeclaredBilling,
			"network_dependency":  profile.NetworkDependency,
			"dialect":             profile.Dialect,
			"max_tokens_field":    profile.MaxTokensField,
			"models":              profile.Models,
			"terms_note":          profile.TermsNote,
			"tokens_per_byte_max": profile.TokensPerByteMax,
		},
		"model":           model,
		"adapter_version": adapterVersion,
	}

	// Marshal to canonical JSON (sorted keys).
	data, err := json.Marshal(input)
	if err != nil {
		return "", err
	}

	// Compute SHA-256 hash and return as lowercase hex.
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash), nil
}

// profileAuth converts a secret reference to its string representation for fingerprinting.
func profileAuth(auth *config.SecretRef) interface{} {
	if auth == nil {
		return nil
	}
	return map[string]string{
		"source": auth.Source,
		"name":   auth.Name,
	}
}

// profileExtraHeaders converts extra headers to a map of secret references for fingerprinting.
func profileExtraHeaders(headers map[string]config.SecretRef) map[string]map[string]string {
	if len(headers) == 0 {
		return nil
	}

	result := make(map[string]map[string]string)
	// Sort keys to ensure consistent order
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		ref := headers[k]
		result[k] = map[string]string{
			"source": ref.Source,
			"name":   ref.Name,
		}
	}
	return result
}

// CalibrationRecord marshals to "unavailable" when Available is false,
// and to an object when Available is true.
type CalibrationRecord struct {
	Available            bool
	BytesPerToken        float64
	ObservedPromptTokens int
	BaselinePromptTokens int
}

// MarshalJSON implements json.Marshaler.
func (c CalibrationRecord) MarshalJSON() ([]byte, error) {
	if !c.Available {
		return json.Marshal("unavailable")
	}

	obj := map[string]interface{}{
		"bytes_per_token":        c.BytesPerToken,
		"observed_prompt_tokens": c.ObservedPromptTokens,
		"baseline_prompt_tokens": c.BaselinePromptTokens,
	}
	return json.Marshal(obj)
}

// UnmarshalJSON implements json.Unmarshaler.
func (c *CalibrationRecord) UnmarshalJSON(data []byte) error {
	var raw interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	switch v := raw.(type) {
	case string:
		if v == "unavailable" {
			c.Available = false
			return nil
		}
		return fmt.Errorf("invalid calibration string: %q", v)

	case map[string]interface{}:
		c.Available = true
		if val, ok := v["bytes_per_token"]; ok {
			if f, ok := val.(float64); ok {
				c.BytesPerToken = f
			}
		}
		if val, ok := v["observed_prompt_tokens"]; ok {
			if f, ok := val.(float64); ok {
				c.ObservedPromptTokens = int(f)
			}
		}
		if val, ok := v["baseline_prompt_tokens"]; ok {
			if f, ok := val.(float64); ok {
				c.BaselinePromptTokens = int(f)
			}
		}
		return nil

	default:
		return fmt.Errorf("invalid calibration type: %T", v)
	}
}

// ProbeManifest is the JSON shape written to the journal for a probe run.
type ProbeManifest struct {
	Export            Export            `json:"export"`
	ConfigFingerprint string            `json:"config_fingerprint"`
	FingerprintRule   string            `json:"fingerprint_rule"`
	Calibration       CalibrationRecord `json:"calibration"`
}

// NewProbeManifest creates a ProbeManifest from an export, profile, model, adapter version, and calibration.
func NewProbeManifest(export Export, profile config.EndpointProfile, model, adapterVersion string, cal transport.Calibration) (ProbeManifest, error) {
	fp, err := ConfigFingerprint(profile, model, adapterVersion)
	if err != nil {
		return ProbeManifest{}, err
	}

	calRec := CalibrationRecord{
		Available:            cal.Available,
		BytesPerToken:        cal.BytesPerToken,
		ObservedPromptTokens: cal.ObservedPromptTokens,
		BaselinePromptTokens: cal.BaselinePromptTokens,
	}

	return ProbeManifest{
		Export:            export,
		ConfigFingerprint: fp,
		FingerprintRule:   FingerprintRule,
		Calibration:       calRec,
	}, nil
}

// ParseProbeManifest accepts both the new wrapped shape and the older bare export.
// For the bare shape it returns the export, an empty fingerprint, and hasFingerprint == false.
// For the new wrapped shape it returns the full manifest with hasFingerprint == true.
// Anything else is an error.
func ParseProbeManifest(raw json.RawMessage) (m ProbeManifest, hasFingerprint bool, err error) {
	// Check for empty objects/arrays first
	var obj interface{}
	if err := json.Unmarshal(raw, &obj); err == nil {
		switch v := obj.(type) {
		case map[string]interface{}:
			if len(v) == 0 {
				return ProbeManifest{}, false, fmt.Errorf("empty object")
			}
		case []interface{}:
			return ProbeManifest{}, false, fmt.Errorf("array not supported")
		}
	}

	// Try to detect the shape by looking at the object structure
	var objMap map[string]interface{}
	if err := json.Unmarshal(raw, &objMap); err == nil {
		// If it has fingerprint-specific fields, it's the new wrapped shape
		if _, hasConfigFingerprint := objMap["config_fingerprint"]; hasConfigFingerprint {
			var pm ProbeManifest
			if err := json.Unmarshal(raw, &pm); err != nil {
				return ProbeManifest{}, false, err
			}
			return pm, true, nil
		}
		if _, hasFingerprintRule := objMap["fingerprint_rule"]; hasFingerprintRule {
			var pm ProbeManifest
			if err := json.Unmarshal(raw, &pm); err != nil {
				return ProbeManifest{}, false, err
			}
			return pm, true, nil
		}
		if _, hasCalibration := objMap["calibration"]; hasCalibration {
			var pm ProbeManifest
			if err := json.Unmarshal(raw, &pm); err != nil {
				return ProbeManifest{}, false, err
			}
			return pm, true, nil
		}

		// Check if it looks like a bare Export (has redaction-specific fields)
		if _, hasRedacted := objMap["redacted"]; hasRedacted {
			var exp Export
			if err := json.Unmarshal(raw, &exp); err != nil {
				return ProbeManifest{}, false, err
			}
			return ProbeManifest{Export: exp}, false, nil
		}
	}

	return ProbeManifest{}, false, fmt.Errorf("invalid probe manifest")
}
