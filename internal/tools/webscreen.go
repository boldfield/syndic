// Package tools holds web pre-send screening logic for canary and shape checks.
package tools

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"math"
	"net/url"
	"strings"
)

// ScreenResult represents the outcome of a pre-send check.
type ScreenResult struct {
	Allowed bool
	Reason  string
}

// URLScreener performs pre-DNS canary and shape checks on URLs.
type URLScreener struct {
	fileBytes    []byte
	hostFetches  map[string]int
	totalFetches int
	maxPerTurn   int
}

// NewURLScreener creates a new URL screener for a turn.
func NewURLScreener(maxPerTurn int) *URLScreener {
	return &URLScreener{
		fileBytes:    []byte{},
		hostFetches:  make(map[string]int),
		totalFetches: 0,
		maxPerTurn:   maxPerTurn,
	}
}

// AddFileBytes adds file-derived content to the canary corpus for this turn.
func (s *URLScreener) AddFileBytes(data []byte) {
	s.fileBytes = append(s.fileBytes, data...)
}

// Screen performs pre-DNS checks on a URL and tracks fetch counts.
// Rules applied in order:
// 1. Scheme check (http/https only)
// 2. Canary check (16+ byte sequences from file-derived content)
// 3. Shape checks (length, entropy, host counts)
func (s *URLScreener) Screen(urlStr string) ScreenResult {
	// Scheme check
	if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
		return ScreenResult{Allowed: false, Reason: "scheme: only http and https allowed"}
	}

	// Parse URL
	u, err := url.Parse(urlStr)
	if err != nil {
		return ScreenResult{Allowed: false, Reason: fmt.Sprintf("invalid URL: %v", err)}
	}

	// URL length check
	if len(urlStr) > 2048 {
		return ScreenResult{Allowed: false, Reason: "url_length: exceeds 2048 bytes"}
	}

	// Hostname validation and canary check on hostname
	hostname := u.Hostname()
	if hostname == "" {
		return ScreenResult{Allowed: false, Reason: "hostname: empty hostname"}
	}

	if len(hostname) > 253 {
		return ScreenResult{Allowed: false, Reason: "hostname_length: exceeds 253 bytes"}
	}

	// Check hostname labels
	labels := strings.Split(hostname, ".")
	for _, label := range labels {
		if len(label) > 63 {
			return ScreenResult{Allowed: false, Reason: "label_length: exceeds 63 bytes"}
		}

		// Canary check on hostname label
		if !s.canaryCheck(label) {
			return ScreenResult{Allowed: false, Reason: "canary: match in hostname"}
		}

		// Shape entropy check on hostname label
		if !s.shapeCheck(label) {
			return ScreenResult{Allowed: false, Reason: "shape: high entropy in hostname label"}
		}
	}

	// Check path segments
	pathSegments := strings.Split(strings.Trim(u.Path, "/"), "/")
	for _, segment := range pathSegments {
		if segment == "" {
			continue
		}

		// Canary check on path segment
		if !s.canaryCheck(segment) {
			return ScreenResult{Allowed: false, Reason: "canary: match in path"}
		}

		// Shape entropy check on path segment
		if !s.shapeCheck(segment) {
			return ScreenResult{Allowed: false, Reason: "shape: high entropy in path"}
		}
	}

	// Check query values
	for _, values := range u.Query() {
		for _, val := range values {
			// Canary check on query value
			if !s.canaryCheck(val) {
				return ScreenResult{Allowed: false, Reason: "canary: match in query"}
			}

			// Shape entropy check on query value
			if !s.shapeCheck(val) {
				return ScreenResult{Allowed: false, Reason: "shape: high entropy in query"}
			}
		}
	}

	// Track host fetch count
	host := u.Host
	s.hostFetches[host]++
	if s.hostFetches[host] > 10 {
		return ScreenResult{Allowed: false, Reason: fmt.Sprintf("host_count: exceeds 10 fetches to %s", host)}
	}

	// Track total fetch count
	s.totalFetches++
	if s.totalFetches > 20 {
		return ScreenResult{Allowed: false, Reason: "turn_count: exceeds 20 total fetches in turn"}
	}

	return ScreenResult{Allowed: true}
}

// canaryCheck checks if a string contains any 16+ byte run from file-derived content,
// after decoding attempts (percent, base64, base64url, hex, and two-level nesting).
func (s *URLScreener) canaryCheck(segment string) bool {
	// Generate all possible decodings of the segment
	decodings := s.allDecodings(segment)

	// Check each decoded version against file bytes
	for _, decoded := range decodings {
		if s.containsFileBytes(decoded) {
			return false
		}
	}

	return true
}

// allDecodings returns all possible decodings of a segment up to depth 2.
func (s *URLScreener) allDecodings(segment string) []string {
	result := []string{segment}

	// Level 1 decodings
	level1 := []string{
		segment,
		s.tryPercentDecode(segment),
		s.tryBase64Decode(segment),
		s.tryBase64URLDecode(segment),
		s.tryHexDecode(segment),
	}

	// Remove duplicates and add to result
	for _, l1 := range level1 {
		if l1 != "" && l1 != segment {
			result = append(result, l1)

			// Level 2 decodings (nesting)
			level2 := []string{
				s.tryPercentDecode(l1),
				s.tryBase64Decode(l1),
				s.tryBase64URLDecode(l1),
				s.tryHexDecode(l1),
			}
			for _, l2 := range level2 {
				if l2 != "" && l2 != segment && l2 != l1 {
					result = append(result, l2)
				}
			}
		}
	}

	return result
}

// tryPercentDecode attempts to percent-decode a string.
func (s *URLScreener) tryPercentDecode(segment string) string {
	decoded, err := url.QueryUnescape(segment)
	if err != nil {
		return ""
	}
	return decoded
}

// tryBase64Decode attempts to base64-decode a string.
func (s *URLScreener) tryBase64Decode(segment string) string {
	decoded, err := base64.StdEncoding.DecodeString(segment)
	if err != nil {
		return ""
	}
	return string(decoded)
}

// tryBase64URLDecode attempts to base64url-decode a string.
func (s *URLScreener) tryBase64URLDecode(segment string) string {
	decoded, err := base64.URLEncoding.DecodeString(segment)
	if err != nil {
		return ""
	}
	return string(decoded)
}

// tryHexDecode attempts to hex-decode a string.
func (s *URLScreener) tryHexDecode(segment string) string {
	if len(segment)%2 != 0 {
		return ""
	}
	var buf bytes.Buffer
	for i := 0; i < len(segment); i += 2 {
		var b byte
		if n, err := fmt.Sscanf(segment[i:i+2], "%02x", &b); err != nil || n != 1 {
			return ""
		}
		buf.WriteByte(b)
	}
	return buf.String()
}

// containsFileBytes checks if decoded contains any 16-byte run from file-derived content.
func (s *URLScreener) containsFileBytes(decoded string) bool {
	if len(decoded) < 16 {
		return false
	}

	decodedBytes := []byte(decoded)
	for i := 0; i <= len(decodedBytes)-16; i++ {
		chunk := decodedBytes[i : i+16]
		if bytes.Contains(s.fileBytes, chunk) {
			return true
		}
	}

	return false
}

// shapeCheck checks if a segment is too long or has excessive entropy.
func (s *URLScreener) shapeCheck(segment string) bool {
	if len(segment) > 48 {
		if s.shannonEntropy(segment) > 4.5 {
			return false
		}
	}
	return true
}

// shannonEntropy calculates the Shannon entropy of a string in bits per byte.
func (s *URLScreener) shannonEntropy(segment string) float64 {
	if len(segment) == 0 {
		return 0
	}

	// Count byte frequencies
	freq := make(map[byte]int)
	for i := 0; i < len(segment); i++ {
		freq[segment[i]]++
	}

	// Calculate entropy
	entropy := 0.0
	length := float64(len(segment))
	for _, count := range freq {
		if count > 0 {
			p := float64(count) / length
			entropy -= p * math.Log2(p)
		}
	}

	return entropy
}
