package daemon

import (
	"github.com/sjawhar/legion/daemon/internal/ghbranch"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// testRepositories serves every repository the daemon tests record issues on, each at main. A
// repository it does not name is one no preparation has read GitHub for, which the daemon answers
// as unread (repositories.lookup): GitHub's answer for a repository refuses it, and nothing else
// does.
var testRepositories = servedRepositories{"acme/widgets": "main", "sjawhar/legion": "main"}

type servedRepositories map[string]string

func (r servedRepositories) ResolveRepository(resolved string) (workflow.Resolution, error) {
	if branch, ok := r[resolved]; ok {
		return workflow.Resolution{Repo: resolved, DefaultBranch: branch, GitHubRepo: resolved}, nil
	}
	return workflow.Resolution{Refusal: "GitHub has not been read for " + resolved + " yet", Unread: true}, nil
}

// fill is the tests' seam that fills the repository state with repos, each at main, as a
// preparation that found the App installed on it and GitHub naming it would leave it
// (prepareName), with no read of GitHub: fillRead of each.
func (r *repositories) fill(repos ...ghrepo.Repository) {
	for _, repo := range repos {
		r.fillRead(ghbranch.Repository{Name: repo, DefaultBranch: "main"})
	}
}

// fillRead fills read's entry as a preparation that found the App installed on read.Name and
// GitHub naming it at read.DefaultBranch would: the App answered installed (answerInstalled), so
// the rename rule serves the name (servesRepository), and the read recorded as what an issue
// resolving to it is recorded on (recordRepository), so an admission on it reads GitHub no further
// (lookup).
func (r *repositories) fillRead(read ghbranch.Repository) {
	answerInstalled(r, read.Name.String(), true)
	r.recordRepository(read.Name.String(), read)
}

// answerInstalled keeps in name's entry GitHub's answer that the App is installed on it, or that
// it is not, as one preparation's read of it would (keepRead).
func answerInstalled(repos *repositories, name string, installed bool) {
	repos.keepRead(ghrepo.MustParse(name), appInstallations{app: appInstallation{answered: true, installed: installed}}, "")
}
