package journal

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Review kinds, as the engine classifies a verdict.
const (
	ReviewKindApprove            = "approve"
	ReviewKindBlock              = "block"
	ReviewKindApproveWithChanges = "approve-with-changes"
)

// Block states within one accepted verdict.
const (
	ReviewBlockOpened     = "opened"
	ReviewBlockMaintained = "maintained"
	ReviewBlockWithdrawn  = "withdrawn"
)

const (
	// ReviewOversizedBytes is the encoded size above which an item is listed in
	// short form, with a ref to its stored record in place of its blocks.
	ReviewOversizedBytes = 64 << 10
	// ReviewMaxLimit is the most items one page may hold.
	ReviewMaxLimit = 50
)

// ErrBadReviewCursor reports a cursor that names no accepted verdict of the turn.
var ErrBadReviewCursor = errors.New("invalid reviews cursor")

// ContentRef points at content fetched as transcript content is.
type ContentRef struct {
	Ref     string `json:"ref"`
	SHA256  string `json:"sha256"`
	ByteLen int64  `json:"byte_len"`
}

// ReviewBlock is one block's state in an accepted verdict.
type ReviewBlock struct {
	BlockID  string      `json:"block_id"`
	State    string      `json:"state"`
	Severity string      `json:"severity,omitempty"`
	Evidence *ContentRef `json:"evidence,omitempty"`
	Remedy   *ContentRef `json:"remedy,omitempty"`
}

// ReviewRecord is the full item for one accepted verdict. Its canonical JSON
// is what is stored and what a record ref retrieves.
type ReviewRecord struct {
	Round         int           `json:"round"`
	CandidateHash string        `json:"candidate_hash"`
	Participant   string        `json:"participant"`
	AttemptID     string        `json:"attempt_id"`
	Kind          string        `json:"kind"`
	Blocks        []ReviewBlock `json:"blocks"`
	Commentary    ContentRef    `json:"commentary"`
}

// ReviewItem is a ReviewRecord as listed: whole, or in short form when its
// encoding exceeds ReviewOversizedBytes.
type ReviewItem struct {
	ReviewRecord
	VerdictID string
	Oversized bool
	Record    *ContentRef
}

type reviewShortItem struct {
	Round         int         `json:"round"`
	CandidateHash string      `json:"candidate_hash"`
	Participant   string      `json:"participant"`
	AttemptID     string      `json:"attempt_id"`
	Kind          string      `json:"kind"`
	Oversized     bool        `json:"oversized"`
	Record        *ContentRef `json:"record"`
}

// MarshalJSON encodes the item in its listed shape.
func (it ReviewItem) MarshalJSON() ([]byte, error) {
	if it.Oversized {
		return json.Marshal(reviewShortItem{it.Round, it.CandidateHash, it.Participant, it.AttemptID, it.Kind, true, it.Record})
	}
	return json.Marshal(it.ReviewRecord)
}

// ReviewBlockState is a block a verdict names as maintained or withdrawn.
type ReviewBlockState struct {
	BlockID string
	State   string
}

// AcceptedReview describes a verdict the engine has just accepted.
type AcceptedReview struct {
	TurnID        string
	VerdictID     string
	AttemptID     string
	Participant   string
	RosterIndex   int
	CandidateHash string
	Kind          string
	// States are the blocks the verdict maintains or withdraws; the block it
	// opens is found by its verdict.
	States []ReviewBlockState
}

// originBlock is the stored block a reviewer's block id stands for.
type originBlock struct {
	rowid    int64
	id       string
	reviewer string
	severity string
	evidence string
	remedy   string
}

func evidenceRef(b originBlock) *ContentRef {
	return textRef("evidence:"+b.id, b.evidence)
}

func remedyRef(b originBlock) *ContentRef {
	return textRef("remedy:"+b.id, b.remedy)
}

func textRef(ref, body string) *ContentRef {
	return &ContentRef{Ref: ref, SHA256: computeSHA256([]byte(body)), ByteLen: int64(len(body))}
}

type reviewBlockSrc struct {
	blockID string
	state   string
	origin  *originBlock
}

// RecordAcceptedReview writes the record of a verdict the engine has accepted.
// It runs in the acceptance transaction, after the verdict's block is opened.
func (tx *Tx) RecordAcceptedReview(a AcceptedReview) error {
	round, err := tx.GetRoundForCandidateHash(a.TurnID, a.CandidateHash)
	if err != nil {
		return err
	}
	opened, err := tx.originBlocks(`WHERE b.verdict_id = ?`, a.VerdictID)
	if err != nil {
		return err
	}
	var srcs []reviewBlockSrc
	for i := range opened {
		srcs = append(srcs, reviewBlockSrc{opened[i].reviewerOrID(), ReviewBlockOpened, &opened[i]})
	}
	seen := map[string]bool{}
	for _, s := range srcs {
		seen[s.blockID] = true
	}
	for _, st := range a.States {
		if seen[st.BlockID] || (st.State != ReviewBlockMaintained && st.State != ReviewBlockWithdrawn) {
			continue
		}
		seen[st.BlockID] = true
		found, err := tx.originBlocks(`JOIN verdicts v ON v.id = b.verdict_id JOIN attempts a ON a.id = v.attempt_id
			WHERE a.turn_id = ? AND a.participant = ? AND b.reviewer_block_id = ? AND b.verdict_id != ?
			ORDER BY b.rowid DESC LIMIT 1`, a.TurnID, a.Participant, st.BlockID, a.VerdictID)
		if err != nil {
			return err
		}
		src := reviewBlockSrc{blockID: st.BlockID, state: st.State}
		if len(found) > 0 {
			src.origin = &found[0]
		}
		srcs = append(srcs, src)
	}
	return tx.insertReviewRecord(a, round, srcs)
}

func (b originBlock) reviewerOrID() string {
	if b.reviewer != "" {
		return b.reviewer
	}
	return b.id
}

// originBlocks selects blocks with the given trailing SQL (JOINs, WHERE, ...).
func (tx *Tx) originBlocks(tail string, args ...any) ([]originBlock, error) {
	rows, err := tx.tx.Query(`SELECT b.rowid, b.id, COALESCE(b.reviewer_block_id, ''), b.severity, b.evidence, b.remedy FROM blocks b `+tail, args...)
	if err != nil {
		return nil, fmt.Errorf("query review blocks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []originBlock
	for rows.Next() {
		var b originBlock
		if err := rows.Scan(&b.rowid, &b.id, &b.reviewer, &b.severity, &b.evidence, &b.remedy); err != nil {
			return nil, fmt.Errorf("scan review block: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// insertReviewRecord builds the canonical record and stores it. Opened blocks
// come first, then the others in the order they were opened.
func (tx *Tx) insertReviewRecord(a AcceptedReview, round int, srcs []reviewBlockSrc) error {
	rank := func(s reviewBlockSrc) int64 {
		if s.state == ReviewBlockOpened {
			return -1
		}
		if s.origin == nil {
			return 1 << 62
		}
		return s.origin.rowid
	}
	sort.SliceStable(srcs, func(i, j int) bool { return rank(srcs[i]) < rank(srcs[j]) })

	var commentary sql.NullString
	if err := tx.tx.QueryRow(`SELECT commentary FROM attempts WHERE id = ?`, a.AttemptID).Scan(&commentary); err != nil {
		return fmt.Errorf("query attempt commentary: %w", err)
	}
	rec := ReviewRecord{
		Round:         round,
		CandidateHash: a.CandidateHash,
		Participant:   a.Participant,
		AttemptID:     a.AttemptID,
		Kind:          a.Kind,
		Blocks:        []ReviewBlock{},
		Commentary:    *textRef("commentary:"+a.AttemptID, commentary.String),
	}
	for _, s := range srcs {
		b := ReviewBlock{BlockID: s.blockID, State: s.state}
		if s.origin != nil {
			b.Severity = s.origin.severity
			b.Evidence = evidenceRef(*s.origin)
			b.Remedy = remedyRef(*s.origin)
		}
		rec.Blocks = append(rec.Blocks, b)
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode review record: %w", err)
	}
	if _, err := tx.tx.Exec(`
		INSERT INTO review_records (verdict_id, turn_id, round, roster_index, participant, attempt_id, candidate_hash, kind, record, record_sha256, record_len)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, a.VerdictID, a.TurnID, round, a.RosterIndex, a.Participant, a.AttemptID, a.CandidateHash, a.Kind, body, computeSHA256(body), len(body)); err != nil {
		return fmt.Errorf("insert review record: %w", err)
	}
	return nil
}

// acceptedVerdicts is the structured acceptance rows: a verdict is accepted
// when it was recorded valid or as a revision request. A malformed original,
// an unsuccessful repair and a rejected verdict are never accepted.
const acceptedVerdicts = `
	FROM verdicts v
	JOIN attempts a ON a.id = v.attempt_id
	WHERE a.turn_id = ? AND v.validation_status IN ('valid', 'revision_request')`

// ReviewCount returns the number of items ReviewsPage lists in all.
func (s *Store) ReviewCount(turnID string) (int, error) {
	var n int
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		n, txErr = tx.ReviewCount(turnID)
		return txErr
	})
	return n, err
}

// ReviewCount returns the number of accepted verdicts of the turn.
func (tx *Tx) ReviewCount(turnID string) (int, error) {
	var n int
	if err := tx.tx.QueryRow(`SELECT COUNT(*) `+acceptedVerdicts, turnID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count reviews: %w", err)
	}
	return n, nil
}

// MaterializeReviewRecords writes, from the structured acceptance rows, the
// record of every accepted verdict of the turn that has none, and returns how
// many it wrote. roster is the participants in roster order. No model output
// is parsed: kind, round and block states come from the verdict and block rows.
func (tx *Tx) MaterializeReviewRecords(turnID string, roster []string) (int, error) {
	type pending struct {
		a      AcceptedReview
		parsed sql.NullString
		round  int
		vrow   int64
	}
	rows, err := tx.tx.Query(`
		SELECT v.rowid, v.id, v.attempt_id, a.participant, v.candidate_hash, v.parsed_verdict,
			COALESCE((SELECT c.round FROM candidates c WHERE c.turn_id = a.turn_id AND c.content_hash = v.candidate_hash ORDER BY c.id DESC LIMIT 1), 0)
		`+acceptedVerdicts+`
		AND NOT EXISTS (SELECT 1 FROM review_records r WHERE r.verdict_id = v.id)
		ORDER BY v.rowid`, turnID)
	if err != nil {
		return 0, fmt.Errorf("query unrecorded verdicts: %w", err)
	}
	var todo []pending
	for rows.Next() {
		p := pending{a: AcceptedReview{TurnID: turnID}}
		if err := rows.Scan(&p.vrow, &p.a.VerdictID, &p.a.AttemptID, &p.a.Participant, &p.a.CandidateHash, &p.parsed, &p.round); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan unrecorded verdict: %w", err)
		}
		todo = append(todo, p)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()
	if len(todo) == 0 {
		return 0, nil
	}

	// Every block of the turn, with the round it was opened in and, when
	// withdrawn, the round of the candidate it was withdrawn against.
	type turnBlock struct {
		originBlock
		participant    string
		state          string
		openedRound    int
		withdrawnRound int
		withdrawnHash  string
		verdictID      string
	}
	brows, err := tx.tx.Query(`
		SELECT b.rowid, b.id, COALESCE(b.reviewer_block_id, ''), b.severity, b.evidence, b.remedy,
			a.participant, b.state, b.verdict_id, COALESCE(b.withdrawn_against_hash, ''),
			COALESCE((SELECT c.round FROM candidates c WHERE c.turn_id = a.turn_id AND c.content_hash = b.candidate_hash ORDER BY c.id DESC LIMIT 1), 0),
			COALESCE((SELECT c.round FROM candidates c WHERE c.turn_id = a.turn_id AND c.content_hash = b.withdrawn_against_hash ORDER BY c.id DESC LIMIT 1), 0)
		FROM blocks b JOIN verdicts v ON v.id = b.verdict_id JOIN attempts a ON a.id = v.attempt_id
		WHERE a.turn_id = ? ORDER BY b.rowid`, turnID)
	if err != nil {
		return 0, fmt.Errorf("query turn blocks: %w", err)
	}
	var blocks []turnBlock
	for brows.Next() {
		var b turnBlock
		if err := brows.Scan(&b.rowid, &b.id, &b.reviewer, &b.severity, &b.evidence, &b.remedy,
			&b.participant, &b.state, &b.verdictID, &b.withdrawnHash, &b.openedRound, &b.withdrawnRound); err != nil {
			_ = brows.Close()
			return 0, fmt.Errorf("scan turn block: %w", err)
		}
		blocks = append(blocks, b)
	}
	if err := brows.Err(); err != nil {
		_ = brows.Close()
		return 0, err
	}
	_ = brows.Close()

	index := map[string]int{}
	for i, id := range roster {
		index[id] = i
	}
	for _, p := range todo {
		a := p.a
		switch {
		case p.parsed.Valid:
			a.Kind = p.parsed.String
		default:
			a.Kind = ReviewKindApproveWithChanges
		}
		if i, ok := index[a.Participant]; ok {
			a.RosterIndex = i
		} else {
			a.RosterIndex = len(roster)
		}
		var srcs []reviewBlockSrc
		for _, b := range blocks {
			if b.participant != a.Participant {
				continue
			}
			ob := b.originBlock
			switch {
			case b.verdictID == a.VerdictID:
				srcs = append(srcs, reviewBlockSrc{ob.reviewerOrID(), ReviewBlockOpened, &ob})
			case b.state == "withdrawn" && b.withdrawnHash == a.CandidateHash:
				srcs = append(srcs, reviewBlockSrc{ob.reviewerOrID(), ReviewBlockWithdrawn, &ob})
			case b.openedRound < p.round && (b.state != "withdrawn" || b.withdrawnRound > p.round):
				// Open before this verdict and not withdrawn by it or an
				// earlier one: the reviewer's block stood.
				srcs = append(srcs, reviewBlockSrc{ob.reviewerOrID(), ReviewBlockMaintained, &ob})
			}
		}
		if err := tx.insertReviewRecord(a, p.round, srcs); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}

// ReviewsPage lists the accepted verdicts of the turn ordered by round, then
// roster order, writing any record an earlier acceptance did not. It returns at
// least one and at most limit items (limit is clamped to ReviewMaxLimit) while
// any remain, as many as encode within frameBytes, and the cursor to resume
// with, empty when none remain. An item over ReviewOversizedBytes is short form.
func (s *Store) ReviewsPage(turnID string, roster []string, cursor string, limit, frameBytes int) (items []ReviewItem, next string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		items, next, txErr = tx.ReviewsPage(turnID, roster, cursor, limit, frameBytes)
		return txErr
	})
	return items, next, err
}

// ReviewsPage is the transaction form of Store.ReviewsPage.
func (tx *Tx) ReviewsPage(turnID string, roster []string, cursor string, limit, frameBytes int) ([]ReviewItem, string, error) {
	if limit < 1 || limit > ReviewMaxLimit {
		limit = ReviewMaxLimit
	}
	if _, err := tx.MaterializeReviewRecords(turnID, roster); err != nil {
		return nil, "", err
	}
	rows, err := tx.tx.Query(`
		SELECT verdict_id, round, candidate_hash, participant, attempt_id, kind, record, record_sha256, record_len
		FROM review_records WHERE turn_id = ?
		ORDER BY round, roster_index, participant, rowid`, turnID)
	if err != nil {
		return nil, "", fmt.Errorf("query review records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []ReviewItem
	used := 0
	pastCursor := cursor == ""
	more := false
	for rows.Next() {
		var it ReviewItem
		var body []byte
		var sum string
		var n int64
		if err := rows.Scan(&it.VerdictID, &it.Round, &it.CandidateHash, &it.Participant, &it.AttemptID, &it.Kind, &body, &sum, &n); err != nil {
			return nil, "", fmt.Errorf("scan review record: %w", err)
		}
		if !pastCursor {
			pastCursor = it.VerdictID == cursor
			continue
		}
		if more {
			continue
		}
		if len(items) >= limit {
			more = true
			continue
		}
		if n > ReviewOversizedBytes {
			it.Oversized = true
			it.Record = &ContentRef{Ref: "record:" + it.VerdictID, SHA256: sum, ByteLen: n}
		} else if err := json.Unmarshal(body, &it.ReviewRecord); err != nil {
			return nil, "", fmt.Errorf("decode review record %s: %w", it.VerdictID, err)
		}
		enc, err := json.Marshal(it)
		if err != nil {
			return nil, "", fmt.Errorf("encode review item: %w", err)
		}
		if len(items) > 0 && used+len(enc)+1 > frameBytes {
			more = true
			continue
		}
		used += len(enc) + 1
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if !pastCursor {
		return nil, "", ErrBadReviewCursor
	}
	if more && len(items) > 0 {
		return items, items[len(items)-1].VerdictID, nil
	}
	return items, "", nil
}
