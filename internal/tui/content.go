package tui

import (
	"context"

	"github.com/boldfield/syndic/internal/client"
	tea "github.com/charmbracelet/bubbletea"
)

// ContentCache stores fetched content bodies by ref.
type ContentCache struct {
	data map[string]ContentItem
}

// ContentItem holds a fetched or in-flight content body.
type ContentItem struct {
	Data  string // UTF-8 text
	Err   error  // non-nil if fetch failed
	Ready bool   // true if fetch completed (success or failure)
}

// NewContentCache returns a fresh ContentCache.
func NewContentCache() *ContentCache {
	return &ContentCache{data: make(map[string]ContentItem)}
}

// Get returns the cached content, or ContentItem with Ready=false if not yet fetched.
func (cc *ContentCache) Get(ref string) ContentItem {
	if item, ok := cc.data[ref]; ok {
		return item
	}
	return ContentItem{Ready: false}
}

// Store stores a completed fetch result.
func (cc *ContentCache) Store(ref string, data string, err error) {
	cc.data[ref] = ContentItem{
		Data:  data,
		Err:   err,
		Ready: true,
	}
}

// contentMsg carries the result of a content fetch or the error that occurred.
type contentMsg struct {
	ref  string
	err  error
	data string
}

// fetchContentCmd fetches content for the given ref.
func fetchContentCmd(ctx context.Context, cl *client.Client, ref string) tea.Cmd {
	return func() tea.Msg {
		data, err := cl.Content(ctx, ref)
		return contentMsg{
			ref:  ref,
			data: string(data),
			err:  err,
		}
	}
}
