package tui_test

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestV6FramesAndStyle(t *testing.T) {
	expectedHashes := map[string]string{
		"changes-apply-100x30.txt": "936ac66274edff5557727cc4198a1cd4461c77fe286b2edffd3b65856420d7d3",
		"chat-attach-100x30.txt":   "f534cf799dbef7edd2596178d3bb83554fec6e80ec0af763092b1d4f8e8b98b6",
		"chat-drawer-80x24.txt":    "d13afe9ee8bc4351d27f30c110a32b70a7230957d2665e5958236a052d69a83a",
		"chat-dumb-80x24.txt":      "906769741fea8038f4a73a5c67b702aadafb196ca38334dbcc86ff8f1908e533",
		"chat-idle-100x30.txt":     "16024e9c5e51f3d2d8a245fa01a6a388a8efc0f08d2891250a28971c8fc5f261",
		"chat-outcomes-100x30.txt": "4bf82e8f6b5e8ee3ea6eb1d6320f4e3e2eb5c67020fc8af613e1a4c32301c24f",
		"participants-100x30.txt":  "832ef3c4386b87a50d35d34fc088e6f65dd5bbf67835629d5154cae0cf9e2cce",
		"transcript-100x30.txt":    "41110be9edc9ad228521fcd18c0656a9e1fe355da2c28b0cbc4a9f1e08288269",
		"style.md":                 "88120450cbc1dc1a7dfda20451b033381d1eee844b19d01883c054e2ccf70faf",
	}

	testdataDir := filepath.Join("testdata", "v6")

	for filename, expectedHash := range expectedHashes {
		t.Run(filename, func(t *testing.T) {
			f, err := os.Open(filepath.Join(testdataDir, filename))
			if err != nil {
				t.Fatalf("failed to open file: %v", err)
			}
			defer func() { _ = f.Close() }()

			h := sha256.New()
			if _, err := io.Copy(h, f); err != nil {
				t.Fatalf("failed to compute hash: %v", err)
			}

			actualHash := fmt.Sprintf("%x", h.Sum(nil))
			if actualHash != expectedHash {
				t.Errorf("SHA-256 mismatch for %s: got %s, want %s", filename, actualHash, expectedHash)
			}
		})
	}
}
