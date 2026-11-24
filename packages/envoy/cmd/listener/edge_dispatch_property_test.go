package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/contracts"
)

// updateDispatchProperty rewrites dispatchPropertyFixture's sha256 and passed from this tree's
// edgeFrameVerdict, keeping every other key the file holds.
var updateDispatchProperty = flag.Bool("update-dispatch-property", false,
	"rewrite packages/contracts/fixtures/dispatch-frame-property.json's sha256 and passed")

// dispatchPropertyFixture is where the generated frames' verdicts are recorded for the plugin's
// side of the property (packages/envoy-client/src/__tests__/dispatch-frame-property.test.ts).
const dispatchPropertyFixture = "../../../contracts/fixtures/dispatch-frame-property.json"

// dispatchPropertyFrames is how many frames generatedDispatchFrames writes.
const dispatchPropertyFrames = 100_000

// generatedDispatchFrames is 100,000 Dispatch frames drawn from PCG(5192, 11), the generator of
// INFRA-764's review of #5192 (Job legion-review5192-fz5wn): each on the session's own agent
// subject, its role lane or the token's project's issue topic, its payload an event or a targeted
// frame whose project and issue key are drawn to split a reader of the payload's fields from what
// the plugin shows - case variants, escapes, repeats, a "__proto__" key, the token's project
// beside another project's nested event, a misspelt delivery line. The plugin's side writes the
// same frames from the same draws (dispatch-frame-generator.ts); the two must agree byte for byte,
// which the fixture's sha256 holds.
func generatedDispatchFrames() []string {
	r := rand.New(rand.NewPCG(5192, 11))
	pick := func(pool []string) string { return pool[r.IntN(len(pool))] }
	own := contracts.LegacyAgentSubject("ses_v")
	frames := make([]string, 0, dispatchPropertyFrames)
	for range dispatchPropertyFrames {
		projects := []string{`"PRIV"`, `"PUB"`, `""`, `"priv"`, `" PRIV"`, `"P\u0052IV"`, `"P\u0055B"`, `null`, `5`}
		issues := []string{`"PRIV-1"`, `"PUB-1"`, `"priv-1"`, `" PUB-1"`, `"PUB-01"`, `"PUB-1 "`, `"P\u0055B-1"`, `"P\u0052IV-1"`, `null`, `"PUB"`, `"PRIV-x"`, `"PRIV-1-2"`}
		var fields []string
		add := func(k, v string) { fields = append(fields, `"`+k+`":`+v) }
		switch r.IntN(5) {
		case 0:
		case 1, 2:
			add(pick([]string{"project", "project", "project", "Project", `pr\u006fject`}), pick(projects))
		default:
			add("project", pick(projects))
			if r.IntN(4) == 0 {
				add(pick([]string{"project", "PROJECT"}), pick(projects))
			}
		}
		switch r.IntN(5) {
		case 0:
		case 1, 2:
			add(pick([]string{"issue_key", "issue_key", "issue_key", "Issue_Key", `issue_k\u0065y`}), pick(issues))
		default:
			add("issue_key", pick(issues))
			if r.IntN(4) == 0 {
				add(pick([]string{"issue_key", "ISSUE_KEY"}), pick(issues))
			}
		}
		add("type", `"comment.created"`)
		add("actor", `{"kind":"person","id":"owner@example.com"}`)
		add("notify", `true`)
		add("payload", `{"id":"c-1","body":"BODY"}`)
		if r.IntN(12) == 0 {
			add("__proto__", `{"project":"PUB"}`)
		}
		r.Shuffle(len(fields), func(i, j int) { fields[i], fields[j] = fields[j], fields[i] })
		event := "{" + strings.Join(fields, ",") + "}"
		var payload string
		switch r.IntN(6) {
		case 0, 1:
			payload = event
		case 2, 3:
			payload = `{"event":` + event + `,"delivery":{"attempt":1,"mode":"steer"}}`
		case 4:
			payload = `{"delivery":{"attempt":1},"event":` + event + `,"project":` + pick(projects) + `}`
		default:
			payload = `{"event":` + event + `,"project":` + pick(projects) + `,"issue_key":` + pick(issues) + `}`
		}
		source := pick([]string{`"dispatch"`, `"dispatch"`, `"dispatch"`, `"d\u0069spatch"`})
		topic := pick([]string{own, own, contracts.RoleTopicPrefix + "legion-priv-priv-1-tester", "notifications.dispatch.issue.PRIV.PRIV-1.comment.created"})
		quoted, err := json.Marshal(payload)
		if err != nil {
			panic(err)
		}
		frames = append(frames, `{"source":`+source+`,"event_id":"g","topic":"`+topic+`","payload":`+string(quoted)+`}`)
	}
	return frames
}

// dispatchFramesDigest is the sha256 of frames, each followed by a line feed.
func dispatchFramesDigest(frames []string) string {
	digest := sha256.New()
	for _, frame := range frames {
		digest.Write([]byte(frame))
		digest.Write([]byte("\n"))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// TestEdgeFrameVerdictOnGeneratedDispatchFrames records which of the generated Dispatch frames
// (generatedDispatchFrames) the edge passes on the session's own agent subject for a token of
// PRIV, for the plugin's side of the property to read: there, each passed frame is rendered as the
// session's plugin renders it, and no card may show another project's event
// (dispatch-frame-property.test.ts). The fixture's sha256 is the frames', so the two sides judge
// the same frames, and its passed indices must be this tree's verdicts; `go test ./cmd/listener
// -run TestEdgeFrameVerdictOnGeneratedDispatchFrames -update-dispatch-property` rewrites both and
// keeps the fixture's passed_at_297f438ae2, the verdicts #5192's head 297f438ae2 gave the same
// frames, against which the plugin's side shows its oracle sees the cards it must count.
func TestEdgeFrameVerdictOnGeneratedDispatchFrames(t *testing.T) {
	frames := generatedDispatchFrames()
	digest := dispatchFramesDigest(frames)
	own := contracts.LegacyAgentSubject("ses_v")
	passed := []int{}
	for index, frame := range frames {
		if edgeFrameVerdict(nil, "ses_v", []string{"PRIV"}, own, []byte(frame)) == framePasses() {
			passed = append(passed, index)
		}
	}
	data, err := os.ReadFile(dispatchPropertyFixture)
	if err != nil && !(*updateDispatchProperty && os.IsNotExist(err)) {
		t.Fatalf("read the property fixture: %v", err)
	}
	fixture := map[string]json.RawMessage{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &fixture); err != nil {
			t.Fatalf("decode the property fixture: %v", err)
		}
	}
	if *updateDispatchProperty {
		fixture["seed"] = json.RawMessage(`[5192,11]`)
		fixture["frames"] = json.RawMessage(`100000`)
		fixture["sha256"] = json.RawMessage(`"` + digest + `"`)
		indices, err := json.Marshal(passed)
		if err != nil {
			t.Fatal(err)
		}
		fixture["passed"] = indices
		out, err := json.Marshal(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dispatchPropertyFixture, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s: %d frames, sha256 %s, %d passed", dispatchPropertyFixture, len(frames), digest, len(passed))
		return
	}
	var recorded struct {
		SHA256 string `json:"sha256"`
		Passed []int  `json:"passed"`
	}
	if err := json.Unmarshal(data, &recorded); err != nil {
		t.Fatalf("decode the property fixture: %v", err)
	}
	if recorded.SHA256 != digest {
		t.Fatalf("the generated frames' sha256 is %s; the fixture records %s: the generator changed without the fixture", digest, recorded.SHA256)
	}
	if !slices.Equal(recorded.Passed, passed) {
		t.Fatalf("the edge passes %d of the generated frames; the fixture records %d: rerun with -update-dispatch-property and read the plugin's side again",
			len(passed), len(recorded.Passed))
	}
	if len(passed) == 0 {
		t.Fatalf("the edge passes none of the %d generated frames: the property would judge nothing", len(frames))
	}
}
