package openai

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/transport"
)

func quotaFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "quota", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func classifyQuotaBody(status int, body []byte) transport.FailureClass {
	return transport.Classify(transport.ClassifyInput{
		Received:    true,
		HTTPStatus:  status,
		QuotaMarker: hasQuotaMarker(status, body),
	})
}

func TestQuotaMarkerRows(t *testing.T) {
	rows := []struct {
		name   string
		status int
	}{
		{"code_insufficient_quota", 429},
		{"code_credit_balance_exhausted", 400},
		{"code_organization_spend_limit_exceeded", 429},
		{"code_project_spend_limit_exceeded", 429},
		{"code_organization_usage_limit_exceeded", 429},
		{"type_insufficient_quota", 429},
		{"details_enforced_spend_limit_reached", 429},
		{"status400_spend_limit", 400},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if got := classifyQuotaBody(r.status, quotaFixture(t, r.name)); got != transport.FailureClassQuota {
				t.Errorf("right path: class %q, want quota", got)
			}
			if got := classifyQuotaBody(r.status, quotaFixture(t, r.name+".wrongpath")); got == transport.FailureClassQuota {
				t.Errorf("other path: class %q, want not quota", got)
			}
		})
	}
}

func TestQuotaMarker400RowOnlyOn400(t *testing.T) {
	body := quotaFixture(t, "status400_spend_limit")
	for _, status := range []int{401, 403, 429, 500} {
		if hasQuotaMarker(status, body) {
			t.Errorf("status %d: marker matched, want none", status)
		}
	}
}

func TestQuotaMarkerThrottledWithoutMarker(t *testing.T) {
	for _, name := range []string{"throttled_ramp_limit_exceeded", "throttled_slow_down"} {
		t.Run(name, func(t *testing.T) {
			if got := classifyQuotaBody(429, quotaFixture(t, name)); got != transport.FailureClassThrottled {
				t.Errorf("class %q, want throttled", got)
			}
		})
	}
}

func TestQuotaMarkerNotJSON(t *testing.T) {
	for _, body := range []string{"", "  \n", "insufficient_quota", "<html>insufficient_quota</html>", "null", "[]", `{"error":"insufficient_quota"}`} {
		if hasQuotaMarker(429, []byte(body)) {
			t.Errorf("body %q: marker matched, want none", strings.TrimSpace(body))
		}
	}
}
