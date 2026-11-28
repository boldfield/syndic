package openai

import (
	"bytes"
	"encoding/json"
	"strings"
)

// quotaErrorCodes are the error.code values that mean the account may not
// spend (v10 delta section 7, "Quota markers").
var quotaErrorCodes = map[string]bool{
	"insufficient_quota":                true,
	"credit_balance_exhausted":          true,
	"organization_spend_limit_exceeded": true,
	"project_spend_limit_exceeded":      true,
	"organization_usage_limit_exceeded": true,
}

const (
	quotaErrorType         = "insufficient_quota"
	quotaDetailsErrorCode  = "enforced_spend_limit_reached"
	quotaSpendLimitType    = "invalid_request_error"
	quotaSpendLimitMessage = "You have reached your specified"
)

// hasQuotaMarker reports whether a response body carries a quota marker at
// one of the documented paths. A 402 needs no marker and is not judged here.
// A body that is empty or not JSON has no marker.
func hasQuotaMarker(status int, body []byte) bool {
	if len(bytes.TrimSpace(body)) == 0 {
		return false
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return false
	}
	// Map lookups are case-sensitive, unlike struct tags, so only the
	// documented member names are read.
	e, ok := doc["error"].(map[string]any)
	if !ok {
		return false
	}
	if code, ok := e["code"].(string); ok && quotaErrorCodes[code] {
		return true
	}
	if typ, ok := e["type"].(string); ok && typ == quotaErrorType {
		return true
	}
	if d, ok := e["details"].(map[string]any); ok {
		if code, ok := d["error_code"].(string); ok && code == quotaDetailsErrorCode {
			return true
		}
	}
	if status == 400 {
		typ, _ := e["type"].(string)
		msg, _ := e["message"].(string)
		if typ == quotaSpendLimitType && strings.HasPrefix(msg, quotaSpendLimitMessage) {
			return true
		}
	}
	return false
}
