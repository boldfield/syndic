package engine

import "testing"

func TestUnifiedDiff(t *testing.T) {
	cases := []struct {
		name   string
		create bool
		base   string
		body   string
		want   string
	}{
		{
			name:   "create is full content against /dev/null",
			create: true, body: "x\ny\n",
			want: "--- /dev/null\n+++ b/f.txt\n@@ -0,0 +1,2 @@\n+x\n+y\n",
		},
		{
			name:   "create without trailing newline",
			create: true, body: "x",
			want: "--- /dev/null\n+++ b/f.txt\n@@ -0,0 +1 @@\n+x\n\\ No newline at end of file\n",
		},
		{
			name:   "create empty has no hunk",
			create: true, body: "",
			want: "--- /dev/null\n+++ b/f.txt\n",
		},
		{
			name: "modify changes one line with context",
			base: "a\nb\nc\nd\ne\nf\ng\nh\n", body: "a\nb\nc\nD\ne\nf\ng\nh\n",
			want: "--- a/f.txt\n+++ b/f.txt\n@@ -1,7 +1,7 @@\n a\n b\n c\n-d\n+D\n e\n f\n g\n",
		},
		{
			name: "modify appends a line",
			base: "x\n", body: "x\ny\n",
			want: "--- a/f.txt\n+++ b/f.txt\n@@ -1 +1,2 @@\n x\n+y\n",
		},
		{
			name: "distant changes make two hunks",
			base: "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n", body: "1\nX\n3\n4\n5\n6\n7\n8\n9\n10\nY\n12\n",
			want: "--- a/f.txt\n+++ b/f.txt\n@@ -1,5 +1,5 @@\n 1\n-2\n+X\n 3\n 4\n 5\n@@ -8,5 +8,5 @@\n 8\n 9\n 10\n-11\n+Y\n 12\n",
		},
		{
			name: "no newline added at end differs from the same text",
			base: "x", body: "x\n",
			want: "--- a/f.txt\n+++ b/f.txt\n@@ -1 +1 @@\n-x\n\\ No newline at end of file\n+x\n",
		},
		{
			name: "no newline on both sides",
			base: "a\nx", body: "a\ny",
			want: "--- a/f.txt\n+++ b/f.txt\n@@ -1,2 +1,2 @@\n a\n-x\n\\ No newline at end of file\n+y\n\\ No newline at end of file\n",
		},
		{
			name: "unchanged content has headers only",
			base: "same\n", body: "same\n",
			want: "--- a/f.txt\n+++ b/f.txt\n",
		},
		{
			name: "empty file gains content",
			base: "", body: "x\n",
			want: "--- a/f.txt\n+++ b/f.txt\n@@ -0,0 +1 @@\n+x\n",
		},
		{
			name: "file emptied",
			base: "x\ny\n", body: "",
			want: "--- a/f.txt\n+++ b/f.txt\n@@ -1,2 +0,0 @@\n-x\n-y\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := unifiedDiff("f.txt", c.create, []byte(c.base), []byte(c.body))
			if got != c.want {
				t.Fatalf("diff:\n%q\nwant:\n%q", got, c.want)
			}
			if again := unifiedDiff("f.txt", c.create, []byte(c.base), []byte(c.body)); again != got {
				t.Fatalf("diff is not reproducible")
			}
		})
	}
}

func TestUnifiedDiffBinaryAndPath(t *testing.T) {
	got := unifiedDiff("b.bin", true, nil, []byte{0, 1, 2})
	want := "--- /dev/null\n+++ b/b.bin\nBinary content, not shown: base sha256 absent, new sha256 ae4b3280e56e2faf83f414a6e3dabe9d5fbe18976544c05fed121accb85b53fc, new length 3\n"
	if got != want {
		t.Fatalf("binary diff = %q", got)
	}
	want = "--- /dev/null\n+++ \"b/a b\\n.txt\"\n@@ -0,0 +1 @@\n+x\n\\ No newline at end of file\n"
	if got := unifiedDiff("a b\n.txt", true, nil, []byte("x")); got != want {
		t.Fatalf("odd path diff = %q", got)
	}
}

func TestUnifiedDiffLargeFallsBackToReplace(t *testing.T) {
	var a, b []byte
	for i := 0; i < 3000; i++ {
		a = append(a, []byte("a\n")...)
		b = append(b, []byte("b\n")...)
	}
	got := unifiedDiff("big", false, a, b)
	if want := "@@ -1,3000 +1,3000 @@\n"; len(got) < len(want) || got[len("--- a/big\n+++ b/big\n"):][:len(want)] != want {
		t.Fatalf("header = %q", got[:60])
	}
}
