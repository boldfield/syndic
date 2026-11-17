package transport

import "strings"

// Scrub performs exact-match substitution of each non-empty secret with ScrubMarker.
// Secrets are sorted by length (longest first) so a secret that is a prefix of another
// is not left half-replaced.
func Scrub(output string, secrets []string) (string, int) {
	if len(secrets) == 0 {
		return output, 0
	}

	// Filter out empty secrets and sort by length (longest first)
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
	for i := 0; i < len(filtered); i++ {
		for j := i + 1; j < len(filtered); j++ {
			if len(filtered[j]) > len(filtered[i]) {
				filtered[i], filtered[j] = filtered[j], filtered[i]
			}
		}
	}

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
