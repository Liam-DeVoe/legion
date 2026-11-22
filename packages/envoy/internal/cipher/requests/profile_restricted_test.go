package requests_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/policy"
	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
	"github.com/sjawhar/envoy/internal/cipher/projectaccess/projectaccesstest"
)

// restrictedProfileFixture is newProfileFixture with Dispatch answering memberKind for the
// session's person: APP_KEY a human-tier profile GitHub serves, so a request a person decides
// gets the check mint, and OTHER_APP_KEY a profile the file refuses, its github block naming an
// account its source's github-owners tag does not.
func restrictedProfileFixture(t *testing.T, memberKind string) *profileFixture {
	t.Helper()
	f := newProfileFixture(t, map[string]int64{standInOwner: 7}, map[string]string{
		"APP_KEY":       `{"accounts": ["ada"], "repositories": ["ada/proofs"], "permissions": {"contents": "read", "metadata": "read"}}`,
		"OTHER_APP_KEY": `{"accounts": ["nobody-listed"], "permissions": {"metadata": "read"}}`,
	})
	dispatch := projectaccesstest.NewServer(t, nil, map[string]string{checkOperator: memberKind})
	f.Projects = projectaccess.New(dispatch.URL, dispatch.Account, nil)
	return f
}

// TestARestrictedMembersProfileRequestIsDeniedBeforeAnyCheckMintOrProfileAnswer: a session whose
// person Dispatch calls a restricted member asks for a human-tier GitHub profile, which a full
// member's request would put to its owner after the check mint, and for a profile the profiles
// file refuses. Each is denied restricted-member, with no approval record and no token request to
// GitHub: the check mint runs only for a request the policy sends to a person, and the policy
// sends a restricted member's to nobody. The refused profile is told restricted-member too, never
// the file's refusal, which would name its source and why it fails. The control is a full member
// of the same fixture, whose request for the profile is pending after one check mint and whose
// request for the refused profile is PROFILE_REFUSED.
func TestARestrictedMembersProfileRequestIsDeniedBeforeAnyCheckMintOrProfileAnswer(t *testing.T) {
	t.Run("the control: a full member", func(t *testing.T) {
		f := restrictedProfileFixture(t, projectaccess.MemberFull)
		if req, err := f.create(t, "APP_KEY"); err != nil || req.State != "pending" || req.RecordID == nil {
			t.Fatalf("Create(APP_KEY) for a full member = %+v, %v; want it pending on its approver", req, err)
		}
		if asked := f.stand.tokenRequests(); len(asked) != 1 {
			t.Fatalf("a full member's request made %d token requests; want the one check mint", len(asked))
		}
		if req, err := f.create(t, "OTHER_APP_KEY"); !errors.Is(err, policy.ErrProfileRefused) {
			t.Fatalf("Create(OTHER_APP_KEY) for a full member = %+v, %v; want ErrProfileRefused", req, err)
		}
	})
	t.Run("a restricted member", func(t *testing.T) {
		f := restrictedProfileFixture(t, projectaccess.MemberRestricted)
		for _, name := range []string{"APP_KEY", "OTHER_APP_KEY"} {
			req, err := f.create(t, name)
			if err != nil || req.State != "denied" || req.RecordID != nil || req.GrantID != nil || req.Detail == nil ||
				!strings.Contains(*req.Detail, name+": "+policy.ReasonRestrictedMember) || strings.Contains(*req.Detail, "profile") {
				t.Fatalf("Create(%s) for a restricted member = %+v, %v; want it denied naming %s and no profile", name, req, err, policy.ReasonRestrictedMember)
			}
		}
		if asked := f.stand.tokenRequests(); len(asked) != 0 {
			t.Fatalf("a restricted member's requests made %d token requests to GitHub; want none", len(asked))
		}
	})
}
