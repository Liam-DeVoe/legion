package dispatch

import "testing"

// A document of an issue is named as Dispatch names it before a block's `#b-`: the issue's primary
// document is `dispatch://<issue>/spec`, whatever its slug, and any other document is
// `dispatch://<issue>/artifact/<slug>`. The fault it catches is the primary document named by its
// slug, which no reference Dispatch writes holds (its control: DocumentRef giving the artifact form
// for the primary document, which fails the first row).
func TestDocumentRefNamesThePrimaryDocumentAsTheSpec(t *testing.T) {
	for _, tc := range []struct {
		name    string
		slug    string
		primary bool
		want    string
	}{
		{name: "the primary document", slug: "spec-md", primary: true, want: "dispatch://LEGION-208/spec"},
		{name: "the plan, a document of its own", slug: "plan-md", want: "dispatch://LEGION-208/artifact/plan-md"},
		{name: "the verification document", slug: VerificationSlug, want: "dispatch://LEGION-208/artifact/verification-md"},
	} {
		if got := DocumentRef("LEGION-208", tc.slug, tc.primary); got != tc.want {
			t.Errorf("DocumentRef of %s (slug %s, primary %t) = %q, want %q", tc.name, tc.slug, tc.primary, got, tc.want)
		}
	}
}
