package projectaccess_test

import (
	"errors"
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
	"github.com/sjawhar/envoy/internal/cipher/projectaccess/projectaccesstest"
)

// TestDeveloperBoxAsOfRefusesAPrivateOrClosingProject: Dispatch's closing state reaches Answer
// from its own key, and a project whose answer carries none is not closing. DeveloperBoxAsOf
// passes a list of public projects its person sees, and refuses, naming it, the first that is
// private or closing; a project its person cannot see is refused as Visible refuses it, never
// named as closed. Visible itself is unchanged: it passes both.
func TestDeveloperBoxAsOfRefusesAPrivateOrClosingProject(t *testing.T) {
	const carol, alice = "carol@example.com", "alice@example.com"
	client := clientOf(projectaccesstest.NewServer(t, map[string]projectaccesstest.Row{
		"PRIV":  {Visibility: "private", Owner: carol},
		"PUB":   {Visibility: "public", Owner: carol},
		"CLOSE": {Visibility: "public", Owner: carol, Closing: true},
	}, map[string]string{carol: projectaccess.MemberFull}))

	closing, err := client.Access(t.Context(), "CLOSE", carol)
	if err != nil {
		t.Fatal(err)
	}
	public, err := client.Access(t.Context(), "PUB", carol)
	if err != nil {
		t.Fatal(err)
	}
	if !closing.Closing || !closing.Public() || !closing.Closed() || public.Closing || public.Closed() {
		t.Fatalf("Access(CLOSE) = %+v and Access(PUB) = %+v, want CLOSE public and closing, PUB neither", closing, public)
	}

	if asked, err := projectaccess.DeveloperBoxAsOf(t.Context(), client, carol, []string{"PUB"}); err != nil || asked.IsZero() {
		t.Fatalf("DeveloperBoxAsOf(carol, PUB) = %v, %v; want a pass and the time its answer was asked", asked, err)
	}
	for _, c := range []struct {
		person   string
		projects []string
		closed   string
	}{
		{carol, []string{"PUB", "CLOSE"}, "CLOSE"},
		{carol, []string{"PRIV", "PUB"}, "PRIV"},
	} {
		_, err := projectaccess.DeveloperBoxAsOf(t.Context(), client, c.person, c.projects)
		var refused *projectaccess.ClosedError
		if !errors.As(err, &refused) || refused.Project != c.closed || !errors.Is(err, projectaccess.ErrClosed) {
			t.Fatalf("DeveloperBoxAsOf(%s, %v) = %v, want ErrClosed naming %s", c.person, c.projects, err, c.closed)
		}
	}
	_, err = projectaccess.DeveloperBoxAsOf(t.Context(), client, alice, []string{"PRIV"})
	var unseen *projectaccess.NotVisibleError
	if !errors.As(err, &unseen) || unseen.Project != "PRIV" || errors.Is(err, projectaccess.ErrClosed) {
		t.Fatalf("DeveloperBoxAsOf(alice, PRIV) = %v, want ErrNotVisible naming PRIV and not ErrClosed", err)
	}
	if err := projectaccess.Visible(t.Context(), client, carol, []string{"CLOSE", "PRIV"}); err != nil {
		t.Fatalf("Visible(carol, CLOSE PRIV) = %v, want a pass: closing is no question of sight", err)
	}
}
