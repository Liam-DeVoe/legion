package store

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// ClaimSessionNamesTx answers every name claim_sessions holds for a claim in the claim's own
// project, each once, in byte order: the names of each of its kinds, and not a name another claim
// holds, nor one held under the same token in another project, whose daemon serves another claim.
// A claim the store does not hold answers none.
func TestClaimSessionNamesAreTheClaimsHoldsInItsProject(t *testing.T) {
	store := migratedStore(t)
	ctx := context.Background()
	const (
		reviewer claim.Token = "legion-acme-acme-1-reviewer"
		other    claim.Token = "legion-acme-acme-1-implementer"
		unknown  claim.Token = "legion-acme-acme-2-reviewer"
	)
	if err := store.Tx(ctx, func(tx pgx.Tx) error {
		for _, token := range []claim.Token{reviewer, other} {
			if _, err := tx.Exec(ctx, `insert into claims (token, project, tree, issue, role, generation, session, session_file,
				state, launch_failures, prompt_failures, prompt_retires, uncertain_streak, updated_at)
				values ($1, 'legion', 'ACME-1', 'ACME-1', 'reviewer', 1, '', '', 'ready', 0, 0, 0, 0, now())`, string(token)); err != nil {
				return err
			}
		}
		for _, hold := range [][4]string{
			{"legion", "session", "ses-b", string(reviewer)},
			{"legion", "file-id", "ses-b", string(reviewer)},
			{"legion", "session", "ses-a", string(reviewer)},
			{"legion", "file-name", "2026-11-22T00-00-00_ses-a.jsonl", string(reviewer)},
			{"legion", "session", "ses-implementer", string(other)},
			{"another", "session", "ses-elsewhere", string(reviewer)},
			{"legion", "session", "ses-unknown", string(unknown)},
		} {
			if _, err := tx.Exec(ctx, `insert into claim_sessions (project, kind, name, claim) values ($1, $2, $3, $4)`,
				hold[0], hold[1], hold[2], hold[3]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed the claims and their holds: %v", err)
	}
	for _, tc := range []struct {
		token claim.Token
		want  []string
	}{
		{reviewer, []string{"2026-11-22T00-00-00_ses-a.jsonl", "ses-a", "ses-b"}},
		{other, []string{"ses-implementer"}},
		{unknown, nil},
		{"", nil},
	} {
		var got []string
		if err := store.Tx(ctx, func(tx pgx.Tx) (err error) {
			got, err = ClaimSessionNamesTx(ctx, tx, tc.token)
			return err
		}); err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("ClaimSessionNamesTx(%q) = %q, %v; want %q", tc.token, got, err, tc.want)
		}
	}
}
