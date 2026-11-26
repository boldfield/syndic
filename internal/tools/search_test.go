package tools

import (
	"encoding/json"
	"regexp"
	"testing"
)

func TestSearchMatchMarshal(t *testing.T) {
	match := SearchMatch{
		File: "main.go",
		Line: 42,
		Text: "fmt.Println(\"hello\")",
	}

	data, err := json.Marshal(match)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed SearchMatch
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed != match {
		t.Errorf("mismatch: got %+v, want %+v", parsed, match)
	}
}

func TestSearchResultMarshal(t *testing.T) {
	result := SearchResult{
		Matches: []SearchMatch{
			{File: "main.go", Line: 1, Text: "package main"},
			{File: "main.go", Line: 3, Text: "func main() {"},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed SearchResult
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(parsed.Matches) != len(result.Matches) {
		t.Errorf("matches count: got %d, want %d", len(parsed.Matches), len(result.Matches))
	}

	for i, match := range parsed.Matches {
		if match != result.Matches[i] {
			t.Errorf("match %d: got %+v, want %+v", i, match, result.Matches[i])
		}
	}
}

func TestSearchPatternValidation(t *testing.T) {
	tests := []struct {
		pattern string
		valid   bool
	}{
		{`^func.*\(`, true},
		{`\d+`, true},
		{`[a-z]+`, true},
		{`(?P<invalid`, false},
		{`[`, false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			_, err := regexp.Compile(tt.pattern)
			if (err == nil) != tt.valid {
				t.Errorf("valid mismatch: got error %v, want valid=%v", err, tt.valid)
			}
		})
	}
}

func TestSearchFilesBasic(t *testing.T) {
	t.Skip("full test requires database setup")
}

func TestSearchFilesWithPath(t *testing.T) {
	t.Skip("full test requires database setup")
}

func TestSearchFilesRecursive(t *testing.T) {
	t.Skip("full test requires database setup")
}

func TestSearchFilesNoMatches(t *testing.T) {
	t.Skip("full test requires database setup")
}

func TestSearchFilesStagedContent(t *testing.T) {
	t.Skip("full test requires database setup")
}

func TestSearchFilesInvalidPath(t *testing.T) {
	t.Skip("full test requires database setup")
}
