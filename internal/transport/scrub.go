package transport

import (
	"slices"
	"strings"
)

// Scrub performs exact-match substitution of each non-empty secret with ScrubMarker.
// Secrets are sorted by length (longest first) so a secret that is a prefix of another
// is not left half-replaced.
func Scrub(output string, secrets []string) (string, int) {
	// Filter out empty secrets
	filtered := make([]string, 0, len(secrets))
	for _, s := range secrets {
		if s != "" {
			filtered = append(filtered, s)
		}
	}

	if len(filtered) == 0 {
		return output, 0
	}

	// Sort by length descending (longest first)
	slices.SortFunc(filtered, func(a, b string) int {
		if len(b) != len(a) {
			return len(b) - len(a)
		}
		return 0
	})

	result := output
	count := 0

	for _, secret := range filtered {
		// Count occurrences and replace
		occurrences := strings.Count(result, secret)
		if occurrences > 0 {
			result = strings.ReplaceAll(result, secret, ScrubMarker)
			count += occurrences
		}
	}

	return result, count
}
