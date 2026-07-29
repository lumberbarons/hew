package main

import "testing"

func TestCountEmptyStringIsZero(t *testing.T) {
	c := newTestCounter(t)
	got, err := c.count("")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 0 {
		t.Fatalf("count(%q) = %d, want 0", "", got)
	}
}

// TestCountLocksEncoding pins the counter to o200k_base by asserting exact
// counts for two samples that cl100k_base tokenizes differently (25 vs 24, and
// 29 vs 30). Every published figure is comparable to DESIGN.md's prime-budget
// spike only while this holds, so a silent encoding swap has to fail here.
func TestCountLocksEncoding(t *testing.T) {
	c := newTestCounter(t)
	cases := []struct {
		name string
		in   string
		want int
	}{
		{
			name: "json line",
			in:   `{"number":117,"priority":"P1","areas":["tests"],"blockedBy":[],"subIssuesTotal":0}`,
			want: 25,
		},
		{
			name: "epic status lines",
			in:   "  ✓ #123 P2 task  Wire voltgo into config\n  ○ #124 P2 bug (tests)  /api/info\n",
			want: 29,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.count(tc.in)
			if err != nil {
				t.Fatalf("count: %v", err)
			}
			if got != tc.want {
				t.Fatalf("count = %d, want %d (encoding must stay %s)", got, tc.want, encodingName)
			}
		})
	}
}

func TestCountGrowsWithInput(t *testing.T) {
	c := newTestCounter(t)
	short, err := c.count("#42 P2 task  Wire voltgo\n")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	long, err := c.count("#42 P2 task  Wire voltgo\n#43 P3 bug  Epever decoding\n")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if long <= short {
		t.Fatalf("two lines counted %d tokens, one line %d; want more", long, short)
	}
}

func newTestCounter(t *testing.T) *counter {
	t.Helper()
	c, err := newCounter()
	if err != nil {
		t.Fatalf("newCounter: %v", err)
	}
	return c
}
