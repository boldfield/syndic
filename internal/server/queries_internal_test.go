package server

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/wire"
)

func summaries(n, pad int) ([]wire.ConversationSummary, []string) {
	items := make([]wire.ConversationSummary, n)
	cursors := make([]string, n)
	for i := range items {
		ws := strings.Repeat("w", pad)
		items[i] = wire.ConversationSummary{ID: string(rune('a' + i)), Workspace: &ws}
		cursors[i] = "snap," + items[i].ID
	}
	return items, cursors
}

// TestFitConversationsPageBoundary pins the trim to the exact encoded frame:
// a page whose frame is exactly MaxFrameBytes is sent whole, one byte more is
// cut after the last item that fits, with the cursor of that item.
func TestFitConversationsPageBoundary(t *testing.T) {
	const id = "req-1"
	// Find the largest padding of the last of three items that still fits.
	fits := func(pad int) bool {
		items, _ := summaries(3, 100)
		ws := strings.Repeat("w", pad)
		items[2].Workspace = &ws
		return replyFits(id, &wire.ConversationsReply{Items: items})
	}
	lo, hi := 0, wire.MaxFrameBytes
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(mid) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	build := func(pad int) ([]wire.ConversationSummary, []string) {
		items, cursors := summaries(3, 100)
		ws := strings.Repeat("w", pad)
		items[2].Workspace = &ws
		return items, cursors
	}

	items, cursors := build(lo)
	reply, err := fitConversationsPage(id, items, cursors, "")
	if err != nil || len(reply.Items) != 3 || reply.NextCursor != nil {
		t.Fatalf("exact fit = %+v, %v; want all 3 items and no cursor", reply, err)
	}
	f, _ := wire.NewReply(id, reply)
	f.V = wire.ProtocolVersion
	data, _ := json.Marshal(f)
	if got := len(data) + 1; got != wire.MaxFrameBytes {
		t.Fatalf("boundary frame is %d bytes, want exactly %d", got, wire.MaxFrameBytes)
	}

	items, cursors = build(lo + 1)
	reply, err = fitConversationsPage(id, items, cursors, "")
	if err != nil || len(reply.Items) != 2 || reply.NextCursor == nil || *reply.NextCursor != cursors[1] {
		t.Fatalf("one byte over = %+v, %v; want 2 items resuming after the second", reply, err)
	}

	// An upstream next cursor is kept when nothing is cut.
	items, cursors = summaries(2, 10)
	reply, err = fitConversationsPage(id, items, cursors, "more")
	if err != nil || len(reply.Items) != 2 || reply.NextCursor == nil || *reply.NextCursor != "more" {
		t.Fatalf("untrimmed page = %+v, %v", reply, err)
	}
}

func TestFitConversationsPageSingleOversizedItem(t *testing.T) {
	items, cursors := summaries(3, 1)
	big := strings.Repeat("w", wire.MaxFrameBytes)
	items[0].Workspace = &big
	if _, err := fitConversationsPage("r", items, cursors, ""); !errors.Is(err, errReplyTooLarge) {
		t.Fatalf("oversized first item: err = %v, want errReplyTooLarge", err)
	}
}
