package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const v6ReviewsPara = "V6Reviews"

func init() {
	cover(v6ReviewsPara, "a verdict accepted on repair is listed once with the repair attempt's id and no malformed attempt is listed", "TestV6Reviews_AcceptedRepairOnce")
	cover(v6ReviewsPara, "a block opened, maintained and withdrawn shows those states in those rounds", "TestV6Reviews_HistoricalBlockTransitions")
	cover(v6ReviewsPara, "oversized fields and metadata are retrieved completely, in short form where needed", "TestV6Reviews_OversizedFieldsAndMetadata")
	cover(v6ReviewsPara, "a verdict accepted before the record existed is served like a current one", "TestV6Reviews_OldRecords")
	cover(v6ReviewsPara, "a solo turn and a turn whose reviewers all failed list nothing and give no cursor", "TestV6Reviews_EmptyPages")
	cover(v6ReviewsPara, "replacing stored commentary does not change decisions and content hashes stay correct", "TestV6Reviews_MutationNoReparse")
	cover(v6ReviewsPara, "replay frames precede live_from_seq and live frames follow it with no gap", "TestV6Reviews_ReplayLiveBoundary")
	cover(v6ReviewsPara, "an empty replay is followed by live frames starting exactly at live_from_seq", "TestV6Reviews_EmptyReplay")
	cover(v6ReviewsPara, "repeated reconnects from the last seq seen deliver each frame once", "TestV6Reviews_RepeatedReconnect")
	cover(v6ReviewsPara, "a restarted engine replays the same frames to a fresh client", "TestV6Reviews_FreshProcess")
	cover(v6ReviewsPara, "the mouse is opt-in and plain mode suppresses it", "internal/tui.TestV6NotifyMouseOptIn")
}

// v6Items fetches every accepted verdict of the turn through the client.
func v6Items(t *testing.T, s *suite, c *client.Client, turnID string, limit int) []wire.ReviewItem {
	t.Helper()
	items, err := c.AllReviewsPaged(s.ctx(), turnID, limit)
	if err != nil {
		t.Fatalf("reviews: %v", err)
	}
	return items
}

// v6States maps each block of the item to its state.
func v6States(it wire.ReviewItem) map[string]string {
	out := map[string]string{}
	for _, b := range it.Blocks {
		out[b.BlockID] = b.State
	}
	return out
}

func v6WantStates(t *testing.T, it wire.ReviewItem, want map[string]string) {
	t.Helper()
	got := v6States(it)
	if len(got) != len(want) {
		t.Fatalf("round %d blocks = %v, want %v", it.Round, got, want)
	}
	for id, st := range want {
		if got[id] != st {
			t.Fatalf("round %d blocks = %v, want %v", it.Round, got, want)
		}
	}
}

// v6History runs a three-round turn: the reviewer's first verdict is malformed
// and accepted only on repair, then a block opened in round 0 is maintained in
// round 1 and withdrawn in round 2.
func v6History(t *testing.T) (*suite, *client.Client, string) {
	t.Helper()
	s := newSuite(t, revRoster(2, 5), map[string][]fake.Step{
		"author": revAuthor(revAnswer, revRevised, revRevised+" (again)"),
		"reviewer": revAcks(
			revMalformed(),
			revBlock0("b1", "the sum is off by one"),
			revV2(1, "block", "b2", "still wrong", "b1 maintained"),
			revV2(2, "approve", "", "", "b1 withdrawn", "b2 withdrawn"),
		),
	})
	c := s.Dial(t)
	turnID, turn := revSubmit(t, s, c, "s1", nil)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	return s, c, turnID
}

func TestV6Reviews_AcceptedRepairOnce(t *testing.T) {
	t.Parallel()
	s, c, turnID := v6History(t)
	turn := s.Turn(t, c, turnID)

	var repair, malformed *wire.AttemptRecord
	for i, a := range turn.Attempts {
		if a.Phase == "review" && a.RepairOf != nil && repair == nil {
			repair = &turn.Attempts[i]
		}
	}
	if repair == nil {
		t.Fatalf("no repair attempt among %+v", turn.Attempts)
	}
	for i, a := range turn.Attempts {
		if a.ID == *repair.RepairOf {
			malformed = &turn.Attempts[i]
		}
	}
	if malformed == nil {
		t.Fatalf("repair of %s has no original among %+v", *repair.RepairOf, turn.Attempts)
	}

	items := v6Items(t, s, c, turnID, 50)
	if len(items) != 3 {
		t.Fatalf("%d verdicts listed, want one per round", len(items))
	}
	count := map[string]int{}
	for _, it := range items {
		count[it.AttemptID]++
		if it.AttemptID == malformed.ID {
			t.Fatalf("the malformed attempt %s is listed", malformed.ID)
		}
	}
	if count[repair.ID] != 1 || items[0].AttemptID != repair.ID || items[0].Round != 0 || items[0].Kind != "block" {
		t.Fatalf("round 0 item %+v, want the block verdict accepted on repair %s exactly once (counts %v)", items[0], repair.ID, count)
	}
	// The review, its repair and one request for each later round.
	revWantCalls(t, s, "reviewer", "review", 4)
}

func TestV6Reviews_HistoricalBlockTransitions(t *testing.T) {
	t.Parallel()
	s, c, turnID := v6History(t)
	for _, limit := range []int{1, 2, 50} {
		items := v6Items(t, s, c, turnID, limit)
		if len(items) != 3 {
			t.Fatalf("limit %d: %d items, want 3", limit, len(items))
		}
		for i, it := range items {
			if it.Round != i {
				t.Fatalf("limit %d: item %d is of round %d", limit, i, it.Round)
			}
		}
		v6WantStates(t, items[0], map[string]string{"b1": journal.ReviewBlockOpened})
		v6WantStates(t, items[1], map[string]string{"b1": journal.ReviewBlockMaintained, "b2": journal.ReviewBlockOpened})
		v6WantStates(t, items[2], map[string]string{"b1": journal.ReviewBlockWithdrawn, "b2": journal.ReviewBlockWithdrawn})
		if items[0].Kind != "block" || items[1].Kind != "block" || items[2].Kind != "approve" {
			t.Fatalf("limit %d: kinds %s %s %s", limit, items[0].Kind, items[1].Kind, items[2].Kind)
		}
	}
}

// v6Manifest names the roster of seeded turns.
const v6Manifest = `{"roster":{"default_preset":"p","presets":{"p":{"mode":"review","author":"au","required_reviewers":["rb","ra"]}}}}`

type v6Block struct{ id, evidence, remedy string }

type v6Verdict struct {
	participant string
	roster      int
	round       int
	kind        string
	opened      []v6Block
	maintained  []string
}

// v6Seed journals a turn of valid verdicts as the engine would. With accept
// set the record of each verdict is written at acceptance; without, the turn
// looks like one accepted before records existed.
func v6Seed(t *testing.T, s *suite, rounds int, accept bool, vs []v6Verdict) string {
	t.Helper()
	var turn string
	err := s.Store.Tx(func(tx *journal.Tx) error {
		runID, _, err := tx.CreateRun(json.RawMessage(v6Manifest))
		if err != nil {
			return err
		}
		if turn, err = tx.CreateTurn(runID, "review", nil); err != nil {
			return err
		}
		prev := ""
		for i := 0; i < rounds; i++ {
			if prev, err = tx.SetLatestCandidate(turn, fmt.Sprintf("content %d", i), fmt.Sprintf("h%d", i), i, prev); err != nil {
				return err
			}
		}
		for _, v := range vs {
			hash := fmt.Sprintf("h%d", v.round)
			a, err := tx.BeginAttempt(turn, v.participant, "review", 1, nil, "")
			if err != nil {
				return err
			}
			if err := tx.SetAttemptCommentary(a, "commentary of "+v.participant); err != nil {
				return err
			}
			vid, err := tx.RecordVerdict(a, hash, v.kind, "", "valid")
			if err != nil {
				return err
			}
			for _, b := range v.opened {
				if _, err := tx.OpenBlockFor(vid, b.id, hash, "low", b.evidence, b.remedy); err != nil {
					return err
				}
			}
			if !accept {
				continue
			}
			var states []journal.ReviewBlockState
			for _, id := range v.maintained {
				states = append(states, journal.ReviewBlockState{BlockID: id, State: journal.ReviewBlockMaintained})
			}
			if err := tx.RecordAcceptedReview(journal.AcceptedReview{TurnID: turn, VerdictID: vid, AttemptID: a,
				Participant: v.participant, RosterIndex: v.roster, CandidateHash: hash, Kind: v.kind, States: states}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	return turn
}

// v6Verified fetches the content the reference names and requires its bytes to
// match the reference's sha256 and byte_len.
func v6Verified(t *testing.T, s *suite, c *client.Client, ref *wire.ContentReference) []byte {
	t.Helper()
	if ref == nil {
		t.Fatal("missing content reference")
	}
	body, err := c.Content(s.ctx(), ref.Ref)
	if err != nil {
		t.Fatalf("content %s: %v", ref.Ref, err)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != ref.SHA256 || int64(len(body)) != ref.ByteLen {
		t.Fatalf("content of %s is %d bytes with sha256 %x, the reference says %d bytes and %s", ref.Ref, len(body), sum, ref.ByteLen, ref.SHA256)
	}
	return body
}

func v6BigBlocks(n int) (opened []v6Block, ids []string) {
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-%d", strings.Repeat("x", 1024), i)
		opened = append(opened, v6Block{id, "e", "r"})
		ids = append(ids, id)
	}
	return opened, ids
}

func TestV6Reviews_OversizedFieldsAndMetadata(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))
	c := s.Dial(t)

	// Metadata: 3,000 block ids of 1 KiB each, in short form with a record.
	n, size := 3000, 600<<10
	if testing.Short() {
		n, size = 300, 100<<10
	}
	opened, ids := v6BigBlocks(n)
	// Fields: evidence and remedy of 600 KiB on a block of the second turn.
	ev, rm := strings.Repeat("e", size), strings.Repeat("R", size)
	turn := v6Seed(t, s, 2, true, []v6Verdict{
		{participant: "rb", roster: 0, round: 0, kind: "block", opened: opened},
		{participant: "rb", roster: 0, round: 1, kind: "block", maintained: ids},
		{participant: "ra", roster: 1, round: 1, kind: "block", opened: []v6Block{{"big", ev, rm}}},
	})

	items := v6Items(t, s, c, turn, 1)
	if len(items) != 3 {
		t.Fatalf("%d items, want 3", len(items))
	}
	for _, it := range items[:2] {
		if !it.Oversized || it.Record == nil || it.Blocks != nil || it.Commentary != nil {
			t.Fatalf("round %d item is not in short form: %+v", it.Round, it)
		}
		full, err := c.FetchOversizedRecord(s.ctx(), &it)
		if err != nil {
			t.Fatalf("fetch record: %v", err)
		}
		if len(full.Blocks) != n {
			t.Fatalf("record has %d blocks, want %d", len(full.Blocks), n)
		}
		for i, b := range full.Blocks {
			if b.BlockID != ids[i] {
				t.Fatalf("record block %d has id %.20s", i, b.BlockID)
			}
		}
		v6Verified(t, s, c, it.Record)
	}
	small := items[2]
	if small.Oversized || len(small.Blocks) != 1 {
		t.Fatalf("one-block item %+v is not in full form", small)
	}
	if got := string(v6Verified(t, s, c, small.Blocks[0].Evidence)); got != ev {
		t.Fatalf("evidence has %d bytes, want %d", len(got), len(ev))
	}
	if got := string(v6Verified(t, s, c, small.Blocks[0].Remedy)); got != rm {
		t.Fatalf("remedy has %d bytes, want %d", len(got), len(rm))
	}
	// The suite's cleanup fails the test if any frame reached the cap.
}

func TestV6Reviews_OldRecords(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))
	c := s.Dial(t)
	n := 3000
	if testing.Short() {
		n = 300
	}
	opened, ids := v6BigBlocks(n)
	vs := []v6Verdict{
		{participant: "rb", roster: 0, round: 0, kind: "block", opened: opened},
		{participant: "rb", roster: 0, round: 1, kind: "block", maintained: ids},
		{participant: "ra", roster: 1, round: 1, kind: "approve"},
	}
	old := v6Seed(t, s, 2, false, vs)
	cur := v6Seed(t, s, 2, true, vs)

	oldItems, curItems := v6Items(t, s, c, old, 50), v6Items(t, s, c, cur, 50)
	if len(oldItems) != 3 || len(curItems) != 3 {
		t.Fatalf("%d old and %d current items, want 3 each", len(oldItems), len(curItems))
	}
	for i := range oldItems {
		o, n := oldItems[i], curItems[i]
		if o.Round != n.Round || o.Participant != n.Participant || o.Kind != n.Kind || o.Oversized != n.Oversized || len(o.Blocks) != len(n.Blocks) {
			t.Fatalf("old item %d %+v differs from the current %+v", i, o, n)
		}
	}
	for _, it := range oldItems[:2] {
		if !it.Oversized || it.Record == nil {
			t.Fatalf("old oversized item is not in short form: %+v", it)
		}
		full, err := c.FetchOversizedRecord(s.ctx(), &it)
		if err != nil || len(full.Blocks) != n {
			t.Fatalf("old record: %d blocks, err %v, want %d blocks", len(full.Blocks), err, n)
		}
	}
	if oldItems[2].Oversized || len(oldItems[2].Blocks) != 0 || oldItems[2].Kind != "approve" {
		t.Fatalf("old small item = %+v", oldItems[2])
	}
}

func TestV6Reviews_EmptyPages(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, s *suite, c *client.Client, turnID string) {
		t.Helper()
		page, err := c.Reviews(s.ctx(), turnID, nil, nil)
		if err != nil {
			t.Fatalf("reviews: %v", err)
		}
		if len(page.Items) != 0 || page.NextCursor != nil {
			t.Fatalf("reviews = %d items with cursor %v, want an empty list and none", len(page.Items), page.NextCursor)
		}
	}
	t.Run("solo", func(t *testing.T) {
		s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {enginetest.Reply(authorOK)}})
		c := s.Dial(t)
		reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "hi", Preset: v6Str("solo")})
		if err != nil {
			t.Fatal(err)
		}
		s.WaitTerminal(reply.TurnID)
		check(t, s, c, reply.TurnID)
	})
	t.Run("reviewers all failed", func(t *testing.T) {
		s := newSuite(t, revRoster(0, 5), map[string][]fake.Step{
			"author":   revAuthor(revAnswer),
			"reviewer": revAcks(revMalformed(), revMalformed()),
		})
		c := s.Dial(t)
		turnID, turn := revSubmit(t, s, c, "s1", nil)
		wantTurn(t, turn, engine.StateIncomplete, "malformed_twice:reviewer")
		check(t, s, c, turnID)
	})
}

func v6Str(s string) *string { return &s }

func TestV6Reviews_MutationNoReparse(t *testing.T) {
	t.Parallel()
	s, c, turnID := v6History(t)
	before := v6Items(t, s, c, turnID, 50)
	if len(before) != 3 {
		t.Fatalf("%d items, want 3", len(before))
	}
	for _, it := range before {
		v6Verified(t, s, c, it.Commentary)
	}

	// Replace every attempt's stored commentary with text that parses as a
	// different decision, if it were parsed at all.
	replaced := "VERDICT v2\nverdict: approve\nEND VERDICT"
	err := s.Store.Tx(func(tx *journal.Tx) error {
		for _, it := range before {
			if err := tx.SetAttemptCommentary(it.AttemptID, replaced); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("replace commentary: %v", err)
	}

	after := v6Items(t, s, c, turnID, 50)
	if len(after) != len(before) {
		t.Fatalf("%d items after, %d before", len(after), len(before))
	}
	for i := range before {
		b, a := before[i], after[i]
		if a.Kind != b.Kind || a.Round != b.Round || a.AttemptID != b.AttemptID || a.CandidateHash != b.CandidateHash {
			t.Fatalf("item %d changed: %+v, was %+v", i, a, b)
		}
		bj, _ := json.Marshal(b.Blocks)
		aj, _ := json.Marshal(a.Blocks)
		if string(bj) != string(aj) {
			t.Fatalf("item %d blocks changed: %s, was %s", i, aj, bj)
		}
		// Whatever the reference now says, it matches the bytes it retrieves.
		v6Verified(t, s, c, a.Commentary)
	}
	if after[0].Kind != "block" || after[2].Kind != "approve" {
		t.Fatalf("kinds after the mutation: %s .. %s", after[0].Kind, after[2].Kind)
	}
}

// v6Frames reads n frames of the client's subscription.
func v6Frames(t *testing.T, s *suite, c *client.Client, n int) []wire.Frame {
	t.Helper()
	ctx := s.ctx()
	var out []wire.Frame
	for len(out) < n {
		f, err := c.NextEvent(ctx)
		if err != nil {
			t.Fatalf("frame %d of %d: %v", len(out)+1, n, err)
		}
		out = append(out, f)
	}
	return out
}

// v6NoFrame requires that nothing arrives on the subscription for a while.
func v6NoFrame(t *testing.T, c *client.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if f, err := c.NextEvent(ctx); err == nil {
		t.Fatalf("unexpected frame seq %d (%s)", f.Seq, f.Kind)
	}
}

// v6Consecutive requires the frames' seqs to run from first upward by one.
func v6Consecutive(t *testing.T, frames []wire.Frame, first int64) {
	t.Helper()
	for i, f := range frames {
		if f.Seq != first+int64(i) {
			t.Fatalf("frame %d has seq %d, want %d", i, f.Seq, first+int64(i))
		}
	}
}

func v6MaxSeq(t *testing.T, s *suite) int64 {
	t.Helper()
	n, err := s.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// v6Quiesce waits until the engine has written no event for a while (the
// deliveries after a turn end follow it) and returns the last seq.
func v6Quiesce(t *testing.T, s *suite) int64 {
	t.Helper()
	last, stable := v6MaxSeq(t, s), 0
	for stable < 10 {
		time.Sleep(50 * time.Millisecond)
		if n := v6MaxSeq(t, s); n != last {
			last, stable = n, 0
		} else {
			stable++
		}
	}
	return last
}

// v6SubscribeFresh dials a new client and subscribes it after since.
func v6SubscribeFresh(t *testing.T, s *suite, since int64) (*client.Client, int64) {
	t.Helper()
	c := s.Dial(t)
	live, err := c.Subscribe(s.ctx(), since)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return c, live
}

func TestV6Reviews_ReplayLiveBoundary(t *testing.T) {
	t.Parallel()
	s := newSuite(t, revRoster(0, 5), map[string][]fake.Step{
		"author":   revAuthor(revAnswer, revAnswer),
		"reviewer": revAcks(revApprove0(), revApprove0()),
	})
	c := s.Dial(t)
	revSubmit(t, s, c, "s1", nil)
	last := v6Quiesce(t, s)
	if last < 3 {
		t.Fatalf("only %d events before the boundary", last)
	}

	rc, live := v6SubscribeFresh(t, s, 0)
	if live != last+1 {
		t.Fatalf("live_from_seq = %d, want %d after %d events", live, last+1, last)
	}
	replay := v6Frames(t, s, rc, int(last))
	v6Consecutive(t, replay, 1)
	if replay[len(replay)-1].Seq >= live {
		t.Fatalf("a replay frame has seq %d at or above live_from_seq %d", replay[len(replay)-1].Seq, live)
	}
	v6NoFrame(t, rc)

	// The next turn's frames are live: the first is exactly at the boundary.
	revSubmit(t, s, c, "s2", nil)
	end := v6Quiesce(t, s)
	liveFrames := v6Frames(t, s, rc, int(end-last))
	v6Consecutive(t, liveFrames, live)
}

func TestV6Reviews_EmptyReplay(t *testing.T) {
	t.Parallel()
	s := newSuite(t, revRoster(0, 5), map[string][]fake.Step{
		"author":   revAuthor(revAnswer),
		"reviewer": revAcks(revApprove0()),
	})
	c := s.Dial(t)
	before := v6MaxSeq(t, s)

	// Subscribing after everything there is: no replay, and the next event is
	// the one at live_from_seq.
	rc, live := v6SubscribeFresh(t, s, before)
	if live != before+1 {
		t.Fatalf("live_from_seq = %d, want %d", live, before+1)
	}
	v6NoFrame(t, rc)

	revSubmit(t, s, c, "s1", nil)
	frames := v6Frames(t, s, rc, int(v6Quiesce(t, s)-before))
	v6Consecutive(t, frames, live)
}

func TestV6Reviews_RepeatedReconnect(t *testing.T) {
	t.Parallel()
	s := newSuite(t, revRoster(0, 5), map[string][]fake.Step{
		"author":   revAuthor(revAnswer, revAnswer, revAnswer),
		"reviewer": revAcks(revApprove0(), revApprove0(), revApprove0()),
	})
	c := s.Dial(t)
	var got []wire.Frame
	var seen int64
	for i := 1; i <= 3; i++ {
		revSubmit(t, s, c, fmt.Sprintf("s%d", i), nil)
		end := v6Quiesce(t, s)
		// A new connection each time, resumed after the last seq processed.
		rc, live := v6SubscribeFresh(t, s, seen)
		if live != end+1 {
			t.Fatalf("reconnect %d: live_from_seq = %d, want %d", i, live, end+1)
		}
		frames := v6Frames(t, s, rc, int(end-seen))
		v6Consecutive(t, frames, seen+1)
		v6NoFrame(t, rc)
		got = append(got, frames...)
		seen = end
	}
	v6Consecutive(t, got, 1)
}

func TestV6Reviews_FreshProcess(t *testing.T) {
	t.Parallel()
	script := func() map[string][]fake.Step {
		return map[string][]fake.Step{
			"author":   revAuthor(revAnswer),
			"reviewer": revAcks(revApprove0()),
		}
	}
	s := newSuite(t, revRoster(0, 5), script())
	c := s.Dial(t)
	revSubmit(t, s, c, "s1", nil)
	last := v6Quiesce(t, s)
	bc, live := v6SubscribeFresh(t, s, 0)
	before := v6Frames(t, s, bc, int(last))

	s.Restart("", script())
	rc, liveAfter := v6SubscribeFresh(t, s, 0)
	if liveAfter < live {
		t.Fatalf("live_from_seq went from %d back to %d", live, liveAfter)
	}
	boundary := liveAfter - 1
	after := v6Frames(t, s, rc, int(boundary))
	v6Consecutive(t, after, 1)
	for i := range before {
		if after[i].Seq != before[i].Seq || after[i].Kind != before[i].Kind || string(after[i].Body) != string(before[i].Body) {
			t.Fatalf("frame %d replayed as %+v, was %+v", i, after[i], before[i])
		}
	}
	v6NoFrame(t, rc)
}
