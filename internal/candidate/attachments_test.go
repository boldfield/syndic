package candidate

import (
	"strings"
	"testing"
)

func TestAttachmentManifestEmpty(t *testing.T) {
	manifest, err := AttachmentManifest(nil)
	if err != nil {
		t.Fatalf("AttachmentManifest(nil) failed: %v", err)
	}
	if manifest != nil {
		t.Errorf("Expected nil manifest for empty attachments, got %v", manifest)
	}

	manifest, err = AttachmentManifest([]Attachment{})
	if err != nil {
		t.Fatalf("AttachmentManifest([]) failed: %v", err)
	}
	if manifest != nil {
		t.Errorf("Expected nil manifest for empty attachments, got %v", manifest)
	}
}

func TestAttachmentManifestHash(t *testing.T) {
	hash, err := AttachmentManifestHash(nil)
	if err != nil {
		t.Fatalf("AttachmentManifestHash(nil) failed: %v", err)
	}
	if hash != "none" {
		t.Errorf("Expected 'none' for empty attachments, got %q", hash)
	}

	hash, err = AttachmentManifestHash([]Attachment{})
	if err != nil {
		t.Fatalf("AttachmentManifestHash([]) failed: %v", err)
	}
	if hash != "none" {
		t.Errorf("Expected 'none' for empty attachments, got %q", hash)
	}
}

func TestAttachmentManifestSingleAttachment(t *testing.T) {
	attachments := []Attachment{
		{
			Path:          "file.txt",
			ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ContentLen:    5,
		},
	}

	manifest, err := AttachmentManifest(attachments)
	if err != nil {
		t.Fatalf("AttachmentManifest failed: %v", err)
	}

	expected := "syndic-attachments/v1\n8:file.txt\naaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n5\n"
	if string(manifest) != expected {
		t.Errorf("Manifest mismatch.\nExpected:\n%q\nGot:\n%q", expected, string(manifest))
	}
}

func TestAttachmentManifestByteOrder(t *testing.T) {
	// Provide in reverse order: f, b, a
	attachments := []Attachment{
		{
			Path:          "f.txt",
			ContentSHA256: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			ContentLen:    1,
		},
		{
			Path:          "b.txt",
			ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			ContentLen:    1,
		},
		{
			Path:          "a.txt",
			ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ContentLen:    1,
		},
	}

	manifest, err := AttachmentManifest(attachments)
	if err != nil {
		t.Fatalf("AttachmentManifest failed: %v", err)
	}

	// Should be sorted: a, b, f
	expected := "syndic-attachments/v1\n5:a.txt\naaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n1\n5:b.txt\nbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n1\n5:f.txt\nffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\n1\n"
	if string(manifest) != expected {
		t.Errorf("Manifest mismatch.\nExpected:\n%q\nGot:\n%q", expected, string(manifest))
	}
}

func TestAttachmentManifestHashConsistency(t *testing.T) {
	attachments := []Attachment{
		{
			Path:          "file.txt",
			ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ContentLen:    5,
		},
	}

	hash1, err := AttachmentManifestHash(attachments)
	if err != nil {
		t.Fatalf("First AttachmentManifestHash failed: %v", err)
	}

	hash2, err := AttachmentManifestHash(attachments)
	if err != nil {
		t.Fatalf("Second AttachmentManifestHash failed: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("Hash not consistent: %s vs %s", hash1, hash2)
	}

	// Golden attachment manifest hash value
	expectedHash := "90eb16b034c08328e6a38ae0434217ac9a1748cb6117d2415501f1980d704387"
	if hash1 != expectedHash {
		t.Errorf("Attachment manifest hash mismatch.\nExpected: %s\nGot: %s", expectedHash, hash1)
	}
}

func TestAttachmentManifestReversedOrderSameHash(t *testing.T) {
	attachments1 := []Attachment{
		{
			Path:          "a.txt",
			ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ContentLen:    1,
		},
		{
			Path:          "b.txt",
			ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			ContentLen:    1,
		},
	}

	attachments2 := []Attachment{
		{
			Path:          "b.txt",
			ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			ContentLen:    1,
		},
		{
			Path:          "a.txt",
			ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ContentLen:    1,
		},
	}

	hash1, err := AttachmentManifestHash(attachments1)
	if err != nil {
		t.Fatalf("AttachmentManifestHash failed: %v", err)
	}

	hash2, err := AttachmentManifestHash(attachments2)
	if err != nil {
		t.Fatalf("AttachmentManifestHash failed: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("Reversed order should produce same hash: %s vs %s", hash1, hash2)
	}
}

func TestAttachmentManifestPathContentMutation(t *testing.T) {
	attachments1 := []Attachment{
		{
			Path:          "file.txt",
			ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ContentLen:    5,
		},
	}

	attachments2 := []Attachment{
		{
			Path:          "file2.txt",
			ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ContentLen:    5,
		},
	}

	hash1, err := AttachmentManifestHash(attachments1)
	if err != nil {
		t.Fatalf("AttachmentManifestHash failed: %v", err)
	}

	hash2, err := AttachmentManifestHash(attachments2)
	if err != nil {
		t.Fatalf("AttachmentManifestHash failed: %v", err)
	}

	if hash1 == hash2 {
		t.Error("Changing path should change hash")
	}

	attachments3 := []Attachment{
		{
			Path:          "file.txt",
			ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			ContentLen:    5,
		},
	}

	hash3, err := AttachmentManifestHash(attachments3)
	if err != nil {
		t.Fatalf("AttachmentManifestHash failed: %v", err)
	}

	if hash1 == hash3 {
		t.Error("Changing content hash should change manifest hash")
	}
}

func TestAttachmentManifestWithPathsOfDifferentLengths(t *testing.T) {
	attachments := []Attachment{
		{
			Path:          "short",
			ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ContentLen:    1,
		},
		{
			Path:          "much_longer_path_name",
			ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			ContentLen:    1,
		},
	}

	manifest, err := AttachmentManifest(attachments)
	if err != nil {
		t.Fatalf("AttachmentManifest failed: %v", err)
	}

	// Sorted by byte order: "much" comes before "short" ('m' < 's')
	expected := "syndic-attachments/v1\n21:much_longer_path_name\nbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n1\n5:short\naaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n1\n"
	if string(manifest) != expected {
		t.Errorf("Manifest mismatch.\nExpected:\n%q\nGot:\n%q", expected, string(manifest))
	}
}

func TestAttachmentValidationEmptyPath(t *testing.T) {
	att := Attachment{
		Path:          "",
		ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ContentLen:    1,
	}

	_, err := AttachmentManifest([]Attachment{att})
	if err == nil {
		t.Error("Should error when path is empty")
	}
}

func TestAttachmentValidationPathWithNewline(t *testing.T) {
	att := Attachment{
		Path:          "file\n.txt",
		ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ContentLen:    1,
	}

	_, err := AttachmentManifest([]Attachment{att})
	if err == nil {
		t.Error("Should error when path contains newline")
	}
}

func TestAttachmentValidationInvalidSHA256(t *testing.T) {
	att := Attachment{
		Path:          "file.txt",
		ContentSHA256: "not-hex",
		ContentLen:    1,
	}

	_, err := AttachmentManifest([]Attachment{att})
	if err == nil {
		t.Error("Should error when sha256 is not valid hex")
	}
}

func TestAttachmentValidationNegativeContentLen(t *testing.T) {
	att := Attachment{
		Path:          "file.txt",
		ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ContentLen:    -1,
	}

	_, err := AttachmentManifest([]Attachment{att})
	if err == nil {
		t.Error("Should error when content_len is negative")
	}
}

func TestAttachmentManifestMultipleAttachments(t *testing.T) {
	attachments := []Attachment{
		{
			Path:          "README.md",
			ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ContentLen:    100,
		},
		{
			Path:          "src/main.go",
			ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			ContentLen:    500,
		},
		{
			Path:          "data.json",
			ContentSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			ContentLen:    1000,
		},
	}

	manifest, err := AttachmentManifest(attachments)
	if err != nil {
		t.Fatalf("AttachmentManifest failed: %v", err)
	}

	// Verify it starts with the magic line
	if !strings.HasPrefix(string(manifest), "syndic-attachments/v1\n") {
		t.Error("Manifest should start with syndic-attachments/v1")
	}

	// Verify paths are sorted (README, data, src)
	manifestStr := string(manifest)
	readmeIdx := strings.Index(manifestStr, "README.md")
	dataIdx := strings.Index(manifestStr, "data.json")
	srcIdx := strings.Index(manifestStr, "src/main.go")

	if readmeIdx > dataIdx || dataIdx > srcIdx {
		t.Error("Paths should be sorted in ascending byte order")
	}
}
