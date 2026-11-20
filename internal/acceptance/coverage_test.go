package acceptance

import (
	"os"
	"strings"
	"sync"
	"testing"
)

var (
	coverageMu sync.Mutex
	coverage   = map[[2]string]string{} // {paragraph, item} -> test name
)

// cover records that the named test covers one item of a section 8 paragraph.
// item is the design's clause, quoted or closely paraphrased.
// test is a function name in this package or <import_suffix>.<TestName>.
func cover(paragraph, item, test string) {
	coverageMu.Lock()
	defer coverageMu.Unlock()
	key := [2]string{paragraph, item}
	if existing, ok := coverage[key]; ok && existing != test {
		panic("duplicate coverage: " + paragraph + ": " + item + " covered by both " + existing + " and " + test)
	}
	coverage[key] = test
}

// TestCoverage_NamedTestsExist verifies that every registered test exists.
func TestCoverage_NamedTestsExist(t *testing.T) {
	coverageMu.Lock()
	registered := make(map[string]bool)
	for _, testName := range coverage {
		registered[testName] = true
	}
	coverageMu.Unlock()

	// Read the test files and check for Test functions
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	seenTests := make(map[string]bool)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		content := string(data)
		// Simple regex-free search for Test functions
		for _, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "func Test") {
				parts := strings.FieldsFunc(line, func(r rune) bool { return r == '(' })
				if len(parts) > 0 {
					fn := strings.TrimPrefix(strings.TrimSpace(parts[0]), "func ")
					seenTests[fn] = true
				}
			}
		}
	}

	// Check that every registered test exists
	for testName := range registered {
		if !seenTests[testName] {
			t.Errorf("registered test %s does not exist", testName)
		}
	}
}

// TestCoverage_Gaps logs every test that calls gap.
func TestCoverage_Gaps(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	gapCount := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		content := string(data)
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			if strings.Contains(line, "gap(t,") {
				// Extract the message
				start := strings.Index(line, `"`)
				end := strings.LastIndex(line, `"`)
				if start >= 0 && end > start {
					msg := line[start+1 : end]
					t.Logf("line %d: %s", i+1, msg)
					gapCount++
				}
			}
		}
	}
	if gapCount == 0 {
		t.Log("no gaps")
	}
}
