package tui

import (
	"context"

	"github.com/boldfield/syndic/internal/client"
	tea "github.com/charmbracelet/bubbletea"
)

// ContentCache holds content bodies by ref. A ref that failed is not a
// success: Need reports it again so the fetch is retried.
type ContentCache struct {
	data    map[string]ContentItem
	pending map[string]bool
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
