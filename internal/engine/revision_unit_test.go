package engine

import "testing"

// A revision is unchanged only when the answer bytes and the change-set lines
// both equal the previous candidate's.
func TestSameRevision(t *testing.T) {
	patch := []string{"a.txt write 0000 1111 5"}
	other := []string{"a.txt write 0000 2222 5"}
	cases := []struct {
		name       string
		prev, next string
		prevL      []string
		nextL      []string
		want       bool
	}{
		{"same text, no patch", "answer", "answer", nil, nil, true},
		{"same text, same patch", "answer", "answer", patch, patch, true},
		{"same text, different patch", "answer", "answer", patch, other, false},
		{"same text, patch added", "answer", "answer", nil, patch, false},
		{"same text, patch dropped", "answer", "answer", patch, nil, false},
		{"changed text, same patch", "answer", "answer.", patch, patch, false},
		{"changed text, no patch", "answer", "answer.", nil, nil, false},
		{"whitespace is a change", "answer", "answer\n", nil, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sameRevision(c.prev, c.prevL, c.next, c.nextL); got != c.want {
				t.Fatalf("sameRevision = %v, want %v", got, c.want)
			}
		})
	}
}
