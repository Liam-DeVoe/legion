package leakguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// KnivesRegistriesVar is the environment variable that lists the knives registries a push's third
// pass reads (Guard.knives): a JSON list of rows {"repo": "<owner>/<name>", "path": "<file path>",
// "ref": "<branch or tag>"}, each one registry file in a GitHub repository. The first row is the
// registry and each row after it an overlay, as knives' own repos.local.toml is one beside its
// repos.toml. The guard reads it from its base environment (Guard.base); it is exported for the
// daemon, which sets it. Unset or empty, a push has no third pass.
const KnivesRegistriesVar = "LEGION_KNIVES_REGISTRIES"

// knivesRow is one row of KnivesRegistriesVar: the file at path, at ref, in the GitHub repository
// repo. A row can name a private repository, so no message of this package carries a row's repo,
// path or ref; a message names a row by its number (knivesRowName).
type knivesRow struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
	Ref  string `json:"ref"`
}

// knivesRowName is how a message names the row at index i: by its 1-based number alone.
func knivesRowName(i int) string {
	return "knives registry " + strconv.Itoa(i+1) + " of " + KnivesRegistriesVar
}

// knivesEntry is one entry of the merged registries: a table repos.<name> of the first registry,
// with the forbidden terms of every overlay that adds to it after its own.
type knivesEntry struct {
	// name is the entry's table name in the first registry: the name of a public repository,
	// and the one name of an entry a message carries (knivesEntryName).
	name string
	// origin and release are the fork's two remotes as the registry writes them, release empty
	// where the entry names none.
	origin, release string
	// forbidden is the merged list, the first registry's terms first, each as written.
	forbidden []string
}

// knivesRegistryFile is what this pass reads of the first registry: each entry's origin, its
// release and its forbidden list. Every other field knives writes (upstream, base, consumers, a
// [trust] table) is passed over, where knives itself refuses a field it does not know: this pass
// reads three fields of the file and needs no opinion on the rest.
type knivesRegistryFile struct {
	Repos map[string]struct {
		Origin    string   `toml:"origin"`
		Release   string   `toml:"release"`
		Forbidden []string `toml:"forbidden"`
	} `toml:"repos"`
}

// knivesOverlayFile is what this pass reads of an overlay: each entry's forbidden list.
type knivesOverlayFile struct {
	Repos map[string]struct {
		Forbidden []string `toml:"forbidden"`
	} `toml:"repos"`
}

// knives is a push's third pass: the forbidden terms of every knives registry entry that names
// the push's destination, matched over the records of the files the push adds as `knives audit`
// matches them (knivesLines). It is the knives audit an agent was asked to run before each push to
// a fork, made into a pass of the push itself (Guard.Push says why).
//
// Every row is read at every push the pass runs for (Guard.Push: one to a destination GitHub
// reports public, or to one it cannot name), with nothing kept from one push to the next, and
// before the destination is matched against the entries: a registry that cannot be read refuses
// such a push, whether or not an entry names its destination. A push whose destination no entry
// names passes. A union of every entry's terms would refuse the local bare pushes of this
// repository's own tests and of knives' ledger rehearsals, which hold those terms by design.
func (g *Guard) knives(ctx context.Context, what string, req PushRequest, records []Record) error {
	listed := value(g.base(), KnivesRegistriesVar)
	if listed == "" {
		return nil
	}
	const unread = "the knives forbidden lists could not be read"
	rows, err := knivesRows(listed)
	if err != nil {
		return &SetupFailure{What: what, Why: unread, Err: err}
	}
	entries, err := g.knivesEntries(ctx, rows)
	if err != nil {
		return &SetupFailure{What: what, Why: unread, Err: err}
	}
	repos, err := g.destinationRepos(ctx, req)
	if err != nil {
		return &SetupFailure{What: what, Why: "the push's remote URLs could not be read", Err: err}
	}
	lines := knivesLines(knivesApplying(entries, repos), records)
	if len(lines) == 0 {
		return nil
	}
	return &Refusal{What: what, By: byKnives, Lines: lines}
}

// knivesRows are the rows listed holds. A value of any other shape is an error, never a list read
// as empty: not one JSON list, a row with a field more, a row with a field missing or empty, or a
// repo that is no <owner>/<name> ghrepo.Parse takes. No error carries the value or the decoder's
// message, which can quote it.
func knivesRows(listed string) ([]knivesRow, error) {
	notRows := errors.New(KnivesRegistriesVar + ` is not a JSON list of {"repo", "path", "ref"} rows`)
	decoder := json.NewDecoder(strings.NewReader(listed))
	decoder.DisallowUnknownFields()
	// A pointer, so that a JSON null, which decodes into a nil slice, is told from a list.
	var rows *[]knivesRow
	if err := decoder.Decode(&rows); err != nil || rows == nil {
		return nil, notRows
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, notRows
	}
	for i, row := range *rows {
		for _, field := range [][2]string{{"repo", row.Repo}, {"path", row.Path}, {"ref", row.Ref}} {
			if field[1] == "" {
				return nil, fmt.Errorf("%s names no %s", knivesRowName(i), field[0])
			}
		}
		if _, err := ghrepo.Parse("repo", row.Repo); err != nil {
			return nil, fmt.Errorf("%s names a repo that is not <owner>/<name>", knivesRowName(i))
		}
	}
	return *rows, nil
}

// knivesEntries are the entries of the registries rows name, in name order, merged as knives
// v4.0.1 merges its registry and its overlay (src/config.rs, load and apply_local_overlay): the
// first row is the registry, whose tables repos.<name> are the entries, and each row after it an
// overlay whose tables may name only an entry an earlier row has, each adding its forbidden terms
// to the end of that entry's list. After each row, each list the row wrote is checked as knives
// checks one (knivesChecked). An entry of the first row must name its origin, which knives requires
// as well. A file that is not TOML of that shape is an error, never a registry read as empty.
//
// The files are read at once, one gh each, and merged in row order after the last read ends: a
// read takes about as long as a push's other passes together (measured 2026-11-17 in an issue
// pod, 5 pushes each to a local bare: a median of 5.12 s with two rows read one after the other,
// 3.45 s with none), and the reads depend on nothing of one another.
func (g *Guard) knivesEntries(ctx context.Context, rows []knivesRow) ([]knivesEntry, error) {
	files := make([]string, len(rows))
	failures := make([]error, len(rows))
	var reads sync.WaitGroup
	for i, row := range rows {
		reads.Go(func() { files[i], failures[i] = g.knivesRegistry(ctx, i, row) })
	}
	reads.Wait()
	if err := errors.Join(failures...); err != nil {
		return nil, err
	}
	entries := map[string]*knivesEntry{}
	for i, file := range files {
		if i == 0 {
			var registry knivesRegistryFile
			if err := toml.Unmarshal([]byte(file), &registry); err != nil {
				return nil, notKnivesTOML(i, err)
			}
			for _, name := range slices.Sorted(maps.Keys(registry.Repos)) {
				read := registry.Repos[name]
				entry := &knivesEntry{name: name, origin: read.Origin, release: read.Release, forbidden: read.Forbidden}
				if entry.origin == "" {
					return nil, fmt.Errorf("%s: %s names no origin", knivesRowName(i), knivesEntryName(name, knivesTerms(entry.forbidden)))
				}
				if err := knivesChecked(i, entry); err != nil {
					return nil, err
				}
				entries[name] = entry
			}
			continue
		}
		var overlay knivesOverlayFile
		if err := toml.Unmarshal([]byte(file), &overlay); err != nil {
			return nil, notKnivesTOML(i, err)
		}
		for _, name := range slices.Sorted(maps.Keys(overlay.Repos)) {
			entry, ok := entries[name]
			if !ok {
				// The overlay's own table name is not printed: an overlay is the private half.
				return nil, fmt.Errorf("%s adds to an entry no earlier registry has; an overlay only adds to an entry the registry already has", knivesRowName(i))
			}
			entry.forbidden = append(entry.forbidden, overlay.Repos[name].Forbidden...)
			if err := knivesChecked(i, entry); err != nil {
				return nil, err
			}
		}
	}
	merged := make([]knivesEntry, 0, len(entries))
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		merged = append(merged, *entries[name])
	}
	return merged, nil
}

// knivesRegistry is the file of the row at index i, as GitHub serves a file's raw content:
//
//	gh api -H 'Accept: application/vnd.github.raw' repos/<repo>/contents/<path>?ref=<ref>
//
// with gh found on the base environment's PATH, as every gh of the guard is (Guard.run), and the
// names ghPlainEnv adds: GH_HOST names github.com for a gh with no configuration of its own to
// read it from, and NO_COLOR=1 with CLICOLOR_FORCE=0 keep what gh prints the file's own bytes. A
// gh that exited non-zero, could not be started or was stopped by the caller's bound is an error,
// and the error names the row by its number and gh's status alone: gh's standard error is never
// read, and its standard output is read only as a file it answered with 0, because what gh prints
// of a failure names the read, and a row can name a private repository.
func (g *Guard) knivesRegistry(ctx context.Context, i int, row knivesRow) (string, error) {
	// The path and the ref are escaped where they are written into the endpoint, so that a `?`,
	// `#` or `%` of a file's name, or an `&` or `+` of a ref, reads the file the row names.
	path := strings.ReplaceAll(url.PathEscape(row.Path), "%2F", "/")
	status, file, err := g.run(ctx, child{
		argv:   []string{"gh", "api", "-H", "Accept: application/vnd.github.raw", "repos/" + row.Repo + "/contents/" + path + "?ref=" + url.QueryEscape(row.Ref)},
		env:    ghPlainEnv(),
		stderr: io.Discard,
	})
	if err != nil {
		return "", fmt.Errorf("%s: %w", knivesRowName(i), err)
	}
	if status != 0 {
		return "", fmt.Errorf("%s: gh exited %d", knivesRowName(i), status)
	}
	return file, nil
}

// notKnivesTOML is the error of the registry file of the row at index i that go-toml could not
// decode into the shape knives writes. It names the line where go-toml says one, and never carries
// go-toml's own message, which can quote a key or a character of the file.
func notKnivesTOML(i int, err error) error {
	why := knivesRowName(i) + " is not TOML of [repos.<name>] tables with origin and release strings and a forbidden list of strings"
	var decoding *toml.DecodeError
	if errors.As(err, &decoding) {
		line, _ := decoding.Position()
		why += fmt.Sprintf(" (line %d)", line)
	}
	return errors.New(why)
}

// knivesChecked is entry's merged list checked as knives checks one after each file
// (checked_forbidden): a term blank once trimmed is a substring of every line, and a term listed
// twice, case aside, would report each of its hits twice. The error names the row after which the
// list is wrong and the entry (knivesEntryName), and never the term.
func knivesChecked(i int, entry *knivesEntry) error {
	seen := map[string]bool{}
	for _, term := range entry.forbidden {
		if strings.TrimSpace(term) == "" {
			return fmt.Errorf("%s: the forbidden list of %s holds an empty term", knivesRowName(i), knivesEntryName(entry.name, knivesTerms(entry.forbidden)))
		}
		lowered := strings.ToLower(term)
		if seen[lowered] {
			return fmt.Errorf("%s: the forbidden list of %s lists a term twice, case aside", knivesRowName(i), knivesEntryName(entry.name, knivesTerms(entry.forbidden)))
		}
		seen[lowered] = true
	}
	return nil
}

// destinationRepos are the repositories a push's destination is named as, each read with
// RepoFromURL, where a URL that names no repository on github.com names none. Three things name it:
//
//   - the URL git hands the pre-push hook, after its insteadOf and pushInsteadOf rewriting. With
//     url.<bare>.pushInsteadOf set, git hands the hook origin and the local bare path, and jj git
//     push hands the same (measured 2026-11-17 in an issue pod), so the URL alone can name nothing
//     on GitHub where the remote does;
//   - the remote, where it is itself a URL: `git push <url>` hands the hook the URL as typed;
//   - and where the remote is a name, the URLs its configuration pushes to, as written there and
//     before any rewriting: remote.<name>.pushurl, and where none is set remote.<name>.url. A
//     URL or path that names no GitHub repository is read as a name too, and no remote is called
//     that, so its reads answer nothing.
func (g *Guard) destinationRepos(ctx context.Context, req PushRequest) ([]string, error) {
	var repos []string
	add := func(remote string) {
		if repo := RepoFromURL(remote); repo != "" {
			repos = append(repos, repo)
		}
	}
	add(req.URL)
	if req.Remote == "" {
		return repos, nil
	}
	if RepoFromURL(req.Remote) != "" {
		add(req.Remote)
		return repos, nil
	}
	urls, err := g.remoteURLs(ctx, req.Dir, req.Remote, "pushurl")
	if err != nil {
		return nil, err
	}
	if len(urls) == 0 {
		if urls, err = g.remoteURLs(ctx, req.Dir, req.Remote, "url"); err != nil {
			return nil, err
		}
	}
	for _, remote := range urls {
		add(remote)
	}
	return repos, nil
}

// remoteURLs are the values of remote.<remote>.<key> in dir's configuration, one a line, and none
// where the key is not set: git config answers 1 for a key it does not hold, and for a key it
// cannot read, which no remote has either. Any other status is an error.
func (g *Guard) remoteURLs(ctx context.Context, dir, remote, key string) ([]string, error) {
	var stderr strings.Builder
	status, out, err := g.run(ctx, child{argv: gitArgv("config", "--get-all", "remote."+remote+"."+key), dir: dir, stderr: &stderr})
	if err != nil {
		return nil, err
	}
	switch status {
	case 0:
	case 1:
		return nil, nil
	default:
		return nil, fmt.Errorf("git config exited %d: %s", status, strings.TrimSpace(stderr.String()))
	}
	var urls []string
	for line := range strings.Lines(out) {
		if remote := strings.TrimRight(line, "\r\n"); remote != "" {
			urls = append(urls, remote)
		}
	}
	return urls, nil
}

// knivesApplying are the entries whose origin or release, read with RepoFromURL, is one of repos,
// case aside.
func knivesApplying(entries []knivesEntry, repos []string) []knivesEntry {
	named := func(remote string) bool {
		repo := RepoFromURL(remote)
		return repo != "" && slices.ContainsFunc(repos, func(destination string) bool { return strings.EqualFold(destination, repo) })
	}
	var applying []knivesEntry
	for _, entry := range entries {
		if named(entry.origin) || named(entry.release) {
			applying = append(applying, entry)
		}
	}
	return applying
}

// knivesLines is the knives pass's report of records against entries: one line for each record of
// a file and each term of an entry's list that the record holds, as knives v4.0.1 src/forbidden.rs
// scan matches one, the same line once, in record order and then term order. A record of a line
// is "<file>:<line>: forbidden term <n>", a record of a path "<file>: the path: forbidden term
// <n>", n the term's 1-based number in its entry's merged list, which holds no blank term
// (knivesChecked); where two entries apply, each line also names the entry, " of
// [repos.<name>]". The file is masked (knivesMask), and no line carries a term: a term is
// private, which is why it is listed.
//
// A record holds a term as scan finds one: both lowered, the term a substring of the text. Rust's
// to_lowercase, which scan lowers with, differs from strings.ToLower in two places: it lowers a
// capital sigma that ends a word to ς, where strings.ToLower lowers every capital sigma to σ, and
// it lowers U+0130 (İ) to i followed by U+0307, where strings.ToLower lowers it to i.
func knivesLines(entries []knivesEntry, records []Record) []string {
	terms := make([][]string, len(entries))
	var every []string
	for e, entry := range entries {
		terms[e] = knivesTerms(entry.forbidden)
		every = append(every, terms[e]...)
	}
	if len(every) == 0 {
		return nil
	}
	var lines []string
	seen := map[string]bool{}
	masked := map[string]string{}
	for _, record := range records {
		if record.File == "" {
			continue
		}
		text := strings.ToLower(record.Text)
		for e, of := range terms {
			for n, term := range of {
				if !strings.Contains(text, term) {
					continue
				}
				file, ok := masked[record.File]
				if !ok {
					file = knivesMask(record.File, every)
					masked[record.File] = file
				}
				line := file + ":" + strconv.Itoa(record.Line) + ": forbidden term " + strconv.Itoa(n+1)
				if record.Line == 0 {
					line = file + ": the path: forbidden term " + strconv.Itoa(n+1)
				}
				if len(entries) > 1 {
					line += " of " + knivesEntryName(entries[e].name, every)
				}
				if !seen[line] {
					seen[line] = true
					lines = append(lines, line)
				}
			}
		}
	}
	return lines
}

// knivesMask is path as the knives pass's report names it: each slash-separated component that
// holds one of terms (lowered, knivesTerms), case aside, written "<forbidden>", and each other one
// masked as the rule pass masks a path (maskPath). A term with a slash in it can stand across two
// components, and a path in which one still stands is written "<forbidden>" whole.
func knivesMask(path string, terms []string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if knivesHolds(part, terms) {
			parts[i] = "<forbidden>"
		} else {
			parts[i] = maskPath(part)
		}
	}
	masked := strings.Join(parts, "/")
	if knivesHolds(masked, terms) {
		return "<forbidden>"
	}
	return masked
}

// knivesEntryName is how a message names an entry: its table, "[repos.<name>]", with the name
// written "<forbidden>" where it holds one of terms (lowered, knivesTerms), case aside.
func knivesEntryName(name string, terms []string) string {
	if knivesHolds(name, terms) {
		name = "<forbidden>"
	}
	return "[repos." + name + "]"
}

// knivesTerms are terms lowered as the scan lowers them, less any that is blank once trimmed,
// which a substring check finds in every text and the registry check refuses (knivesChecked).
func knivesTerms(terms []string) []string {
	var lowered []string
	for _, term := range terms {
		if strings.TrimSpace(term) != "" {
			lowered = append(lowered, strings.ToLower(term))
		}
	}
	return lowered
}

// knivesHolds is whether text holds one of terms, which are lowered already, case aside.
func knivesHolds(text string, terms []string) bool {
	lowered := strings.ToLower(text)
	return slices.ContainsFunc(terms, func(term string) bool { return strings.Contains(lowered, term) })
}
