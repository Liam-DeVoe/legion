package text

import "testing"

// EndsInURL reads the last run with no white space of what a cut kept, so a caller that marks
// the cut can put a space between a URL and the mark: a run that holds a "://", or a "www." at
// its start or after one of ( * _ [ ] ~, where GitHub's markdown starts a link with no scheme.
func TestEndsInURLReadsTheLastRun(t *testing.T) {
	for _, row := range []struct {
		value string
		want  bool
	}{
		{"Log: https://example.com/a", true},
		{"https://example.com/a", true},
		{"See www.example.com", true},
		{"See (www.example.com", true},
		{"https://example.com/a and more", false},
		{"Log:", false},
		{"/srv/data/segment/", false},
		{"See awww.example.com", false},
		{"", false},
	} {
		if got := EndsInURL(row.value); got != row.want {
			t.Errorf("EndsInURL(%q) = %v, want %v", row.value, got, row.want)
		}
	}
}
