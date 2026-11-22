package server_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/server"
	"github.com/boldfield/syndic/internal/wire"
)

// seedConversations creates n conversations with no turns, each in a
// workspace padded to pad bytes, and returns their ids.
func seedConversations(t *testing.T, store *journal.Store, n, pad int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ws := "/w/" + strings.Repeat("x", pad)
		var id string
		err := store.Tx(func(tx *journal.Tx) (err error) {
			id, err = tx.CreateConversation(&ws)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// totalTurns counts the turns of every conversation.
func totalTurns(t *testing.T, store *journal.Store) int {
	t.Helper()
	rows, _, err := store.ListConversations(nil, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		n += r.TurnCount
	}
	return n
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != code {
		t.Fatalf("error = %v, want code %q", err, code)
	}
}

// probingEngine answers as an engine still probing: hello says so and submit
// is refused, as the real engine does before its run starts.
type probingEngine struct{ server.Engine }

func (p probingEngine) Hello() wire.HelloReply {
	h := p.Engine.Hello()
	h.State = "probing"
	return h
}

func (p probingEngine) Submit(wire.SubmitRequest) (wire.SubmitReply, error) {
	return wire.SubmitReply{}, &wire.Error{Code: wire.ErrorCodeProbing}
}

func TestConversationReadsWorkDuringProbing(t *testing.T) {
	h := newHarness(t)
	ids := seedConversations(t, h.Store, 2, 1)
	s := serve(t, probingEngine{h.Engine}, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	if hello := c.Hello(); hello.State != "probing" {
		t.Fatalf("hello state = %q, want probing", hello.State)
	}
	list, err := c.Conversations(ctx, nil, "", 10)
	if err != nil || len(list.Items) != 2 {
		t.Fatalf("conversations during probing = %+v, %v", list, err)
	}
	conv, err := c.Conversation(ctx, ids[0][:12])
	if err != nil || conv.ID != ids[0] {
		t.Fatalf("conversation during probing = %+v, %v", conv, err)
	}
	_, err = c.Submit(ctx, soloReq("probing-1"))
	wantCode(t, err, wire.ErrorCodeProbing)
}

func TestConversationUnknownAndAmbiguousSendNoSubmit(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	// Hex ids: 17 conversations force two to share a first character.
	ids := seedConversations(t, h.Store, 17, 1)
	byFirst := map[string]int{}
	ambiguous := ""
	for _, id := range ids {
		byFirst[id[:1]]++
		if byFirst[id[:1]] == 2 {
			ambiguous = id[:1]
		}
	}
	if ambiguous == "" {
		t.Fatal("no two ids share a first character")
	}

	for _, tc := range []struct{ name, prefix, code string }{
		{"unknown", "zz-not-an-id", wire.ErrorCodeNotFound},
		{"ambiguous", ambiguous, wire.ErrorCodeAmbiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.resetFrames()
			_, err := c.Conversation(ctx, tc.prefix)
			wantCode(t, err, tc.code)
			_, err = c.SubmitToConversation(ctx, tc.prefix, soloReq("guarded-"+tc.name))
			wantCode(t, err, tc.code)
			if n := totalTurns(t, h.Store); n != 0 {
				t.Fatalf("%d turns after a failed resolution, want none", n)
			}
		})
	}
}

func TestSubmitToConversationUsesCanonicalID(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	ids := seedConversations(t, h.Store, 1, 1) // workspace "/w/x"
	req := soloReq("canon-1")
	req.Workspace = "/w/other"
	_, err := c.SubmitToConversation(ctx, ids[0][:10], req)
	wantCode(t, err, wire.ErrorCodeWorkspaceMismatch)
	if n := totalTurns(t, h.Store); n != 0 {
		t.Fatalf("workspace mismatch submitted %d turns", n)
	}

	req.Workspace = "/w/x"
	reply, err := c.SubmitToConversation(ctx, ids[0][:10], req)
	if err != nil {
		t.Fatal(err)
	}
	if reply.ConversationID != ids[0] {
		t.Fatalf("submit went to %s, want %s", reply.ConversationID, ids[0])
	}
	conv, err := c.Conversation(ctx, ids[0][:10])
	if err != nil || conv.ID != ids[0] || len(conv.Turns) != 1 || conv.Turns[0].ID != reply.TurnID {
		t.Fatalf("conversation = %+v, %v", conv, err)
	}
}

func TestReconnectSeesLatestTurnState(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c1 := s.dial()
	ctx := ctxT(t)

	release := h.Hold("author")
	reply, err := c1.Submit(ctx, reviewReq("recon-1"))
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(reply.TurnID, engine.StateAuthorDispatched)

	// A new connection sees the journaled turn_state of the live turn, and the list marks it running.
	c2 := s.dial()
	conv, err := c2.Conversation(ctx, reply.ConversationID[:8])
	if err != nil || len(conv.Turns) != 1 {
		t.Fatalf("conversation = %+v, %v", conv, err)
	}
	if got := conv.Turns[0].State; got != engine.StateAuthorDispatched {
		t.Fatalf("live turn state = %q, want %q", got, engine.StateAuthorDispatched)
	}
	list, err := c2.Conversations(ctx, nil, "", 10)
	if err != nil || len(list.Items) != 1 || !list.Items[0].Running || list.Items[0].TurnCount != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}

	// After the turn ends, a later reconnect reports the terminal state.
	release()
	row := waitTerminal(t, h, reply.TurnID)
	_ = c2.Close()
	c3 := s.dial()
	conv, err = c3.Conversation(ctx, reply.ConversationID)
	if err != nil || conv.Turns[0].State != row.TerminalState {
		t.Fatalf("after end: %+v (terminal %q), %v", conv, row.TerminalState, err)
	}
	list, err = c3.Conversations(ctx, nil, "", 10)
	if err != nil || list.Items[0].Running {
		t.Fatalf("list after end = %+v, %v", list, err)
	}
}

func walk(t *testing.T, c *client.Client, ws *string, limit int) (pages int, seen []string) {
	t.Helper()
	ctx := ctxT(t)
	cursor := ""
	for {
		pages++
		if pages > 500 {
			t.Fatal("paging does not terminate")
		}
		page, err := c.Conversations(ctx, ws, cursor, limit)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range page.Items {
			seen = append(seen, it.ID)
		}
		if page.NextCursor == nil {
			return pages, seen
		}
		if len(page.Items) == 0 {
			t.Fatal("empty page with a next cursor")
		}
		cursor = *page.NextCursor
	}
}

func assertEach(t *testing.T, seen, want []string) {
	t.Helper()
	count := map[string]int{}
	for _, id := range seen {
		count[id]++
	}
	for _, id := range want {
		if count[id] != 1 {
			t.Fatalf("conversation %s returned %d times", id, count[id])
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("returned %d conversations, want %d", len(seen), len(want))
	}
}

func TestConversationListPagesByCursor(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ids := seedConversations(t, h.Store, 7, 1)

	pages, seen := walk(t, c, nil, 3)
	if pages != 3 {
		t.Fatalf("%d pages of 3 for 7 conversations, want 3", pages)
	}
	assertEach(t, seen, ids)

	// The limit is clamped to the protocol maximum.
	page, err := c.Conversations(ctxT(t), nil, "", 100000)
	if err != nil || len(page.Items) != 7 || page.NextCursor != nil {
		t.Fatalf("clamped page = %+v, %v", page, err)
	}

	_, err = c.Conversations(ctxT(t), nil, "not-a-cursor", 3)
	wantCode(t, err, wire.ErrorCodeBadRequest)
}

// TestConversationListStopsEarlyAtFrameLimit fills a page past the frame
// limit and walks the next_cursor chain: every conversation arrives exactly
// once and no frame exceeds the limit.
func TestConversationListStopsEarlyAtFrameLimit(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	// 150 items of ~12 KiB: one default page of 200 cannot fit in 1 MiB.
	n := 150
	if testing.Short() {
		n = 100 // still over the limit at ~12 KiB each
	}
	ids := seedConversations(t, h.Store, n, 12<<10)

	s.resetFrames()
	first, err := c.Conversations(ctxT(t), nil, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) == 0 || len(first.Items) >= n || first.NextCursor == nil {
		t.Fatalf("first page has %d of %d items, next %v; want a trimmed page", len(first.Items), n, first.NextCursor)
	}
	if s.maxFrame() > wire.MaxFrameBytes {
		t.Fatalf("frame of %d bytes exceeds the limit", s.maxFrame())
	}
	pages, seen := walk(t, c, nil, 0)
	if pages < 2 {
		t.Fatalf("%d pages, want the list split across several", pages)
	}
	assertEach(t, seen, ids)
}

func TestConversationRefusedWhenOneItemExceedsFrame(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	seedConversations(t, h.Store, 1, wire.MaxFrameBytes)

	_, err := c.Conversations(ctxT(t), nil, "", 10)
	wantCode(t, err, wire.ErrorCodeFrame)
}
