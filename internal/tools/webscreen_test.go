package tools

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// Test 16-byte match rejection
func TestCanaryCheck16Bytes(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("this is a test file content that we want to protect")
	screener.AddFileBytes(fileContent)

	// Extract a 16-byte sequence from file content
	secret := string(fileContent[10:26]) // "a test file con"

	// URL containing this exact sequence should be rejected
	urlStr := "https://example.com/path?query=" + secret
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected URL with 16-byte match to be rejected, got allowed")
	}
}

// Test 15-byte match allowed
func TestCanaryCheck15BytesAllowed(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("this is a test file content that we want to protect")
	screener.AddFileBytes(fileContent)

	// Extract a 15-byte sequence from file content
	secret := string(fileContent[10:25]) // "a test file co"

	// URL containing a 15-byte sequence should be allowed
	urlStr := "https://example.com/path?query=" + secret
	result := screener.Screen(urlStr)
	if !result.Allowed {
		t.Errorf("Expected URL with 15-byte match to be allowed, got rejected: %s", result.Reason)
	}
}

// Test whole URL check: bytes spread across path segments
func TestCanaryCheckWholeURLAcrossSegments(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("docs/design/v5-delta-secret-XYZ")
	screener.AddFileBytes(fileContent)

	// 16-byte sequence: "docs/design/v5-de"
	// Split across path segments
	urlStr := "https://example.com/docs/design/v5-delta"
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected URL with 16-byte run across segments to be rejected, got allowed")
	}
}

// Test query keys are checked
func TestCanaryCheckQueryKey(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secrettoken123456789")
	screener.AddFileBytes(fileContent)

	// 16 bytes of file content
	secret := string(fileContent[0:16]) // "secrettoken12345"

	// URL with secret in query key should be rejected
	urlStr := "https://example.com/path?" + secret + "=value"
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected URL with 16-byte match in query key to be rejected")
	}
}

// Test userinfo is checked
func TestCanaryCheckUserinfo(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secrettoken123456789")
	screener.AddFileBytes(fileContent)

	// 16 bytes of file content
	secret := string(fileContent[0:16]) // "secrettoken12345"

	// URL with secret in userinfo should be rejected
	urlStr := "https://" + secret + "@example.com/path"
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected URL with 16-byte match in userinfo to be rejected")
	}
}

// Test percent-encoding in various components
func TestCanaryCheckPercentEncoded(t *testing.T) {
	screener := NewURLScreener(20)
	secret := "secrettoken12345"
	screener.AddFileBytes([]byte(secret))

	// Percent-encode the secret
	encoded := "%73%65%63%72%65%74%74%6F%6B%65%6E%31%32%33%34%35"
	urlStr := "https://example.com/path?q=" + encoded
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected percent-encoded match to be rejected")
	}
}

// Test base64 encoding (padded)
func TestCanaryCheckBase64Padded(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secretpassword123456")
	screener.AddFileBytes(fileContent)

	// 16-byte portion: "secretpassword12"
	encoded := base64.StdEncoding.EncodeToString([]byte("secretpassword12"))
	urlStr := "https://example.com/path?data=" + encoded
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected base64-encoded match to be rejected")
	}
}

// Test base64 encoding (unpadded)
func TestCanaryCheckBase64Unpadded(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secretpassword123456")
	screener.AddFileBytes(fileContent)

	// 16-byte portion: "secretpassword12"
	encoded := base64.StdEncoding.EncodeToString([]byte("secretpassword12"))
	// Remove padding to get genuinely unpadded base64
	unpadded := strings.TrimRight(encoded, "=")
	urlStr := "https://example.com/path?data=" + unpadded
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected unpadded base64-encoded match to be rejected")
	}
}

// Test base64url encoding (padded)
func TestCanaryCheckBase64URLPadded(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secretpassword123456")
	screener.AddFileBytes(fileContent)

	// 16-byte portion: "secretpassword12"
	encoded := base64.URLEncoding.EncodeToString([]byte("secretpassword12"))
	urlStr := "https://example.com/path?data=" + encoded
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected base64url-encoded match to be rejected")
	}
}

// Test base64url encoding (unpadded)
func TestCanaryCheckBase64URLUnpadded(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secretpassword123456")
	screener.AddFileBytes(fileContent)

	// 16-byte portion: "secretpassword12"
	encoded := base64.URLEncoding.EncodeToString([]byte("secretpassword12"))
	// Remove padding to get genuinely unpadded base64url
	unpadded := strings.TrimRight(encoded, "=")
	urlStr := "https://example.com/path?data=" + unpadded
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected unpadded base64url-encoded match to be rejected")
	}
}

// Test hex encoding
func TestCanaryCheckHex(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secretpassword123456")
	screener.AddFileBytes(fileContent)

	// 16-byte portion: "secretpassword12"
	encoded := hex.EncodeToString([]byte("secretpassword12"))
	urlStr := "https://example.com/path?data=" + encoded
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected hex-encoded match to be rejected")
	}
}

// Test nested encoding: base64-of-hex
func TestCanaryCheckBase64OfHex(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secretpassword123456")
	screener.AddFileBytes(fileContent)

	// 16-byte portion: "secretpassword12"
	hexed := hex.EncodeToString([]byte("secretpassword12"))
	encoded := base64.StdEncoding.EncodeToString([]byte(hexed))
	urlStr := "https://example.com/path?data=" + encoded
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected base64-of-hex-encoded match to be rejected")
	}
}

// Test nested encoding: hex-of-base64
func TestCanaryCheckHexOfBase64(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secretpassword123456")
	screener.AddFileBytes(fileContent)

	// 16-byte portion: "secretpassword12"
	encoded := base64.StdEncoding.EncodeToString([]byte("secretpassword12"))
	hexed := hex.EncodeToString([]byte(encoded))
	urlStr := "https://example.com/path?data=" + hexed
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected hex-of-base64-encoded match to be rejected")
	}
}

// Test nested encoding: base64-of-base64
func TestCanaryCheckBase64OfBase64(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secretpassword123456")
	screener.AddFileBytes(fileContent)

	// 16-byte portion: "secretpassword12"
	encoded1 := base64.StdEncoding.EncodeToString([]byte("secretpassword12"))
	encoded2 := base64.StdEncoding.EncodeToString([]byte(encoded1))
	urlStr := "https://example.com/path?data=" + encoded2
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected base64-of-base64-encoded match to be rejected")
	}
}

// Test third level encoding is NOT decoded (should be allowed)
func TestCanaryCheckThirdLevelNotDecoded(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secretpassword123456")
	screener.AddFileBytes(fileContent)

	// 16-byte portion: "secretpassword12"
	encoded1 := base64.StdEncoding.EncodeToString([]byte("secretpassword12"))
	encoded2 := base64.StdEncoding.EncodeToString([]byte(encoded1))
	encoded3 := base64.StdEncoding.EncodeToString([]byte(encoded2))
	urlStr := "https://example.com/path?data=" + encoded3
	result := screener.Screen(urlStr)
	if !result.Allowed {
		t.Errorf("Expected third-level-encoded match to be allowed (not decoded): %s", result.Reason)
	}
}

// Test separate corpus sequences don't create false positives
func TestCorpusSeparationNoFalsePositives(t *testing.T) {
	screener := NewURLScreener(20)

	// Add two 8-byte sequences separately
	screener.AddFileBytes([]byte("first8by"))
	screener.AddFileBytes([]byte("second8b"))

	// Their concatenation would be 16 bytes: "first8bysecond8b"
	// But this shouldn't match because they're separate
	urlStr := "https://example.com/path?q=first8bysecond8b"
	result := screener.Screen(urlStr)
	if !result.Allowed {
		t.Errorf("Expected concatenation of separate sequences to be allowed: %s", result.Reason)
	}
}

// Test route types: file, history, diff, base
func TestRouteTypes(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("filecontentsecret123456789")
	historyContent := []byte("historycontentsecret123456789")
	diffContent := []byte("diffcontentsecret123456789")
	baseContent := []byte("basecontentsecret123456789")

	screener.AddFileBytes(fileContent)
	screener.AddHistoryBytes(historyContent)
	screener.AddDiffBytes(diffContent)
	screener.AddBaseBytes(baseContent)

	// Test each route
	tests := []struct {
		name    string
		secret  string
		allowed bool
	}{
		{"file", string(fileContent[0:16]), false},
		{"history", string(historyContent[0:16]), false},
		{"diff", string(diffContent[0:16]), false},
		{"base", string(baseContent[0:16]), false},
	}

	for _, test := range tests {
		urlStr := "https://example.com/path?q=" + test.secret
		result := screener.Screen(urlStr)
		if result.Allowed != test.allowed {
			t.Errorf("Route %s: got allowed=%v, want %v (reason: %s)", test.name, result.Allowed, test.allowed, result.Reason)
		}
	}
}

// Test URL length exceeds 2048 bytes
func TestShapeCheckURLLength(t *testing.T) {
	screener := NewURLScreener(20)

	// URL longer than 2048 bytes
	padding := strings.Repeat("a", 2050)
	longPath := "https://example.com/" + padding
	result := screener.Screen(longPath)
	if result.Allowed {
		t.Errorf("Expected URL over 2048 bytes to be rejected")
	}
}

// Test hostname length exceeds 253 bytes
func TestShapeCheckHostnameLength(t *testing.T) {
	screener := NewURLScreener(20)

	// Hostname longer than 253 bytes
	padding := strings.Repeat("a", 254)
	longHostname := "https://" + padding + ".com/path"
	result := screener.Screen(longHostname)
	if result.Allowed {
		t.Errorf("Expected hostname over 253 bytes to be rejected")
	}
}

// Test label length exceeds 63 bytes
func TestShapeCheckLabelLength(t *testing.T) {
	screener := NewURLScreener(20)

	// Label longer than 63 bytes
	padding := strings.Repeat("a", 64)
	urlStr := "https://" + padding + ".com/path"
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected label over 63 bytes to be rejected")
	}
}

// Test 48-byte with high entropy allowed, 49-byte with high entropy rejected
func TestShapeCheckEntropy48vs49Bytes(t *testing.T) {
	screener := NewURLScreener(20)

	// 48-byte high-entropy string should be allowed
	highEntropyStr := "aAbBcCdDeEfFgGhHiIjJkKlMnNoOpPqQrRsStTuUvVwXxYyZz0123456789-_"
	str48 := highEntropyStr[:48]
	entropy48 := screener.shannonEntropy(str48)

	urlStr := "https://example.com/path?data=" + str48
	result := screener.Screen(urlStr)
	if !result.Allowed {
		t.Errorf("Expected 48-byte string with entropy %.2f to be allowed, got rejected: %s", entropy48, result.Reason)
	}

	// 49-byte high-entropy string should be rejected
	str49 := highEntropyStr[:49]
	entropy49 := screener.shannonEntropy(str49)

	urlStr = "https://example.com/path?data=" + str49
	result = screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected 49-byte string with entropy %.2f to be rejected, got allowed", entropy49)
	}
}

// Test 48-byte/49-byte entropy boundary in hostname label
func TestShapeCheckEntropy48vs49BytesInHostname(t *testing.T) {
	screener := NewURLScreener(20)

	// 48-byte high-entropy string in hostname should be allowed
	highEntropyStr := "aAbBcCdDeEfFgGhHiIjJkKlMnNoOpPqQrRsStTuUvVwXxYyZz0123456789-_"
	str48 := highEntropyStr[:48]
	entropy48 := screener.shannonEntropy(str48)

	urlStr := "https://" + str48 + ".example.com/path"
	result := screener.Screen(urlStr)
	if !result.Allowed {
		t.Errorf("Expected 48-byte hostname label with entropy %.2f to be allowed, got rejected: %s", entropy48, result.Reason)
	}

	// 49-byte high-entropy string in hostname should be rejected
	str49 := highEntropyStr[:49]
	entropy49 := screener.shannonEntropy(str49)

	// 49 bytes is within the 63-byte hostname label limit
	urlStr = "https://" + str49 + ".example.com/path"
	result = screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected 49-byte hostname label with entropy %.2f to be rejected, got allowed", entropy49)
	}
}

// Test standard base64 with + in raw query value (space bypass)
func TestCanaryCheckStdBase64WithPlusInQueryValue(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("\xfb\xff\xfe" + strings.Repeat("\x00", 13)) // 16-byte corpus
	screener.AddFileBytes(fileContent)

	// Standard base64 of this content
	encoded := base64.StdEncoding.EncodeToString(fileContent)
	// Construct URL with + characters that would be converted to space if not careful
	// The corpus is "\xfb\xff\xfe..." which encodes to "+//++//++//++//++//++w==" (or similar)
	// In raw query, this needs to be preserved and checked
	urlStr := "https://example.com/p?q=" + encoded
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected standard base64 match in query value to be rejected, got allowed")
	}
}

// Test standard base64 with %2F in path segment bypass
func TestCanaryCheckStdBase64With2FInPath(t *testing.T) {
	screener := NewURLScreener(20)
	// File content is the binary data that +//++//++ (base64) decodes to
	// This creates a 16-byte sequence that we want to protect
	fileContent := []byte{0xfb, 0xff, 0xfe, 0xfb, 0xff, 0xfe, 0xfb, 0xff, 0xfe, 0xfb, 0xff, 0xfe, 0xfb, 0xff, 0xfe, 0xc0}
	screener.AddFileBytes(fileContent)

	// Standard base64 of the file content: "+//++//++//++//++//+wA=="
	// In a URL path, the / must be percent-encoded as %2F to avoid being treated as a separator
	encoded := base64.StdEncoding.EncodeToString(fileContent)
	// Replace / with %2F for proper URL encoding
	encodedPath := strings.ReplaceAll(encoded, "/", "%2F")

	urlStr := "https://example.com/" + encodedPath
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected base64-encoded corpus bytes in path (with %%2F) to be rejected, got allowed")
	}
}

// Test refused fetches don't corrupt count state
func TestRefusedFetchCountConsistency(t *testing.T) {
	screener := NewURLScreener(1) // maxPerTurn = 1

	// First fetch should be allowed
	result := screener.Screen("https://a.example.com/path")
	if !result.Allowed {
		t.Errorf("First fetch should be allowed")
	}

	// 10 fetches to b.example.com - all should be refused by turn cap
	for i := 0; i < 10; i++ {
		result = screener.Screen("https://b.example.com/path")
		if result.Allowed {
			t.Errorf("Fetch %d to b.example.com should be refused (turn cap)", i+1)
		}
	}

	// With our fix, only allowed fetches increment counters
	// So hostFetches["b.example.com"] should be 0 and totalFetches should be 1
	if screener.hostFetches["b.example.com"] != 0 {
		t.Errorf("Expected hostFetches[b.example.com] = 0 (refused fetch), got %d", screener.hostFetches["b.example.com"])
	}
	if screener.totalFetches != 1 {
		t.Errorf("Expected totalFetches = 1, got %d", screener.totalFetches)
	}
}

// Test count state only increments on allowed fetches
func TestCountOnlyIncrementedForAllowedFetches(t *testing.T) {
	screener := NewURLScreener(5)

	// Allow 5 fetches to different hosts
	for i := 0; i < 5; i++ {
		host := "https://host" + string(rune(97+i)) + ".com/path"
		result := screener.Screen(host)
		if !result.Allowed {
			t.Errorf("Fetch %d should be allowed", i+1)
		}
	}

	// Try 10 refused fetches to a single new host - should all be refused but NOT increment total count
	for i := 0; i < 10; i++ {
		result := screener.Screen("https://hostf.com/path")
		if result.Allowed {
			t.Errorf("Fetch to hostf should be refused (at turn limit)")
		}
	}

	// totalFetches should still be 5 since all the hostf fetches were refused
	if screener.totalFetches != 5 {
		t.Errorf("Expected totalFetches = 5 (refused fetches don't count), got %d", screener.totalFetches)
	}

	// hostFetches["hostf.com"] should be 0 since all were refused
	if screener.hostFetches["hostf.com"] != 0 {
		t.Errorf("Expected hostFetches[hostf.com] = 0 (all refused), got %d", screener.hostFetches["hostf.com"])
	}
}

// Test per-host fetch count with canonical hostname (case-insensitive, default port stripped)
func TestHostFetchCountCanonical(t *testing.T) {
	screener := NewURLScreener(20)

	// 10 fetches to example.com
	for i := 0; i < 10; i++ {
		result := screener.Screen("https://example.com/path")
		if !result.Allowed {
			t.Errorf("Fetch %d to example.com should be allowed", i+1)
		}
	}

	// 11th fetch with different case should still be rejected (canonical host matching)
	result := screener.Screen("https://EXAMPLE.COM/path")
	if result.Allowed {
		t.Errorf("Expected 11th fetch to EXAMPLE.COM (same canonical host) to be rejected")
	}

	// Test with explicit default port
	screener2 := NewURLScreener(20)
	for i := 0; i < 10; i++ {
		result := screener2.Screen("https://example.com:443/path")
		if !result.Allowed {
			t.Errorf("Fetch %d to example.com:443 should be allowed", i+1)
		}
	}

	// 11th fetch without port should still be rejected (same canonical host)
	result = screener2.Screen("https://example.com/path")
	if result.Allowed {
		t.Errorf("Expected 11th fetch to example.com (same as example.com:443) to be rejected")
	}
}

// Test maxPerTurn is used (not hard-coded 20)
func TestMaxPerTurnConfigurable(t *testing.T) {
	screener := NewURLScreener(5)

	// 5 fetches to different hosts should be allowed
	for i := 0; i < 5; i++ {
		host := "https://host" + string(rune(97+i)) + ".com/path"
		result := screener.Screen(host)
		if !result.Allowed {
			t.Errorf("Fetch %d should be allowed with maxPerTurn=5", i+1)
		}
	}

	// 6th fetch should be rejected
	result := screener.Screen("https://hostf.com/path")
	if result.Allowed {
		t.Errorf("Expected 6th fetch to be rejected with maxPerTurn=5")
	}
}

// Test matching at non-zero offset in decoded value
func TestCanaryCheckOffsetInDecoded(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secrettoken123456789")
	screener.AddFileBytes(fileContent)

	// 16 bytes of file content
	secret := string(fileContent[0:16]) // "secrettoken12345"

	// Encode with prefix
	encoded := base64.StdEncoding.EncodeToString([]byte("prefixXX" + secret))
	urlStr := "https://example.com/path?q=" + encoded
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected match at non-zero offset to be rejected")
	}
}

// Test 15-byte allowed in hostname and path
func TestCanaryCheck15BytesInHostnameAndPath(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("verylongtoken123") // 16 bytes
	screener.AddFileBytes(fileContent)

	// Extract a 15-byte sequence
	secret := string(fileContent[0:15]) // "verylongtoken12"

	// In hostname
	urlStr := "https://" + secret + ".example.com/path"
	result := screener.Screen(urlStr)
	if !result.Allowed {
		t.Errorf("Expected 15-byte match in hostname to be allowed")
	}

	// In path
	urlStr = "https://example.com/" + secret + "/other"
	result = screener.Screen(urlStr)
	if !result.Allowed {
		t.Errorf("Expected 15-byte match in path to be allowed")
	}
}

// Test scheme validation
func TestSchemeValidation(t *testing.T) {
	screener := NewURLScreener(20)

	tests := []struct {
		url     string
		allowed bool
	}{
		{"https://example.com/path", true},
		{"http://example.com/path", true},
		{"ftp://example.com/path", false},
		{"file:///path/to/file", false},
		{"gopher://example.com/path", false},
	}

	for _, test := range tests {
		result := screener.Screen(test.url)
		if result.Allowed != test.allowed {
			t.Errorf("URL %s: got allowed=%v, want %v", test.url, result.Allowed, test.allowed)
		}
	}
}

// Test hex in path segment
func TestCanaryCheckHexInPath(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secretpassword123456")
	screener.AddFileBytes(fileContent)

	// 16-byte portion hex-encoded
	hexed := hex.EncodeToString([]byte("secretpassword12"))
	urlStr := "https://example.com/" + hexed + "/other"
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected hex-encoded match in path to be rejected")
	}
}

// Test hex in hostname label
func TestCanaryCheckHexInHostname(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secretpassword123456")
	screener.AddFileBytes(fileContent)

	// 16-byte portion hex-encoded
	hexed := hex.EncodeToString([]byte("secretpassword12"))
	urlStr := "https://" + hexed + ".example.com/path"
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected hex-encoded match in hostname to be rejected")
	}
}

// Test base64url in path segment
func TestCanaryCheckBase64URLInPath(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secretpassword123456")
	screener.AddFileBytes(fileContent)

	// 16-byte portion base64url-encoded
	encoded := base64.URLEncoding.EncodeToString([]byte("secretpassword12"))
	urlStr := "https://example.com/" + encoded + "/other"
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected base64url-encoded match in path to be rejected")
	}
}

// Test base64url in hostname label
func TestCanaryCheckBase64URLInHostname(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secretpassword123456")
	screener.AddFileBytes(fileContent)

	// 16-byte portion base64url-encoded
	encoded := base64.URLEncoding.EncodeToString([]byte("secretpassword12"))
	urlStr := "https://" + encoded + ".example.com/path"
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected base64url-encoded match in hostname to be rejected")
	}
}

// Test count consistency: refused fetches don't use budget
func TestRefusedHostCapFetchDoesntUseTotal(t *testing.T) {
	screener := NewURLScreener(5)

	// Make 5 fetches to a.com (at turn limit)
	for i := 0; i < 5; i++ {
		result := screener.Screen("https://a.example.com/path" + string(rune('0'+i)))
		if !result.Allowed {
			t.Errorf("Fetch %d to a.example.com should be allowed (within turn limit)", i+1)
		}
	}

	// 6th fetch to a.com should be refused by turn cap
	result := screener.Screen("https://a.example.com/path5")
	if result.Allowed {
		t.Errorf("6th fetch should be refused (at turn limit 5)")
	}

	// Try a fetch to a new host - should also be refused since we're at turn limit
	result = screener.Screen("https://b.example.com/path")
	if result.Allowed {
		t.Errorf("Fetch to new host should be refused (at turn limit 5)")
	}
}

// Test valid URL passes
func TestValidURL(t *testing.T) {
	screener := NewURLScreener(20)

	result := screener.Screen("https://example.com/path?key=value")
	if !result.Allowed {
		t.Errorf("Valid URL should be allowed, got rejected: %s", result.Reason)
	}
}

// Test invalid percent escapes are handled leniently and decoded bytes are still checked
func TestCanaryCheckInvalidPercentEscapeLenient(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("sk-live-ABCDEFGHIJKLMNOP")
	screener.AddFileBytes(fileContent)

	// URL with invalid escape %zz followed by valid escapes should still decode the valid parts
	// This is lenient decoding: %zz stays as literal "%zz", but the following valid escapes decode
	encoded := "%73%6B%2D%6C%69%76%65%2D%41%42%43%44%45%46%47%48" // sk-live-ABCDEFGH in percent encoding
	urlStr := "https://example.com/p?q=%zz" + encoded
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected URL with invalid escape followed by secret in valid escapes to be rejected")
	}
}

// Test trailing incomplete percent escape is handled leniently
func TestCanaryCheckTrailingPercentEscape(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("sk-live-ABCDEFGHIJKLMNOP")
	screener.AddFileBytes(fileContent)

	// URL with trailing incomplete percent escape
	encoded := "%73%6B%2D%6C%69%76%65%2D%41%42%43%44%45%46%47%48" // sk-live-ABCDEFGH in percent encoding
	urlStr := "https://example.com/p?q=" + encoded + "%"
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected URL with secret in valid escapes (even with trailing %%) to be rejected")
	}
}

// Test literal plus in URL is preserved and checked in whole URL
func TestCanaryCheckLiteralPlusInURL(t *testing.T) {
	screener := NewURLScreener(20)
	// 16-byte corpus that spans path and query
	fileContent := []byte("ab+cdef/ghijklmn")
	screener.AddFileBytes(fileContent)

	// URL with literal + in path and literal / in query (not encoded)
	// When percent-decoded, the literal + stays as + (not converted to space like QueryUnescape does)
	urlStr := "https://example.com/ab+cdef/ghijklmn"
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected URL with 16-byte run including literal + to be rejected")
	}
}

// Test literal plus in query key
func TestCanaryCheckLiteralPlusInQueryKey(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("abcd+efghijklmno")
	screener.AddFileBytes(fileContent)

	// URL with literal + in query key
	urlStr := "https://example.com/p?abcd+efghijklmno=1"
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected URL with 16-byte run including literal + in query key to be rejected")
	}
}
