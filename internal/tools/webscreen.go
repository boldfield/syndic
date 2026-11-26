package tools

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
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
	corpusSequences [][]byte // Keep file-derived sequences separate
	hostFetches     map[string]int
	totalFetches    int
	maxPerTurn      int
}

// NewURLScreener creates a new URL screener for a turn.
func NewURLScreener(maxPerTurn int) *URLScreener {
	return &URLScreener{
		corpusSequences: [][]byte{},
		hostFetches:     make(map[string]int),
		totalFetches:    0,
		maxPerTurn:      maxPerTurn,
	}
}

// AddFileBytes adds file-derived content (file snapshot) to the canary corpus for this turn.
func (s *URLScreener) AddFileBytes(data []byte) {
	s.corpusSequences = append(s.corpusSequences, bytes.Clone(data))
}

// AddHistoryBytes adds history document content to the canary corpus.
func (s *URLScreener) AddHistoryBytes(data []byte) {
	s.corpusSequences = append(s.corpusSequences, bytes.Clone(data))
}

// AddDiffBytes adds diff content to the canary corpus.
func (s *URLScreener) AddDiffBytes(data []byte) {
	s.corpusSequences = append(s.corpusSequences, bytes.Clone(data))
}

// AddBaseBytes adds base content to the canary corpus.
func (s *URLScreener) AddBaseBytes(data []byte) {
	s.corpusSequences = append(s.corpusSequences, bytes.Clone(data))
}

// canonicalHost returns the canonical form of a hostname: lowercase with default port stripped.
func (s *URLScreener) canonicalHost(u *url.URL) string {
	hostname := u.Hostname()
	if hostname == "" {
		return ""
	}
	hostname = strings.ToLower(hostname)

	// Strip default ports
	port := u.Port()
	if port == "" || (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		return hostname
	}
	return hostname + ":" + port
}

// Screen performs pre-DNS checks on a URL and tracks fetch counts.
// Rules applied in order:
// 1. Scheme check (http/https only)
// 2. URL length check
// 3. Hostname validation
// 4. Canary check (16+ byte sequences from file-derived content)
// 5. Shape checks (entropy, host counts)
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

	// Hostname validation
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
	}

	// Check entire percent-decoded URL for 16-byte sequences from corpus
	// Use lenient percent-decoding (doesn't treat + as space like QueryUnescape does)
	decodedURL := s.lenientPercentDecode(urlStr)

	if !s.canaryCheckURL(decodedURL) {
		return ScreenResult{Allowed: false, Reason: "canary: match in URL"}
	}

	// For standard base64, also check with spaces mapped back to + (since + in URLs becomes space after QueryUnescape)
	decodedURLWithPlus := strings.ReplaceAll(decodedURL, " ", "+")
	if !s.canaryCheckURL(decodedURLWithPlus) {
		return ScreenResult{Allowed: false, Reason: "canary: match in URL"}
	}

	// Check hostname labels with bounded decoding
	for _, label := range labels {
		if !s.canaryCheck(label) {
			return ScreenResult{Allowed: false, Reason: "canary: match in hostname"}
		}
		// Also try with spaces mapped back to + for standard base64 decoding
		labelWithPlus := strings.ReplaceAll(label, " ", "+")
		if !s.canaryCheck(labelWithPlus) {
			return ScreenResult{Allowed: false, Reason: "canary: match in hostname"}
		}
		if !s.shapeCheck(label) {
			return ScreenResult{Allowed: false, Reason: "shape: high entropy in hostname label"}
		}
	}

	// Check userinfo - parse directly from URL to get raw form
	if u.User != nil {
		userStr := u.User.String() // This includes username and password
		// Use lenient percent-decoding for userinfo
		decodedUserinfo := s.lenientPercentDecode(userStr)
		if !s.canaryCheck(decodedUserinfo) {
			return ScreenResult{Allowed: false, Reason: "canary: match in userinfo"}
		}
		// Also try with spaces mapped back to + for standard base64 decoding
		userinfoWithPlus := strings.ReplaceAll(decodedUserinfo, " ", "+")
		if !s.canaryCheck(userinfoWithPlus) {
			return ScreenResult{Allowed: false, Reason: "canary: match in userinfo"}
		}
	}

	// Check path segments from escaped path, then percent-decode each segment
	pathSegments := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	for _, segment := range pathSegments {
		if segment == "" {
			continue
		}
		// Use lenient percent-decoding for path segments (doesn't treat + as space)
		decodedSegment := s.lenientPercentDecode(segment)
		if !s.canaryCheck(decodedSegment) {
			return ScreenResult{Allowed: false, Reason: "canary: match in path"}
		}
		// Also try with spaces mapped back to + for standard base64 decoding
		decodedSegmentWithPlus := strings.ReplaceAll(decodedSegment, " ", "+")
		if !s.canaryCheck(decodedSegmentWithPlus) {
			return ScreenResult{Allowed: false, Reason: "canary: match in path"}
		}
		if !s.shapeCheck(decodedSegment) {
			return ScreenResult{Allowed: false, Reason: "shape: high entropy in path"}
		}
	}

	// Check query keys and values from raw query
	rawQuery := u.RawQuery
	if rawQuery != "" {
		for _, pair := range strings.Split(rawQuery, "&") {
			parts := strings.SplitN(pair, "=", 2)
			key := parts[0]
			val := ""
			if len(parts) > 1 {
				val = parts[1]
			}
			// Use lenient percent-decoding for key and value (doesn't treat + as space)
			decodedKey := s.lenientPercentDecode(key)
			decodedVal := s.lenientPercentDecode(val)
			// For standard base64, also try with spaces mapped back to +
			decodedValWithPlus := strings.ReplaceAll(decodedVal, " ", "+")

			if !s.canaryCheck(decodedKey) {
				return ScreenResult{Allowed: false, Reason: "canary: match in query key"}
			}
			if !s.canaryCheck(decodedVal) {
				return ScreenResult{Allowed: false, Reason: "canary: match in query value"}
			}
			if !s.canaryCheck(decodedValWithPlus) {
				return ScreenResult{Allowed: false, Reason: "canary: match in query value"}
			}
			if !s.shapeCheck(decodedVal) {
				return ScreenResult{Allowed: false, Reason: "shape: high entropy in query value"}
			}
		}
	}

	// Count and check host fetch limit
	canHost := s.canonicalHost(u)
	if s.hostFetches[canHost]+1 > 10 {
		return ScreenResult{Allowed: false, Reason: fmt.Sprintf("host_count: exceeds 10 fetches to %s", canHost)}
	}

	// Count and check total fetch limit
	if s.totalFetches+1 > s.maxPerTurn {
		return ScreenResult{Allowed: false, Reason: fmt.Sprintf("turn_count: exceeds %d total fetches in turn", s.maxPerTurn)}
	}

	// All checks passed, increment counters
	s.hostFetches[canHost]++
	s.totalFetches++

	return ScreenResult{Allowed: true}
}

// canaryCheckURL checks if a percent-decoded URL contains any 16-byte run from the corpus.
func (s *URLScreener) canaryCheckURL(urlStr string) bool {
	urlBytes := []byte(urlStr)
	for _, seqBytes := range s.corpusSequences {
		if s.containsRunInBytes(urlBytes, seqBytes) {
			return false
		}
	}
	return true
}

// canaryCheck checks if a string contains any 16+ byte run from file-derived content,
// after decoding attempts (percent, base64, base64url, hex, and up to two-level nesting).
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
func (s *URLScreener) allDecodings(segment string) [][]byte {
	result := [][]byte{[]byte(segment)}
	seen := make(map[string]bool)
	seen[segment] = true

	// Level 1 decodings
	level1 := map[string][]byte{
		"percent": s.tryPercentDecode(segment),
		"base64":  s.tryBase64Decode(segment),
		"base64u": s.tryBase64URLDecode(segment),
		"hex":     s.tryHexDecode(segment),
	}

	for _, decoded := range level1 {
		if decoded != nil && !seen[string(decoded)] {
			result = append(result, decoded)
			seen[string(decoded)] = true

			// Level 2 decodings (nesting) - decode only once more
			level2 := [][]byte{
				s.tryPercentDecodeBytes(decoded),
				s.tryBase64DecodeBytes(decoded),
				s.tryBase64URLDecodeBytes(decoded),
				s.tryHexDecodeBytes(decoded),
			}
			for _, l2 := range level2 {
				if l2 != nil && !seen[string(l2)] {
					result = append(result, l2)
					seen[string(l2)] = true
				}
			}
		}
	}

	return result
}

// tryPercentDecode attempts to percent-decode a string using lenient percent-decoding.
func (s *URLScreener) tryPercentDecode(segment string) []byte {
	decoded := s.lenientPercentDecode(segment)
	if decoded == segment {
		return nil
	}
	return []byte(decoded)
}

// tryPercentDecodeBytes attempts to percent-decode bytes.
func (s *URLScreener) tryPercentDecodeBytes(b []byte) []byte {
	return s.tryPercentDecode(string(b))
}

// lenientPercentDecode decodes valid %XX escapes and leaves invalid ones literal.
// Unlike url.QueryUnescape, this doesn't treat + as space (percent-only decoding).
func (s *URLScreener) lenientPercentDecode(s2 string) string {
	var result strings.Builder
	i := 0
	for i < len(s2) {
		if s2[i] == '%' && i+2 < len(s2) {
			// Try to decode %XX
			h1, h2 := s2[i+1], s2[i+2]
			// Check if both characters are valid hex digits
			v1, ok1 := hexCharToValue(h1)
			v2, ok2 := hexCharToValue(h2)
			if ok1 && ok2 {
				// Valid hex escape
				result.WriteByte(v1<<4 | v2)
				i += 3
				continue
			}
		}
		result.WriteByte(s2[i])
		i++
	}
	return result.String()
}

// hexCharToValue converts a hex character to its value (0-15) and a boolean indicating validity.
func hexCharToValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

// tryBase64Decode attempts to base64-decode a string (both padded and unpadded).
func (s *URLScreener) tryBase64Decode(segment string) []byte {
	// Try padded first
	if decoded, err := base64.StdEncoding.DecodeString(segment); err == nil {
		return decoded
	}
	// Try unpadded
	padded := segment + strings.Repeat("=", (4-len(segment)%4)%4)
	if decoded, err := base64.StdEncoding.DecodeString(padded); err == nil {
		return decoded
	}
	return nil
}

// tryBase64DecodeBytes attempts to base64-decode bytes.
func (s *URLScreener) tryBase64DecodeBytes(b []byte) []byte {
	return s.tryBase64Decode(string(b))
}

// tryBase64URLDecode attempts to base64url-decode a string (both padded and unpadded).
func (s *URLScreener) tryBase64URLDecode(segment string) []byte {
	// Try padded first
	if decoded, err := base64.URLEncoding.DecodeString(segment); err == nil {
		return decoded
	}
	// Try unpadded
	padded := segment + strings.Repeat("=", (4-len(segment)%4)%4)
	if decoded, err := base64.URLEncoding.DecodeString(padded); err == nil {
		return decoded
	}
	return nil
}

// tryBase64URLDecodeBytes attempts to base64url-decode bytes.
func (s *URLScreener) tryBase64URLDecodeBytes(b []byte) []byte {
	return s.tryBase64URLDecode(string(b))
}

// tryHexDecode attempts to hex-decode a string.
func (s *URLScreener) tryHexDecode(segment string) []byte {
	if len(segment)%2 != 0 {
		return nil
	}
	decoded, err := hex.DecodeString(segment)
	if err != nil {
		return nil
	}
	return decoded
}

// tryHexDecodeBytes attempts to hex-decode bytes.
func (s *URLScreener) tryHexDecodeBytes(b []byte) []byte {
	return s.tryHexDecode(string(b))
}

// containsRunInBytes checks if haystack contains any 16-byte run from needle.
func (s *URLScreener) containsRunInBytes(haystack, needle []byte) bool {
	if len(needle) < 16 {
		return false
	}
	// Check each 16-byte run in needle
	for i := 0; i <= len(needle)-16; i++ {
		chunk := needle[i : i+16]
		if bytes.Contains(haystack, chunk) {
			return true
		}
	}
	return false
}

// containsFileBytes checks if decoded contains any 16-byte run from file-derived content.
func (s *URLScreener) containsFileBytes(decoded []byte) bool {
	if len(decoded) < 16 {
		return false
	}

	for _, seqBytes := range s.corpusSequences {
		if s.containsRunInBytes(decoded, seqBytes) {
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
