package tools

import (
	"testing"
)

func TestCanaryCheck16Bytes(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("this is a test file content that we want to protect")
	screener.AddFileBytes(fileContent)

	// Extract a 16-byte sequence from file content
	// "a test file con"

	// URL containing this exact sequence should be rejected
	urlStr := "https://example.com/path?query=" + string(fileContent[10:26])
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected URL with 16-byte match to be rejected, got allowed")
	}
	if result.Reason != "canary: match in query" {
		t.Errorf("Expected canary rejection reason, got: %s", result.Reason)
	}
}

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

func TestCanaryCheckBase64Encoding(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secretpassword123456")
	screener.AddFileBytes(fileContent)

	// 16-byte portion of file content: "secretpassword12"
	// Base64 encode it
	encoded := "c2VjcmV0cGFzc3dvcmQxMg==" // base64 of "secretpassword12"

	// URL with base64-encoded secret should be rejected
	urlStr := "https://example.com/path?data=" + encoded
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected base64-encoded canary match to be rejected, got allowed")
	}
}

func TestShapeCheckURLLength(t *testing.T) {
	screener := NewURLScreener(20)

	// URL longer than 2048 bytes
	padding := ""
	for i := 0; i < 2050; i++ {
		padding += "a"
	}
	longPath := "https://example.com/" + padding
	result := screener.Screen(longPath)
	if result.Allowed {
		t.Errorf("Expected URL over 2048 bytes to be rejected")
	}
	if result.Reason != "url_length: exceeds 2048 bytes" {
		t.Errorf("Expected URL length rejection, got: %s", result.Reason)
	}
}

func TestShapeCheckHostnameLength(t *testing.T) {
	screener := NewURLScreener(20)

	// Hostname longer than 253 bytes
	padding := ""
	for i := 0; i < 254; i++ {
		padding += "a"
	}
	longHostname := "https://" + padding + ".com/path"
	result := screener.Screen(longHostname)
	if result.Allowed {
		t.Errorf("Expected hostname over 253 bytes to be rejected")
	}
	if result.Reason != "hostname_length: exceeds 253 bytes" {
		t.Errorf("Expected hostname length rejection, got: %s", result.Reason)
	}
}

func TestShapeCheckLabelLength(t *testing.T) {
	screener := NewURLScreener(20)

	// Label longer than 63 bytes
	padding := ""
	for i := 0; i < 64; i++ {
		padding += "a"
	}
	urlStr := "https://" + padding + ".com/path"
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected label over 63 bytes to be rejected")
	}
	if result.Reason != "label_length: exceeds 63 bytes" {
		t.Errorf("Expected label length rejection, got: %s", result.Reason)
	}
}

func TestShapeCheckEntropy48Bytes(t *testing.T) {
	screener := NewURLScreener(20)

	// 49-byte high-entropy string should be rejected
	highEntropyStr := "aAbBcCdDeEfFgGhHiIjJkKlLmMnNoOpPqQrRsStTuUvVwXxYyZz0123456789"
	urlStr := "https://example.com/path?data=" + highEntropyStr[:49]
	result := screener.Screen(urlStr)

	entropy := screener.shannonEntropy(highEntropyStr[:49])
	if entropy > 4.5 {
		if result.Allowed {
			t.Errorf("Expected 49-byte high-entropy string (entropy %.2f) to be rejected", entropy)
		}
	}
}

func TestShapeCheckEntropy48BytesLow(t *testing.T) {
	screener := NewURLScreener(20)

	// 48-byte string with low entropy should be allowed
	lowEntropyStr := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	urlStr := "https://example.com/path?data=" + lowEntropyStr
	result := screener.Screen(urlStr)
	if !result.Allowed {
		t.Errorf("Expected 48-byte low-entropy string to be allowed, got rejected: %s", result.Reason)
	}
}

func TestHostFetchCount(t *testing.T) {
	screener := NewURLScreener(20)

	// 10 fetches to same host should be allowed
	for i := 0; i < 10; i++ {
		result := screener.Screen("https://example.com/path")
		if !result.Allowed {
			t.Errorf("Fetch %d to same host should be allowed, got rejected: %s", i+1, result.Reason)
		}
	}

	// 11th fetch should be rejected
	result := screener.Screen("https://example.com/path")
	if result.Allowed {
		t.Errorf("Expected 11th fetch to same host to be rejected")
	}
	if result.Reason != "host_count: exceeds 10 fetches to example.com" {
		t.Errorf("Expected host count rejection, got: %s", result.Reason)
	}
}

func TestTotalFetchCount(t *testing.T) {
	screener := NewURLScreener(20)

	// 20 fetches total should be allowed
	for i := 0; i < 20; i++ {
		path := "https://host" + string(rune(97+i%26)) + ".com/path"
		result := screener.Screen(path)
		if !result.Allowed {
			t.Errorf("Fetch %d should be allowed, got rejected: %s", i+1, result.Reason)
		}
	}

	// 21st fetch should be rejected
	result := screener.Screen("https://hosta.com/path21")
	if result.Allowed {
		t.Errorf("Expected 21st fetch to be rejected")
	}
	if result.Reason != "turn_count: exceeds 20 total fetches in turn" {
		t.Errorf("Expected turn count rejection, got: %s", result.Reason)
	}
}

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
			t.Errorf("URL %s: got allowed=%v, want %v (reason: %s)", test.url, result.Allowed, test.allowed, result.Reason)
		}
	}
}

func TestCanaryCheckHostname(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secrettoken123456789")
	screener.AddFileBytes(fileContent)

	// 16 bytes of file content
	secret := string(fileContent[0:16]) // "secrettoken12345"

	// URL with secret in hostname should be rejected
	urlStr := "https://" + secret + ".example.com/path"
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected URL with 16-byte match in hostname to be rejected")
	}
	if result.Reason != "canary: match in hostname" {
		t.Errorf("Expected canary rejection in hostname, got: %s", result.Reason)
	}
}

func TestCanaryCheckPath(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secrettoken123456789")
	screener.AddFileBytes(fileContent)

	// 16 bytes of file content
	secret := string(fileContent[0:16]) // "secrettoken12345"

	// URL with secret in path should be rejected
	urlStr := "https://example.com/" + secret + "/other"
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected URL with 16-byte match in path to be rejected")
	}
	if result.Reason != "canary: match in path" {
		t.Errorf("Expected canary rejection in path, got: %s", result.Reason)
	}
}

func TestShannonEntropy(t *testing.T) {
	screener := NewURLScreener(20)

	tests := []struct {
		str      string
		maxLower bool // true if entropy should be <= 4.5
	}{
		{"aaaaaaaaaaaaa", true}, // very low entropy (0)
		{"aAbBcCdDeEfFgGhHiIjJkKlMnNoOpPqQrSsTtUuVvWwXxYyZz", false}, // 52 unique chars
		{"111111111111111111111111111111111111", true},               // all same char (0)
		{"abcdefghijklmnopqrstuvwxyz", false},                        // 26 unique chars high entropy
	}

	for _, test := range tests {
		entropy := screener.shannonEntropy(test.str)
		isLow := entropy <= 4.5
		if isLow != test.maxLower {
			t.Errorf("String %q: entropy %.2f, expected low=%v", test.str, entropy, test.maxLower)
		}
	}
}

func TestValidURL(t *testing.T) {
	screener := NewURLScreener(20)

	// A completely valid URL should pass
	result := screener.Screen("https://example.com/path?key=value")
	if !result.Allowed {
		t.Errorf("Valid URL should be allowed, got rejected: %s", result.Reason)
	}
}

func TestMultiplePathSegments(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secrettoken123456789")
	screener.AddFileBytes(fileContent)

	secret := string(fileContent[0:16])

	// URL with secret in one of multiple path segments should be rejected
	urlStr := "https://example.com/api/v1/" + secret + "/resource"
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected path segment with canary match to be rejected")
	}
}

func TestMultipleQueryValues(t *testing.T) {
	screener := NewURLScreener(20)
	fileContent := []byte("secrettoken123456789")
	screener.AddFileBytes(fileContent)

	secret := string(fileContent[0:16])

	// URL with secret in one of multiple query values should be rejected
	urlStr := "https://example.com/path?key1=value1&key2=" + secret
	result := screener.Screen(urlStr)
	if result.Allowed {
		t.Errorf("Expected query value with canary match to be rejected")
	}
}

func TestFetchCountPerHost(t *testing.T) {
	screener := NewURLScreener(20)

	// 10 fetches to host1
	for i := 0; i < 10; i++ {
		result := screener.Screen("https://host1.com/path")
		if !result.Allowed {
			t.Errorf("Fetch %d to host1 should be allowed", i+1)
		}
	}

	// 10 fetches to host2 should be allowed (different host)
	for i := 0; i < 10; i++ {
		result := screener.Screen("https://host2.com/path")
		if !result.Allowed {
			t.Errorf("Fetch %d to host2 should be allowed", i+1)
		}
	}

	// 11th fetch to host1 should be rejected (exceeds per-host limit of 10)
	result := screener.Screen("https://host1.com/path")
	if result.Allowed {
		t.Errorf("11th fetch to host1 should be rejected")
	}

	// 11th fetch to host2 should also be rejected
	result = screener.Screen("https://host2.com/path")
	if result.Allowed {
		t.Errorf("11th fetch to host2 should be rejected")
	}
}
