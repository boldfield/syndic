package tui

import (
	"context"
	"strings"

	"github.com/boldfield/syndic/internal/client"
	tea "github.com/charmbracelet/bubbletea"
)

// ContentCache holds content bodies by ref. A ref that failed is not a
// success: Need reports it again so the fetch is retried.
type ContentCache struct {
	data    map[string]ContentItem
	pending map[string]bool
}

// SafeData returns the content as safe terminal text: control characters and
// invalid UTF-8 are escaped. Ready and Err have the same meaning as Get.
func (cc *ContentCache) SafeData(ref string) (data string, ready bool, err error) {
	item := cc.Get(ref)
	if !item.Ready {
		return "", false, nil
	}
	if item.Err != nil {
		return "", true, item.Err
	}
	return ToSafeText([]byte(item.Data)), true, nil
}

// ContentItem holds a fetched content body.
type ContentItem struct {
	Data  string // UTF-8 text
	Err   error  // non-nil if the fetch failed
	Ready bool   // true once a fetch completed, successfully or not
}

// NewContentCache returns an empty cache.
func NewContentCache() *ContentCache {
	return &ContentCache{data: map[string]ContentItem{}, pending: map[string]bool{}}
}

// Get returns the cached item, or one with Ready=false.
func (cc *ContentCache) Get(ref string) ContentItem { return cc.data[ref] }

// Store records a completed fetch.
func (cc *ContentCache) Store(ref, data string, err error) {
	delete(cc.pending, ref)
	cc.data[ref] = ContentItem{Data: data, Err: err, Ready: true}
}

// Need reports whether ref should be fetched now, and marks it in flight if so.
func (cc *ContentCache) Need(ref string) bool {
	if cc.pending[ref] {
		return false
	}
	if it := cc.data[ref]; it.Ready && it.Err == nil {
		return false
	}
	cc.pending[ref] = true
	return true
}

// contentMsg carries the result of one content fetch.
type contentMsg struct {
	ref  string
	data string
	err  error
}

// fetchContentCmd fetches the body behind ref.
func fetchContentCmd(ctx context.Context, cl *client.Client, ref string) tea.Cmd {
	return func() tea.Msg {
		data, err := cl.Content(ctx, ref)
		return contentMsg{ref: ref, data: string(data), err: err}
	}
}

// fetchRefs returns the fetch commands for the refs the cache still needs.
func fetchRefs(ctx context.Context, cl *client.Client, cc *ContentCache, refs []string) []tea.Cmd {
	var cmds []tea.Cmd
	if cl == nil {
		return nil
	}
	for _, r := range refs {
		if cc.Need(r) {
			cmds = append(cmds, fetchContentCmd(ctx, cl, r))
		}
	}
	return cmds
}

// WrapContentLines wraps safe text to width and prefixes every terminal line with two spaces.
// It applies the content-line indentation requirement: the prefix is applied last, to each
// terminal line, so no content can place a character in column 0 or 1.
func WrapContentLines(text string, width int) []string {
	return SafeTextWithPrefix(text, width)
}

// WrapSafeTextWithPrefix wraps already-escaped text to width and prefixes every terminal line.
// Unlike SafeTextWithPrefix, this does NOT call ToSafeText - it assumes the input is already
// safe (escaped). Use this when the text has already been escaped by ToSafeText.
func WrapSafeTextWithPrefix(safeText string, width int) []string {
	const prefix = "  "
	const minWidth = 3 // minimum 3: prefix (2) + at least 1 for content

	if width < minWidth {
		width = minWidth
	}

	// Split into paragraphs on newlines, trim trailing empty line
	paragraphs := strings.Split(safeText, "\n")
	if len(paragraphs) > 0 && paragraphs[len(paragraphs)-1] == "" {
		paragraphs = paragraphs[:len(paragraphs)-1]
	}

	var out []string
	for _, para := range paragraphs {
		// Wrap paragraph to width, accounting for the prefix
		lines := wrapParagraph(para, width-len(prefix))
		// Add prefix to each line
		for _, line := range lines {
			out = append(out, prefix+line)
		}
	}

	// If input was entirely empty or only newlines, return at least one empty prefixed line
	if len(out) == 0 {
		out = []string{prefix}
	}

	return out
}
