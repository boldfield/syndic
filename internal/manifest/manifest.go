package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/boldfield/syndic/internal/referee"
	"os"
	"sort"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/evidence"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
)

// Input is the input to Build.
type Input struct {
	Store          *journal.Store
	Roster         *config.Roster
	RosterPath     string
	RosterFile     []byte
	EngineVersion  string
	StartedAt      time.Time
	AdapterVersion func(p config.Participant) string
}

// Participant describes the manifest state of a roster participant.
type Participant struct {
	ID                       string
	Usable                   bool
	Detail                   string
	Expected                 transport.Expected
	EligibleRequiredReviewer transport.Tri
	ToolCalls                transport.Tri
	ProbeObservedAt          *time.Time
	ContextBound             *int
	ContextSource            string
	OutputBound              *int
	BytesPerToken            *float64
	BytesPerTokenSource      string
	// CalibratedBytesPerToken is the probe's calibration before the safety
	// factor; nil unless BytesPerTokenSource is "probe".
	CalibratedBytesPerToken *float64
	FailingStep             *string
	ServerStatus            *string
}

// Built is the output of Build.
type Built struct {
	JSON             json.RawMessage
	RosterHash       string
	RosterFileSHA256 string
	Participants     map[string]Participant
}

// Build constructs an engine run manifest from the roster and probe records.
func Build(in Input) (Built, error) {
	participants := make(map[string]Participant)
	probeManifests := make(map[string]*evidence.ProbeManifest)

	for _, p := range in.Roster.Participants {
		participant, probeManifest, err := buildParticipant(in, p)
		if err != nil {
			return Built{}, err
		}
		participants[p.ID] = participant
		if probeManifest != nil {
			probeManifests[p.ID] = probeManifest
		}
	}

	manifestJSON, rosterHash, rosterFileSHA256, err := buildManifestJSON(in, participants, probeManifests)
	if err != nil {
		return Built{}, err
	}

	return Built{
		JSON:             manifestJSON,
		RosterHash:       rosterHash,
		RosterFileSHA256: rosterFileSHA256,
		Participants:     participants,
	}, nil
}

// buildParticipant constructs a Participant from a roster entry and probe records.
// It returns the participant, the parsed probe manifest (if usable), and an error.
func buildParticipant(in Input, rosterParticipant config.Participant) (Participant, *evidence.ProbeManifest, error) {
	p := Participant{
		ID: rosterParticipant.ID,
	}

	endpoint := findEndpoint(in.Roster, rosterParticipant.Endpoint)
	if endpoint == nil {
		return Participant{}, nil, fmt.Errorf("endpoint %q not found", rosterParticipant.Endpoint)
	}

	record, found, err := in.Store.LatestProbeRecord(endpoint.Name, rosterParticipant.Model)
	if err != nil {
		return Participant{}, nil, fmt.Errorf("lookup probe record for %s/%s: %w", endpoint.Name, rosterParticipant.Model, err)
	}

	if !found {
		p.Detail = "no_record"
		return p, nil, nil
	}

	probeManifest, hasFingerprint, err := evidence.ParseProbeManifest(record.Manifest)
	if err != nil {
		return Participant{}, nil, fmt.Errorf("parse probe manifest for %s/%s: %w", endpoint.Name, rosterParticipant.Model, err)
	}

	if !hasFingerprint {
		p.Detail = "no_fingerprint"
		return p, nil, nil
	}

	currentFingerprint, err := evidence.ConfigFingerprint(*endpoint, rosterParticipant.Model, in.AdapterVersion(rosterParticipant))
	if err != nil {
		return Participant{}, nil, fmt.Errorf("compute fingerprint for %s/%s: %w", endpoint.Name, rosterParticipant.Model, err)
	}

	if probeManifest.ConfigFingerprint != currentFingerprint {
		p.Detail = "fingerprint_mismatch"
		return p, nil, nil
	}

	p.Usable = true
	p.Expected = transport.Expected{
		Identity:      probeManifest.Export.Identity,
		ReportedModel: probeManifest.Export.ReportedModel,
	}
	p.EligibleRequiredReviewer = probeManifest.Export.EligibleRequiredReviewer
	p.ToolCalls = probeManifest.Export.ToolCalls
	p.ProbeObservedAt = &probeManifest.Export.ObservedAt

	if probeManifest.Export.ContextBound != nil {
		p.ContextBound = probeManifest.Export.ContextBound
		p.ContextSource = "probe"
	} else if rosterParticipant.ContextTokens != nil {
		p.ContextBound = rosterParticipant.ContextTokens
		p.ContextSource = "roster"
	}

	if probeManifest.Export.OutputBound != nil {
		p.OutputBound = probeManifest.Export.OutputBound
	}

	calibrationSafety := 0.75
	if in.Roster.Budgets.CalibrationSafety != nil {
		calibrationSafety = *in.Roster.Budgets.CalibrationSafety
	}

	if probeManifest.Calibration.Available {
		bytesPerToken := probeManifest.Calibration.BytesPerToken * calibrationSafety
		p.BytesPerToken = &bytesPerToken
		p.BytesPerTokenSource = "probe"
		calibrated := probeManifest.Calibration.BytesPerToken
		p.CalibratedBytesPerToken = &calibrated
	} else if rosterParticipant.BytesPerToken != nil {
		p.BytesPerToken = rosterParticipant.BytesPerToken
		p.BytesPerTokenSource = "roster"
	}

	return p, &probeManifest, nil
}

// findEndpoint finds an endpoint profile by name.
func findEndpoint(roster *config.Roster, name string) *config.EndpointProfile {
	for i := range roster.Endpoints {
		if roster.Endpoints[i].Name == name {
			return &roster.Endpoints[i]
		}
	}
	return nil
}

// buildManifestJSON builds the canonical manifest JSON with sorted keys.
func buildManifestJSON(in Input, participants map[string]Participant, probeManifests map[string]*evidence.ProbeManifest) (json.RawMessage, string, string, error) {
	rosterFileSHA256 := hashBytes(in.RosterFile)

	rosterMap, err := rosterToMap(in.Roster)
	if err != nil {
		return nil, "", "", fmt.Errorf("convert roster to map: %w", err)
	}

	probeRecords := make(map[string]json.RawMessage)
	expectedMap := make(map[string]map[string]interface{})

	for id, p := range participants {
		if p.Usable {
			if probeManifest, ok := probeManifests[id]; ok {
				probeData, err := json.Marshal(probeManifest)
				if err != nil {
					return nil, "", "", fmt.Errorf("marshal probe manifest for %s: %w", id, err)
				}
				probeRecords[id] = json.RawMessage(probeData)

				expectedMap[id] = map[string]interface{}{
					"identity":       p.Expected.Identity,
					"reported_model": p.Expected.ReportedModel,
				}
			}
		}
	}

	manifest := map[string]interface{}{
		"kind":               "engine",
		"engine_version":     in.EngineVersion,
		"started_at":         in.StartedAt.UTC().Format(time.RFC3339Nano),
		"roster":             rosterMap,
		"roster_path":        in.RosterPath,
		"roster_file_sha256": rosterFileSHA256,
		"probe_records":      probeRecords,
		"expected":           expectedMap,
		// The definitions hash is recorded whenever the manifest is built,
		// whatever the presets enable, so a change to any tool definition
		// changes the manifest.
		"tool_definitions_hash": tools.DefinitionsHash(),
	}

	manifestJSON, err := canonicalJSON(manifest)
	if err != nil {
		return nil, "", "", fmt.Errorf("marshal manifest: %w", err)
	}

	rosterHash, err := computeRosterHash(rosterMap)
	if err != nil {
		return nil, "", "", fmt.Errorf("compute roster hash: %w", err)
	}

	return json.RawMessage(manifestJSON), rosterHash, rosterFileSHA256, nil
}

// rosterToMap converts a Roster to a map suitable for JSON marshaling.
func rosterToMap(roster *config.Roster) (map[string]interface{}, error) {
	roMap := make(map[string]interface{})

	participants := make([]map[string]interface{}, len(roster.Participants))
	for i, p := range roster.Participants {
		pMap := map[string]interface{}{
			"id":             p.ID,
			"model":          p.Model,
			"route":          p.Route,
			"endpoint":       p.Endpoint,
			"eligible_roles": p.EligibleRoles,
			"pool":           p.Pool,
		}
		if p.Binary != "" {
			pMap["binary"] = p.Binary
		}
		if p.ContextTokens != nil {
			pMap["context_tokens"] = *p.ContextTokens
		}
		if p.BytesPerToken != nil {
			pMap["bytes_per_token"] = *p.BytesPerToken
		}
		participants[i] = pMap
	}
	roMap["participants"] = participants

	endpoints := make([]map[string]interface{}, len(roster.Endpoints))
	for i, e := range roster.Endpoints {
		eMap := map[string]interface{}{
			"name":               e.Name,
			"base_url":           e.BaseURL,
			"declared_location":  e.DeclaredLocation,
			"declared_execution": e.DeclaredExecution,
			"declared_billing":   e.DeclaredBilling,
			"network_dependency": e.NetworkDependency,
			"dialect":            e.Dialect,
			"max_tokens_field":   e.MaxTokensField,
		}
		if e.Auth != nil {
			eMap["auth"] = e.Auth.String()
		}
		if len(e.ExtraHeaders) > 0 {
			headers := make(map[string]string)
			for k, v := range e.ExtraHeaders {
				headers[k] = v.String()
			}
			eMap["extra_headers"] = headers
		}
		if len(e.Models) > 0 {
			eMap["models"] = e.Models
		}
		if e.TermsNote != "" {
			eMap["terms_note"] = e.TermsNote
		}
		if e.TokensPerByteMax != nil {
			eMap["tokens_per_byte_max"] = *e.TokensPerByteMax
		}
		if e.SpendingControl != "" {
			eMap["spending_control"] = e.SpendingControl
		}
		if e.RefereeModel != "" {
			eMap["referee_model"] = e.RefereeModel
			eMap["referee_template_hash"] = referee.TemplateHash()
		}
		if e.RefereeEndpoint != "" {
			eMap["referee_endpoint"] = e.RefereeEndpoint
		}
		endpoints[i] = eMap
	}
	roMap["endpoints"] = endpoints

	presets := make(map[string]map[string]interface{})
	for name, preset := range roster.Presets {
		pMap := map[string]interface{}{
			"mode":   preset.Mode,
			"author": preset.Author,
		}
		if len(preset.RequiredReviewers) > 0 {
			pMap["required_reviewers"] = preset.RequiredReviewers
		}
		if len(preset.Tools) > 0 {
			pMap["tools"] = preset.Tools
		}
		if len(preset.ReviewerTools) > 0 {
			pMap["reviewer_tools"] = preset.ReviewerTools
		}
		if preset.Deadlines != nil {
			dMap := make(map[string]interface{})
			if preset.Deadlines.Attempt != nil {
				dMap["attempt"] = preset.Deadlines.Attempt.String()
			}
			if preset.Deadlines.Turn != nil {
				dMap["turn"] = preset.Deadlines.Turn.String()
			}
			if preset.Deadlines.Delivery != nil {
				dMap["delivery"] = preset.Deadlines.Delivery.String()
			}
			if preset.Deadlines.Provisional != nil {
				dMap["provisional"] = *preset.Deadlines.Provisional
			}
			if len(dMap) > 0 {
				pMap["deadlines"] = dMap
			}
		}
		if preset.Budgets != nil {
			bMap := make(map[string]interface{})
			if preset.Budgets.CalibrationSafety != nil {
				bMap["calibration_safety"] = *preset.Budgets.CalibrationSafety
			}
			if preset.Budgets.MessageOverheadTokens != nil {
				bMap["message_overhead_tokens"] = *preset.Budgets.MessageOverheadTokens
			}
			if preset.Budgets.RequestOverheadTokens != nil {
				bMap["request_overhead_tokens"] = *preset.Budgets.RequestOverheadTokens
			}
			if preset.Budgets.ContextMargin != nil {
				bMap["context_margin"] = *preset.Budgets.ContextMargin
			}
			if preset.Budgets.AuthorMaxOutputTokens != nil {
				bMap["author_max_output_tokens"] = *preset.Budgets.AuthorMaxOutputTokens
			}
			if preset.Budgets.ReviewerMaxOutputTokens != nil {
				bMap["reviewer_max_output_tokens"] = *preset.Budgets.ReviewerMaxOutputTokens
			}
			if preset.Budgets.DeliveryMaxTokens != nil {
				bMap["delivery_max_tokens"] = *preset.Budgets.DeliveryMaxTokens
			}
			if preset.Budgets.TurnMaxEstimatedTokens != nil {
				bMap["turn_max_estimated_tokens"] = *preset.Budgets.TurnMaxEstimatedTokens
			}
			if len(bMap) > 0 {
				pMap["budgets"] = bMap
			}
		}
		if preset.AllowWebWithRead != nil {
			pMap["allow_web_with_read"] = *preset.AllowWebWithRead
		}
		presets[name] = pMap
	}
	roMap["presets"] = presets

	policy := map[string]interface{}{
		"local_only": roster.Policy.LocalOnly,
	}
	if len(roster.Policy.AllowedDestinations) > 0 {
		policy["allowed_destinations"] = roster.Policy.AllowedDestinations
	}
	roMap["policy"] = policy

	deadlines := make(map[string]interface{})
	if roster.Deadlines.Attempt != nil {
		deadlines["attempt"] = roster.Deadlines.Attempt.String()
	}
	if roster.Deadlines.Turn != nil {
		deadlines["turn"] = roster.Deadlines.Turn.String()
	}
	if roster.Deadlines.Delivery != nil {
		deadlines["delivery"] = roster.Deadlines.Delivery.String()
	}
	if roster.Deadlines.Provisional != nil {
		deadlines["provisional"] = *roster.Deadlines.Provisional
	}
	roMap["deadlines"] = deadlines

	budgets := make(map[string]interface{})
	if roster.Budgets.CalibrationSafety != nil {
		budgets["calibration_safety"] = *roster.Budgets.CalibrationSafety
	}
	if roster.Budgets.MessageOverheadTokens != nil {
		budgets["message_overhead_tokens"] = *roster.Budgets.MessageOverheadTokens
	}
	if roster.Budgets.RequestOverheadTokens != nil {
		budgets["request_overhead_tokens"] = *roster.Budgets.RequestOverheadTokens
	}
	if roster.Budgets.ContextMargin != nil {
		budgets["context_margin"] = *roster.Budgets.ContextMargin
	}
	if roster.Budgets.AuthorMaxOutputTokens != nil {
		budgets["author_max_output_tokens"] = *roster.Budgets.AuthorMaxOutputTokens
	}
	if roster.Budgets.ReviewerMaxOutputTokens != nil {
		budgets["reviewer_max_output_tokens"] = *roster.Budgets.ReviewerMaxOutputTokens
	}
	if roster.Budgets.DeliveryMaxTokens != nil {
		budgets["delivery_max_tokens"] = *roster.Budgets.DeliveryMaxTokens
	}
	if roster.Budgets.TurnMaxEstimatedTokens != nil {
		budgets["turn_max_estimated_tokens"] = *roster.Budgets.TurnMaxEstimatedTokens
	}
	roMap["budgets"] = budgets

	engine := make(map[string]interface{})
	if roster.Engine.StopGrace != nil {
		engine["stop_grace"] = roster.Engine.StopGrace.String()
	}
	roMap["engine"] = engine

	pools := make(map[string]map[string]interface{})
	for name, pool := range roster.Pools {
		pMap := make(map[string]interface{})
		if pool.Concurrency != nil {
			pMap["concurrency"] = *pool.Concurrency
		}
		if pool.UncertainReleaseAfter != nil {
			pMap["uncertain_release_after"] = pool.UncertainReleaseAfter.String()
		}
		pools[name] = pMap
	}
	roMap["pools"] = pools

	quick := make(map[string]map[string]interface{})
	for name, qp := range roster.Quick {
		qMap := map[string]interface{}{
			"preset": qp.Preset,
			"drafts": qp.Drafts,
		}
		if qp.Budgets != nil {
			bMap := make(map[string]interface{})
			if qp.Budgets.CalibrationSafety != nil {
				bMap["calibration_safety"] = *qp.Budgets.CalibrationSafety
			}
			if qp.Budgets.MessageOverheadTokens != nil {
				bMap["message_overhead_tokens"] = *qp.Budgets.MessageOverheadTokens
			}
			if qp.Budgets.RequestOverheadTokens != nil {
				bMap["request_overhead_tokens"] = *qp.Budgets.RequestOverheadTokens
			}
			if qp.Budgets.ContextMargin != nil {
				bMap["context_margin"] = *qp.Budgets.ContextMargin
			}
			if qp.Budgets.AuthorMaxOutputTokens != nil {
				bMap["author_max_output_tokens"] = *qp.Budgets.AuthorMaxOutputTokens
			}
			if qp.Budgets.ReviewerMaxOutputTokens != nil {
				bMap["reviewer_max_output_tokens"] = *qp.Budgets.ReviewerMaxOutputTokens
			}
			if qp.Budgets.DeliveryMaxTokens != nil {
				bMap["delivery_max_tokens"] = *qp.Budgets.DeliveryMaxTokens
			}
			if qp.Budgets.TurnMaxEstimatedTokens != nil {
				bMap["turn_max_estimated_tokens"] = *qp.Budgets.TurnMaxEstimatedTokens
			}
			if len(bMap) > 0 {
				qMap["budgets"] = bMap
			}
		}
		if qp.EligibleRequestCategories != nil {
			qMap["eligible_request_categories"] = qp.EligibleRequestCategories
		}
		if qp.ProtectedPaths != nil {
			qMap["protected_paths"] = qp.ProtectedPaths
		}
		if qp.RequiredReviewTriggers != nil {
			qMap["required_review_triggers"] = qp.RequiredReviewTriggers
		}
		if qp.OutputEscalationRules != nil {
			qMap["output_escalation_rules"] = qp.OutputEscalationRules
		}
		quick[name] = qMap
	}
	roMap["quick"] = quick

	if roster.Tools != nil {
		toolsMap := make(map[string]interface{})
		if roster.Tools.AllowWebWithRead != nil {
			toolsMap["allow_web_with_read"] = *roster.Tools.AllowWebWithRead
		}
		if len(roster.Tools.ProtectedPaths) > 0 {
			toolsMap["protected_paths"] = roster.Tools.ProtectedPaths
		}
		if len(roster.Tools.FileEgressDestinations) > 0 {
			toolsMap["file_egress_destinations"] = roster.Tools.FileEgressDestinations
		}
		if roster.Tools.MaxFetchesPerTurn != nil {
			toolsMap["max_fetches_per_turn"] = *roster.Tools.MaxFetchesPerTurn
		}
		if roster.Tools.MaxCallsPerTurn != nil {
			toolsMap["max_calls_per_turn"] = *roster.Tools.MaxCallsPerTurn
		}
		if roster.Tools.MaxReviewerCalls != nil {
			toolsMap["max_reviewer_calls"] = *roster.Tools.MaxReviewerCalls
		}
		if roster.Tools.MaxResultBytes != nil {
			toolsMap["max_result_bytes"] = *roster.Tools.MaxResultBytes
		}
		if roster.Tools.Web != nil {
			webMap := make(map[string]interface{})
			if len(roster.Tools.Web.AllowedHosts) > 0 {
				webMap["allowed_hosts"] = roster.Tools.Web.AllowedHosts
			}
			if len(webMap) > 0 {
				toolsMap["web"] = webMap
			}
		}
		if roster.Tools.Referee != nil {
			refereeMap := make(map[string]interface{})
			if roster.Tools.Referee.MaxOutputTokens != nil {
				refereeMap["max_output_tokens"] = *roster.Tools.Referee.MaxOutputTokens
			}
			if len(refereeMap) > 0 {
				toolsMap["referee"] = refereeMap
			}
		}
		if len(toolsMap) > 0 {
			roMap["tools"] = toolsMap
		}
	}

	roMap["default_preset"] = roster.DefaultPreset

	return roMap, nil
}

// canonicalJSON marshals an object to canonical JSON with sorted keys at every level.
func canonicalJSON(v interface{}) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var obj interface{}
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	return encodeCanonical(obj)
}

// encodeCanonical encodes a value to canonical JSON with sorted keys at every level.
func encodeCanonical(v interface{}) ([]byte, error) {
	switch val := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		var buf bytes.Buffer
		buf.WriteString("{")
		for i, k := range keys {
			if i > 0 {
				buf.WriteString(",")
			}
			keyBytes, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			buf.Write(keyBytes)
			buf.WriteString(":")
			valBytes, err := encodeCanonical(val[k])
			if err != nil {
				return nil, err
			}
			buf.Write(valBytes)
		}
		buf.WriteString("}")
		return buf.Bytes(), nil

	case []interface{}:
		var buf bytes.Buffer
		buf.WriteString("[")
		for i, item := range val {
			if i > 0 {
				buf.WriteString(",")
			}
			itemBytes, err := encodeCanonical(item)
			if err != nil {
				return nil, err
			}
			buf.Write(itemBytes)
		}
		buf.WriteString("]")
		return buf.Bytes(), nil

	default:
		return json.Marshal(v)
	}
}

// computeRosterHash computes the SHA-256 hash of the roster member.
func computeRosterHash(roster map[string]interface{}) (string, error) {
	data, err := canonicalJSON(roster)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// hashBytes computes SHA-256 hash of bytes.
func hashBytes(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// RosterFileChanged reports whether the file at path has a different SHA-256 from sha256AtStart.
// A file that cannot be read is reported as changed, with nil error only when the cause is that it does not exist.
func RosterFileChanged(path, sha256AtStart string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return true, err
	}
	currentSHA256 := hashBytes(data)
	return currentSHA256 != sha256AtStart, nil
}
