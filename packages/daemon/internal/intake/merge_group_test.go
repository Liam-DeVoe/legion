package intake

import (
	"encoding/json"
	"maps"
	"reflect"
	"testing"
)

// A merge_group event, as Envoy's listener publishes it on the repository's merge_group subject,
// decodes to the MergeGroup of the pull request its head_ref names (the payload's number, which
// Envoy reads from that ref), at the group's own commit, with GitHub's action: checks_requested
// and destroyed alike, since the handler decides what each asks. One that names no pull request
// (Envoy leaves number out where the ref names none), no action or no commit is no fact, and so is
// one of a repository the daemon does not hear.
func TestLegionVerdictMergeGroupDecodesToThePullRequestItsRefNames(t *testing.T) {
	const subject = "notifications.github.sjawhar.legion.merge_group"
	group := map[string]string{
		"kind": "merge_group", "action": "checks_requested", "repo": "sjawhar/legion",
		"head_sha": "9e1c7d2a4b6f8e0c1d3a5b7c9e1f2a4b6c8d0e2f", "head_ref": "refs/heads/gh-readonly-queue/main/pr-1050-7f60bbfb57cb2a1d0e9f8a7b6c5d4e3f2a1b0c9d",
		"base_ref": "refs/heads/main", "number": "1050",
	}
	with := func(key, value string) map[string]string {
		payload := maps.Clone(group)
		if value == "" {
			delete(payload, key)
		} else {
			payload[key] = value
		}
		return payload
	}
	requested := MergeGroup{Repo: "sjawhar/legion", Number: 1050, Action: "checks_requested", HeadSHA: "9e1c7d2a4b6f8e0c1d3a5b7c9e1f2a4b6c8d0e2f"}
	destroyed := requested
	destroyed.Action = "destroyed"
	for _, tc := range []struct {
		name    string
		payload map[string]string
		want    Fact
	}{
		{"checks_requested", group, requested},
		{"destroyed", with("action", "destroyed"), destroyed},
		{"a ref that names no pull request", with("number", ""), nil},
		{"no action", with("action", ""), nil},
		{"no commit", with("head_sha", ""), nil},
		{"a repository the daemon does not hear", with("repo", "acme/widgets"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodeMessage(subject, "CAPTURE", capturedRepositories, envelopeJSON(t, "merge-group", "github", string(payload)))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !reflect.DeepEqual(got.Fact, tc.want) {
				t.Fatalf("fact = %#v, want %#v", got.Fact, tc.want)
			}
		})
	}
}
