package delivery

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// hostCheckReach is how far before an issue path the reading's host check reads: the byte before
// the host, github.com, the colon and the longest port it credits (attribution_keys).
const hostCheckReach = 1 + len("github.com") + 1 + 253

// wholeBodyLimit is the longest body the row reads with the whole-body copy. That copy hands each
// path's check the body from its start, so it costs the square of a body's length: over the cost
// row's 1 MiB body, with a path every thirteen bytes, about 40 GB. A longer body is held to the
// copy whose check gets hostCheckReach bytes, which cites as the whole-body copy does over every
// shorter body of the corpus; the same shape at 64 KiB is read whole.
const wholeBodyLimit = 1 << 17

// TestEveryCitationIsDecidedWithinItsReachAsOverTheWholeBody is the differential of
// citationReach, on the reading in the database (attribution_keys). The reading hands each issue
// path's host check the citationReach bytes before the path (readingSlice); before, its one
// pattern decided each path over the whole body. A copy of it whose check gets the body from its
// start (readingSliceFromTheStart) is that whole-body reading. Over a generated corpus
// (reachCorpus) the row holds, for every body:
//
//   - the reading cites what the whole-body copy cites (up to wholeBodyLimit);
//   - a copy whose check gets hostCheckReach bytes cites the same, and one whose check gets a byte
//     fewer cites otherwise for some body of the corpus, so the corpus reaches the edge of what the
//     check reads, which citationReach holds;
//   - the reading hands the checks exactly one slice for each issue path the finder answers, the
//     citationReach bytes before the path or the body before it where the path stands nearer the
//     start (counted through reading_span, createCountedReading).
//
// The Go row also compared authorityBefore and citesGitHubIssue over the reach and the body, and
// counted the passes over the body and the spans of each credited citation's parts. The reading
// has neither function, its check being one pattern over the slice, and no counted body: the one
// pass's captures give a citation's parts. So the first two holds above stand for them.
func TestEveryCitationIsDecidedWithinItsReachAsOverTheWholeBody(t *testing.T) {
	pool, ctx := deliveryTestPool(t)
	const repo = "acme/widgets"
	reach := func(n int) string {
		return fmt.Sprintf("pg_catalog.substring(b.bytes, greatest(path_at - %d, 0) + 1, least(path_at, %d))", n, n)
	}
	if got := reach(citationReach); got != readingSlice {
		t.Fatalf("the slice of a reach of %d is %q, want the reading's own %q", citationReach, got, readingSlice)
	}
	createReadingCopy(t, ctx, pool, "attribution_keys_whole", readingEdit{readingSlice, readingSliceFromTheStart, 1})
	createReadingCopy(t, ctx, pool, "attribution_keys_check_reach", readingEdit{readingSlice, reach(hostCheckReach), 1})
	createReadingCopy(t, ctx, pool, "attribution_keys_check_reach_short", readingEdit{readingSlice, reach(hostCheckReach - 1), 1})
	createCountedReading(t, ctx, pool, "attribution_keys_counted")
	var bodies, paths, cut, shorterDiffers, credited, refused, failures int
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf(format, args...)
		if failures++; failures == 20 {
			t.Fatalf("stopped after %d differences", failures)
		}
	}
	// One statement reads every body, so the row's time is the database's reading and not a round
	// trip for each body: reading_spans_of sets its counts back to 0 at each call.
	corpus := reachCorpus()
	rows, err := pool.Query(ctx, `select (attribution_keys($1, '', '', u.body, null)).cited_issues,
		pg_catalog.octet_length(u.body) <= $3,
		case when pg_catalog.octet_length(u.body) <= $3 then (attribution_keys_whole($1, '', '', u.body, null)).cited_issues end,
		(attribution_keys_check_reach($1, '', '', u.body, null)).cited_issues,
		(attribution_keys_check_reach_short($1, '', '', u.body, null)).cited_issues,
		s.cited, s.spans, s.handed, s.longest
		from unnest($2::text[]) with ordinality u(body, i), lateral reading_spans_of('attribution_keys_counted', $1, u.body) s
		order by u.i`, repo, corpus, wholeBodyLimit)
	if err != nil {
		t.Fatalf("read the corpus: %v", err)
	}
	type answer struct {
		reading, whole, atReach, short, counted []string
		wholeRead                               bool
		spans                                   readingSpans
	}
	var answers []answer
	for rows.Next() {
		var a answer
		if err := rows.Scan(&a.reading, &a.wholeRead, &a.whole, &a.atReach, &a.short, &a.counted, &a.spans.Spans, &a.spans.Handed, &a.spans.Longest); err != nil {
			t.Fatalf("read the answer of body %d: %v", len(answers), err)
		}
		answers = append(answers, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the corpus: %v", err)
	}
	if len(answers) != len(corpus) {
		t.Fatalf("the statement answered %d bodies of %d", len(answers), len(corpus))
	}
	var wholeBodies int
	for i, body := range corpus {
		bodies++
		reading, whole, atReach, short, counted, spans := answers[i].reading, answers[i].whole, answers[i].atReach, answers[i].short, answers[i].counted, answers[i].spans
		if answers[i].wholeRead {
			wholeBodies++
			if !slices.Equal(reading, whole) {
				fail("body %d (%d bytes) cites %v within the reach and %v over the whole body: %.300q", i, len(body), reading, whole, body)
			}
			if !slices.Equal(atReach, whole) {
				fail("body %d (%d bytes) cites %v within the check's reach of %d and %v over the whole body: %.300q", i, len(body), atReach, hostCheckReach, whole, body)
			}
		} else if !slices.Equal(reading, atReach) {
			fail("body %d (%d bytes, longer than the whole-body copy reads) cites %v within the reach and %v within the check's reach of %d", i, len(body), reading, atReach, hostCheckReach)
		}
		if !slices.Equal(short, atReach) {
			shorterDiffers++
		}
		if !slices.Equal(counted, reading) {
			fail("body %d: the counted copy cites %v and the reading %v", i, counted, reading)
		}
		var want readingSpans
		found := readingPath.FindAllStringIndex(body, -1)
		for _, path := range found {
			paths++
			if path[0] > citationReach {
				cut++
			}
			n := min(path[0], citationReach)
			want.Spans++
			want.Handed += n
			want.Longest = max(want.Longest, n)
		}
		if spans != want {
			fail("body %d: the reading handed %+v, want %+v: the reach before each path or the body before it where that is shorter, one slice for each path", i, spans, want)
		}
		if len(found) > 0 && len(reading) == 0 {
			refused++
		}
		credited += len(reading)
	}
	t.Logf("%d bodies, %d of them read whole, %d paths: %d decided over a reach that cut the body; %d bodies cite otherwise over a reach a byte shorter than the check's; %d citations, %d bodies whose paths cite nothing", bodies, wholeBodies, paths, cut, shorterDiffers, credited, refused)
	if wholeBodies < bodies-1 || cut == 0 || shorterDiffers == 0 || credited == 0 || refused == 0 {
		t.Errorf("the corpus has %d bodies read whole of %d (want all but the 1 MiB one), %d paths the reach cuts the body before, %d bodies a reach a byte shorter than the check's changes, %d citations and %d bodies whose paths cite nothing, want some of each: without them the row compares nothing at the reach's edge", wholeBodies, bodies, cut, shorterDiffers, credited, refused)
	}
}

// reachCorpus is the differential row's bodies, the default branch's corpus of the same row:
//
//   - a grid of the shapes at the reach's edges: a port of up to and past 253 digits, an empty one
//     and none, after a run of host bytes up to and past 253 ending in github.com, with a host
//     byte, a space, a comma, an `@` or a two-byte letter before the run, each far from the body's
//     start (the reach cuts the body) and at it;
//   - the bodies of the cost rows, the shape of the 1 MiB one at 64 KiB, the host-name length cases
//     and the default branch's corpus (citationCorpus);
//   - 2,000 bodies drawn with a fixed seed, each of one to five citations with prose before them
//     of lengths at, near and far from the reach, in ASCII and not, any separator from a space to
//     none, schemes, userinfo, hosts that are github.com and hosts that only resemble it, ports,
//     and paths the finder answers and paths it does not, some with a bare #N after them.
func reachCorpus() []string {
	const path = "/acme/widgets/issues/7"
	var bodies []string
	for _, before := range []string{"", "a", " ", ",", "@", "é"} {
		for _, run := range []int{1, 10, 11, 252, 253, 254, 300} {
			host := strings.Repeat("a", max(0, run-10)) + "github.com"[max(0, 10-run):]
			for _, digits := range []int{-1, 0, 1, 252, 253, 254, 300} {
				port := ""
				if digits >= 0 {
					port = ":" + strings.Repeat("4", digits)
				}
				citation := before + host + port + path
				bodies = append(bodies, citation, strings.Repeat("~", 600)+citation, strings.Repeat("a", 600)+citation)
			}
		}
	}
	bySpace, byComma := citationsFarFromASpace()
	bodies = append(bodies,
		strings.Repeat("/a/b/issues/1", 1<<20/13)+",github.com/acme/widgets/issues/7",
		strings.Repeat("/a/b/issues/1", 1<<16/13)+",github.com/acme/widgets/issues/7",
		bySpace,
		byComma,
		strings.Repeat("a", 65533)+" https://github.com/acme/widgets/issues/7",
		strings.Repeat("a", 65533)+"github.com/acme/widgets/issues/7",
		"See https://github.com:"+strings.Repeat("4", 300)+"/acme/widgets/issues/7 here.",
	)
	for _, row := range citationCorpus {
		bodies = append(bodies, row.body)
	}
	alphabets := []string{"a", "1", "-", ".", "~", " ", "é", "中", "😀", "a b", "x,"}
	lengths := []int{0, 1, 5, 252, 253, 254, 505, 506, 507, 508, 700}
	separators := []string{"", " ", ",", "\n", "\t", "(", "<", "[", "é", "—", "\u00a0", "@", ":", "/", "#", "%"}
	schemes := []string{"", "https://", "http://", "HTTPS://", "//", "git+ssh://"}
	userinfos := []string{"", "", "", "user@", "a:b@"}
	hosts := []string{
		"github.com", "github.com", "GitHub.COM", "www.github.com", "evilgithub.com", "github.com.evil.test",
		"gıthub.com", "github.com.", "xn--github.com", "127.0.0.1",
		strings.Repeat("a", 242) + "github.com", strings.Repeat("a", 243) + "github.com",
	}
	ports := []string{"", "", ":443", ":", ":" + strings.Repeat("9", 252), ":" + strings.Repeat("9", 253), ":" + strings.Repeat("9", 254)}
	paths := []string{
		"/acme/widgets/issues/7", "/a/b/issues/12345", "/o.r-g/re_po/issues/9", "/acme/widgets/issues/07",
		"/acme/widgets/issues/7x", "/acme/widgets/issues/7#c", "/acme/widgets/issues/7?q=1", "/acme/widgets/pull/7",
	}
	bares := []string{"", "", " #12", " (#4567)", "#89"}
	draw := rand.New(rand.NewPCG(4904, 2026))
	pick := func(from []string) string { return from[draw.IntN(len(from))] }
	for range 2000 {
		var body strings.Builder
		for range 1 + draw.IntN(5) {
			// n copies of the alphabet: n bytes of an ASCII letter, and whole characters of the
			// others, so every body stays valid UTF-8.
			body.WriteString(strings.Repeat(pick(alphabets), lengths[draw.IntN(len(lengths))]))
			body.WriteString(pick(separators) + pick(schemes) + pick(userinfos) + pick(hosts) + pick(ports) + pick(paths) + pick(bares))
		}
		bodies = append(bodies, body.String())
	}
	return bodies
}
