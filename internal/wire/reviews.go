package wire

import "encoding/json"

// ReviewsRequest asks for one page of the accepted verdicts of a turn.
type ReviewsRequest struct {
	TurnID string  `json:"turn_id"`
	Cursor *string `json:"cursor,omitempty"`
	Limit  *int    `json:"limit,omitempty"`
}

// ReviewBlock is one block's state in a listed verdict.
type ReviewBlock struct {
	BlockID  string            `json:"block_id"`
	State    string            `json:"state"`
	Severity *string           `json:"severity,omitempty"`
	Evidence *ContentReference `json:"evidence,omitempty"`
	Remedy   *ContentReference `json:"remedy,omitempty"`
}

// ContentReference points at content fetched through the content request.
type ContentReference struct {
	Ref     string `json:"ref"`
	SHA256  string `json:"sha256"`
	ByteLen int64  `json:"byte_len"`
}

// ReviewItem is the decoded form of one reviews item. The server never
// encodes it: it sends the journal's canonical item encoding, which carries
// blocks and commentary in full form and oversized and record in their place
// in short form.
type ReviewItem struct {
	Round         int               `json:"round"`
	CandidateHash string            `json:"candidate_hash"`
	Participant   string            `json:"participant"`
	AttemptID     string            `json:"attempt_id"`
	Kind          string            `json:"kind"`
	Blocks        []ReviewBlock     `json:"blocks,omitempty"`
	Commentary    *ContentReference `json:"commentary,omitempty"`
	Oversized     bool              `json:"oversized,omitempty"`
	Record        *ContentReference `json:"record,omitempty"`
}

// ReviewsReply is one page of listed verdicts. Items are raw so that the
// encoding a reader sees is the stored record's own.
type ReviewsReply struct {
	Items      []json.RawMessage `json:"items"`
	NextCursor *string           `json:"next_cursor,omitempty"`
}

// DecodeItems decodes the page's items.
func (r ReviewsReply) DecodeItems() ([]ReviewItem, error) {
	out := make([]ReviewItem, len(r.Items))
	for i, raw := range r.Items {
		if err := json.Unmarshal(raw, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}
