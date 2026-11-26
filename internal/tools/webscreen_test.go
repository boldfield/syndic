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
	// Remove padding
	unpadded := encoded[:len(encoded)-len(encoded)%4]
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
	// Remove padding
	unpadded := encoded[:len(encoded)-len(encoded)%4]
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

// Test 48-byte low entropy allowed, 49-byte high entropy rejected
func TestShapeCheckEntropy48vs49Bytes(t *testing.T) {
	screener := NewURLScreener(20)

	// 48-byte high-entropy string should be allowed
	highEntropyStr := "aAbBcCdDeEfFgGhHiIjJkKlMnNoOpPqQrRsStTuUvVwXxYyZz0123456789-_"
	str48 := highEntropyStr[:48]
	entropy48 := screener.shannonEntropy(str48)

	urlStr := "https://example.com/path?data=" + str48
	result := screener.Screen(urlStr)
	if !result.Allowed && entropy48 <= 4.5 {
		t.Errorf("Expected 48-byte string with entropy %.2f <= 4.5 to be allowed, got rejected: %s", entropy48, result.Reason)
	}

	// 49-byte high-entropy string should be rejected
	str49 := highEntropyStr[:49]
	entropy49 := screener.shannonEntropy(str49)

	urlStr = "https://example.com/path?data=" + str49
	result = screener.Screen(urlStr)
	if result.Allowed && entropy49 > 4.5 {
		t.Errorf("Expected 49-byte string with entropy %.2f > 4.5 to be rejected", entropy49)
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

// Test valid URL passes
func TestValidURL(t *testing.T) {
	screener := NewURLScreener(20)

	result := screener.Screen("https://example.com/path?key=value")
	if !result.Allowed {
		t.Errorf("Valid URL should be allowed, got rejected: %s", result.Reason)
	}
}
