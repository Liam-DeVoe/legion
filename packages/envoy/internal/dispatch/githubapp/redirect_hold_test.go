package githubapp

import (
	"net/url"
	"testing"
)

// TestAHeldReadFollowsOnlyWhereGitHubAnsweredTheRepository: a read held to a repository
// (HoldToRepository) follows a redirect to the repository's own name, or to the location GitHub
// answered the repository's id read at, whatever that location's form, and nowhere else. GitHub's
// REST guide asks a client to follow the Location it is given and not to predict a URL's structure,
// so the hold predicts none: a location by id and one by new name are each followed when GitHub
// gave them, and neither when it did not. Control: predict /repositories/<id> (the hold's earlier
// form), and the by-name location fails.
func TestAHeldReadFollowsOnlyWhereGitHubAnsweredTheRepository(t *testing.T) {
	parse := func(raw string) *url.URL {
		t.Helper()
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
		return parsed
	}
	sent := parse("https://api.github.com/repos/acme/privmoved/pulls/1")
	for _, row := range []struct {
		name     string
		location string
		target   string
		follows  bool
	}{
		{"the name the read spelled", "", "https://api.github.com/repos/ACME/PrivMoved/pulls/1", true},
		{"a location by new name GitHub gave", "https://api.github.com/repos/other/privmoved", "https://api.github.com/repos/other/privmoved/pulls/1", true},
		{"a location by id GitHub gave", "https://api.github.com/repositories/9002", "https://api.github.com/repositories/9002/pulls/1", true},
		{"the location itself", "https://api.github.com/repositories/9002", "https://api.github.com/repositories/9002", true},
		{"a location by id GitHub did not give", "https://api.github.com/repos/other/privmoved", "https://api.github.com/repositories/9002/pulls/1", false},
		{"a location by id where GitHub gave none", "", "https://api.github.com/repositories/9002/pulls/1", false},
		{"another repository whose id the given one prefixes", "https://api.github.com/repositories/9002", "https://api.github.com/repositories/90021/pulls/1", false},
		{"another repository whose name the given one prefixes", "https://api.github.com/repos/other/privmoved", "https://api.github.com/repos/other/privmovedx/pulls/1", false},
		{"the given location on another host", "https://api.github.com/repositories/9002", "https://evil.example/repositories/9002/pulls/1", false},
		{"a location whose path is the root", "https://api.github.com/", "https://api.github.com/repos/other/anything/pulls/1", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			held := heldRepository{owner: "acme", repo: "privmoved"}
			if row.location != "" {
				held.location = parse(row.location)
			}
			if got := sameRepository(sent, parse(row.target), held); got != row.follows {
				t.Errorf("a read of %s held with location %q follows a redirect to %s: %t, want %t", sent, row.location, row.target, got, row.follows)
			}
		})
	}
}
