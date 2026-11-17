package leakguard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// KnivesRegistriesVar is the environment variable that lists the knives registries a push's third
// pass reads (Guard.knives), and its rule pass too where it would refuse the push (Guard.Push): a
// JSON list of rows {"repo": "<owner>/<name>", "path": "<file path>", "ref": "<branch or tag>"},
// each one registry file in a GitHub repository. The first row is the registry and each row after
// it an overlay, as knives' own repos.local.toml is one beside its repos.toml. The guard reads it
// from its base environment (Guard.base); it is exported for the daemon, which sets it. Unset or
// empty, a push has no third pass, and no upstream's commit is held for its rules.
const KnivesRegistriesVar = "LEGION_KNIVES_REGISTRIES"

// KnivesRegistry is one row of KnivesRegistriesVar: the file at Path, at Ref, in the GitHub
// repository Repo. It is the one type of the row at both ends of the variable: the daemon settles
// legion.yaml's leak_guard.knives_registries into these rows (config.LeakGuard) and writes them
// with KnivesRegistriesValue, and the guard reads them back with knivesRows, whose decoder refuses
// a field it does not know. A row can name a private repository, so no message of this package
// carries a row's repo, path or ref; a message names a row by its number (knivesRowName).
type KnivesRegistry struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
	Ref  string `json:"ref"`
}

// knivesRowName is how a message names the row at index i: by its 1-based number alone.
func knivesRowName(i int) string {
	return "knives registry " + strconv.Itoa(i+1) + " of " + KnivesRegistriesVar
}

// knivesEntry is one entry of the merged registries: a table repos.<name> of the first registry,
// with the forbidden terms of every overlay that adds to it after its own. Its JSON is the entry as
// the kept list's file holds it (knivesKept), so the order and the names of its fields are that
// file's.
type knivesEntry struct {
	// Name is the entry's table name in the first registry: the name of a public repository,
	// and the one name of an entry a message carries (knivesEntryName).
	Name string `json:"name"`
	// Origin and Release are the fork's two remotes as the registry writes them, Release empty
	// where the entry names none.
	Origin  string `json:"origin"`
	Release string `json:"release"`
	// Upstream is the repository the fork is of, as the registry writes it, empty where the entry
	// names none. A commit its branches or tags reach is read by neither the knives pass nor the
	// rule pass (knivesRead).
	Upstream string `json:"upstream"`
	// Forbidden is the merged list, the first registry's terms first, each as written.
	Forbidden []string `json:"forbidden"`
}

// knivesRegistryFile is what this pass reads of the first registry: each entry's origin, its
// release, its upstream and its forbidden list. Every other field knives writes (base, consumers,
// a [trust] table) is passed over, where knives itself refuses a field it does not know: this pass
// reads four fields of the file and needs no opinion on the rest. The three remotes are decoded as
// whatever the file holds and read as strings entry by entry (knivesEntries), so that a remote
// that is not a string is refused naming its entry and field, where a string field would fail the
// whole decode naming neither (notKnivesTOML).
type knivesRegistryFile struct {
	Repos map[string]struct {
		Origin    any      `toml:"origin"`
		Release   any      `toml:"release"`
		Upstream  any      `toml:"upstream"`
		Forbidden []string `toml:"forbidden"`
	} `toml:"repos"`
}

// knivesOverlayFile is what this pass reads of an overlay: each entry's forbidden list.
type knivesOverlayFile struct {
	Repos map[string]struct {
		Forbidden []string `toml:"forbidden"`
	} `toml:"repos"`
}

// knivesRead is what the rule pass and the knives pass of one push read of the knives registries
// (Guard.Push): named, the entries that name the push's destination as a fork
// (Guard.knivesNaming), and upstreamHeld, the commits of the push's records that a branch or a tag
// of an applying entry's upstream reaches (Guard.upstreamHeld). Each is read the first time a pass
// asks for it and kept for the other, so a push reads each registry and each upstream URL at most
// once, and nothing a pass does not ask for. The rule pass asks for the entries only where it
// would refuse the push; the knives pass asks for them for every push it runs on, and with no
// registry listed they are read from nothing. Each pass asks for the upstreams' commits only where
// it would refuse the push without them.
type knivesRead struct {
	named        func() knivesNamed
	upstreamHeld func() (map[string]bool, error)
}

// knivesReadOf is the knivesRead of the push req, whose records are records, its failures naming
// what. It reads nothing until a pass asks.
func (g *Guard) knivesReadOf(ctx context.Context, what string, req PushRequest, records []Record) knivesRead {
	named := sync.OnceValue(func() knivesNamed { return g.knivesNaming(ctx, what, req) })
	return knivesRead{
		named: named,
		upstreamHeld: sync.OnceValues(func() (map[string]bool, error) {
			return g.upstreamHeld(ctx, what, req.Dir, named().applying, records)
		}),
	}
}

// knives is a push's third pass: the forbidden terms of every knives registry entry that names
// the push's destination, matched over the records of the files the push adds as `knives audit`
// matches them (knivesLines). It is the knives audit an agent was asked to run before each push to
// a fork, made into a pass of the push itself (Guard.Push says why).
//
// The pass runs for a push Guard.Push scans (one to a destination GitHub reports public, or to
// one it cannot name), over the entries read names as the destination's (Guard.knivesNaming),
// which the rule pass may have asked for already. Where those could not be told, the pass refuses
// the push with that read's setup failure, or lets it go on, saying that read's notice once
// through Options.Notice, where a read failed and the kept list names no entry for the
// destination. A push whose destination no entry names passes. A union of every entry's terms
// would refuse the local bare pushes of this repository's own tests and of knives' ledger
// rehearsals, which hold those terms by design.
//
// Of the push's commits, the pass reads none that the fork or the upstream of an entry that
// applies holds already. The fork's are in no record: Guard.commits lists no commit that a branch
// or a tag of the push's destination reaches, as the one read of Guard.Push answered, so the
// records the pass is handed hold none of them. Of the rest, it reads none that a branch or a tag
// of such an upstream reaches (Guard.upstreamHeld), read the same way from the entry's upstream
// URL (Guard.advertised), each ref counting where this clone has the commit it is at. A commit the
// fork holds is the fork's own history, and one the upstream holds is upstream's text, not the
// fork's: `knives audit` reads none of it either, since its base for a branch is its fork point
// with upstream's trunk, and a mirror sync of a fork's main, a release cut over a newer upstream
// base and a member rebased onto one all push such commits. No ref of this clone's own says what
// either holds (Guard.Push says why): a cut rehearsed on a local bare repository, whatever the
// remote it was pushed through is named and whatever URL that remote names now, is still the real
// push's own, and the pass reads it (the architect's rule of 2026-11-17). An upstream's ref at a
// commit this clone does not have reaches nothing, so a clone that has not fetched as far as that
// ref reads upstream's lines and refuses those that hold a term (knivesAdvice). The upstreams are
// read only for a push this pass or the rule pass would otherwise refuse, once for both
// (knivesRead), so a push that adds no term and no private name runs no git for them; a read that
// fails refuses the push as a setup failure naming the URL.
func (g *Guard) knives(what string, read knivesRead, records []Record) error {
	named := read.named()
	if named.failure != nil {
		return named.failure
	}
	if named.notice != "" {
		g.notice(named.notice)
		return nil
	}
	lines := knivesLines(named.applying, records)
	if len(lines) == 0 {
		return nil
	}
	// A commit the destination holds is in no record (Guard.commits), so a commit is held here
	// where the refs of the upstream of an entry that applies reach it.
	reached, err := read.upstreamHeld()
	if err != nil {
		return err
	}
	if len(reached) > 0 {
		if lines = knivesLines(named.applying, unheld(records, reached)); len(lines) == 0 {
			return nil
		}
	}
	return &Refusal{What: what, By: byKnives, Lines: lines, Advice: knivesAdvice}
}

// knivesAdvice is what a refusal of the knives pass says after its report (Refusal.Advice). The
// pass reads no commit a branch or a tag of the destination or of the entry's upstream reaches,
// as each reports its refs at the push, and a ref counts only where this clone has the commit it
// is at (Guard.knives): where a refused line is one an upstream wrote, the clone lacks the commit
// upstream's ref is at. It names no repository and no term.
const knivesAdvice = "A commit is not read where a branch or tag of the destination or of the registry entry's upstream reaches it, as each reports its refs at the push; " +
	"a ref at a commit this clone does not have reaches nothing, so an upstream's own line is read where the clone lacks the commit upstream's branch or tag is at."

// knivesNamed is a push's read of the knives registry entries that name its destination as a
// fork (Guard.knivesNaming): applying, those entries (knivesApplying); failure, where they could
// not be told, the setup failure the knives pass refuses the push with; and notice, where a read
// failed and the kept list names no entry for the destination, what the knives pass says as it
// lets the push go on. All three are empty where KnivesRegistriesVar lists no registry. The rule
// pass reads applying alone, so a failure or a notice leaves its refusal standing (Guard.Push).
type knivesNamed struct {
	applying []knivesEntry
	failure  error
	notice   string
}

// knivesNaming reads the knives registry entries that name the push req's destination, its
// failure naming what. It reads nothing where KnivesRegistriesVar lists no registry. It matches
// the destination against the entries of the kept list (knivesKept) where that list was read no
// more than knivesKeptFor ago, reading no row. Otherwise it reads every row, before the
// destination is matched, and a read in which every row was read and every list checked becomes
// the kept list, under the time it was made. A read that fails is never kept. It answers a failure
// where no list is kept, whether or not an entry names the destination. Where one is kept, of any
// age, it answers a failure when an entry of the kept list names the destination, and a notice
// when none does: the kept list is what spares every public push of every role a refusal while a
// registry cannot be read.
func (g *Guard) knivesNaming(ctx context.Context, what string, req PushRequest) knivesNamed {
	listed := value(g.base(), KnivesRegistriesVar)
	if listed == "" {
		return knivesNamed{}
	}
	const unread = "the knives forbidden lists could not be read"
	rows, err := knivesRows(listed)
	if err != nil {
		return knivesNamed{failure: &SetupFailure{What: what, Why: unread, Err: err}}
	}
	now := g.clock().Now()
	kept, held := g.keptKnives(listed)
	var entries []knivesEntry
	// failed is a read that failed while a list is kept: the kept list's entries then decide the
	// push, once its destination is named.
	var failed error
	if held && kept.freshAt(now) {
		entries = kept.Entries
	} else {
		read, err := g.knivesEntries(ctx, rows)
		switch {
		case err == nil:
			entries = read
			g.keepKnives(listed, now, read)
		case !held:
			return knivesNamed{failure: &SetupFailure{What: what, Why: unread, Err: err}}
		default:
			entries, failed = kept.Entries, err
		}
	}
	repos, err := g.destinationRepos(ctx, req)
	if err != nil {
		return knivesNamed{failure: &SetupFailure{What: what, Why: "the push's remote URLs could not be read", Err: err}}
	}
	applying := knivesApplying(entries, repos)
	if failed != nil {
		if len(applying) > 0 {
			return knivesNamed{failure: &SetupFailure{What: what, Why: unread, Err: failed}}
		}
		return knivesNamed{notice: "LEAK CHECK: " + unread + " (" + strings.ReplaceAll(failed.Error(), "\n", "; ") + "); the list read at " +
			kept.ReadAt.UTC().Format(time.RFC3339) + " names no entry for this push's destination, so the push goes on"}
	}
	return knivesNamed{applying: applying}
}

// upstreamHeld are the commits of records that a branch or a tag of the upstream of one of
// applying reaches, as that upstream reports its refs at the push, read in dir once for each
// upstream URL (Guard.advertised), each ref counting where this clone has the commit it is at
// (remoteHeld): the commits neither the rule pass nor the knives pass reads (knivesRead). An entry
// that names no upstream holds nothing. A read of an upstream that fails is a setup failure of
// what naming the upstream's URL, and a rev-list that fails is one too, never a commit read as
// held.
func (g *Guard) upstreamHeld(ctx context.Context, what, dir string, applying []knivesEntry, records []Record) (map[string]bool, error) {
	var holding []string
	upstreams := map[string]bool{}
	for _, entry := range applying {
		if entry.Upstream == "" || upstreams[entry.Upstream] {
			continue
		}
		upstreams[entry.Upstream] = true
		upstream, err := g.advertised(ctx, dir, entry.Upstream)
		if err != nil {
			return nil, &SetupFailure{What: what, Why: "the refs of the registry entry's upstream could not be read", Err: err}
		}
		holding = append(holding, upstream...)
	}
	reached, err := g.remoteHeld(ctx, dir, holding, records)
	if err != nil {
		return nil, &SetupFailure{What: what, Why: "the commits the registry entry's upstream holds could not be read", Err: err}
	}
	return reached, nil
}

// unheld are records less each record of a commit held names (Guard.upstreamHeld): a ref name,
// and any other record of no commit, stays.
func unheld(records []Record, held map[string]bool) []Record {
	return slices.DeleteFunc(slices.Clone(records), func(record Record) bool { return held[record.Commit] })
}

// remoteHeld are the commits of records that one of held reaches, each reaching itself: the
// commits neither the rule pass nor the knives pass reads (Guard.upstreamHeld), held being the
// commits that the branches and tags of an applying entry's upstream are at (Guard.advertised).
// One rev-list answers for all of them, fed on its standard input the records' commits, a line
// each, and then ^<sha> for each of held:
//
//	git rev-list --stdin
//
// which is `git rev-list <commits...> --not <held...>` with the shas off the command line, since a
// repository can advertise thousands of refs. It prints every fed commit that none of held
// reaches, with any ancestor of one that none reaches either, so a fed commit it does not print is
// held. No git runs where no record names a commit, or where held is empty: nothing is held then.
// A git that fails is an error, never a commit read as held.
func (g *Guard) remoteHeld(ctx context.Context, dir string, held []string, records []Record) (map[string]bool, error) {
	// reached is every commit asked about, until rev-list names the ones none of held reaches.
	reached := map[string]bool{}
	var fed strings.Builder
	for _, record := range records {
		if record.Commit == "" || reached[record.Commit] {
			continue
		}
		reached[record.Commit] = true
		fed.WriteString(record.Commit)
		fed.WriteString("\n")
	}
	if len(reached) == 0 || len(held) == 0 {
		return nil, nil
	}
	for _, sha := range held {
		fed.WriteString("^")
		fed.WriteString(sha)
		fed.WriteString("\n")
	}
	out, err := g.gitFed(ctx, dir, fed.String(), "rev-list", "--stdin")
	if err != nil {
		return nil, err
	}
	for _, unheld := range strings.Fields(out) {
		delete(reached, unheld)
	}
	return reached, nil
}

// knivesKeptFor is how long a push's read of the registries matches it against the kept list
// without reading a registry (Guard.knivesNaming), and so how long a change to a registry file can
// take to reach a role's pushes. The architect decided on 2026-11-17 to keep the pod's last good
// list, because a list that cannot be read must not refuse every push to every public repository
// from every role; and a read costs about 1.0-1.2 s each through the pod's legion gh (measured
// 2026-11-17), which a fresh kept list spares.
const knivesKeptFor = 10 * time.Minute

// knivesKept is the kept list of the read of the registries (Guard.knivesNaming): the merged
// entries of the last read in which every row was read and every list checked, and when that read
// was made, in UTC by the guard's clock. It is kept in a file of its own for each value of
// KnivesRegistriesVar, under the base environment's XDG_STATE_HOME (knivesKeptPath), which a pod
// names for each role. Only this user may read and write the file (readPrivateState,
// writePrivateState): it holds every entry's terms, and a term is private, which is why it is
// listed.
type knivesKept struct {
	ReadAt time.Time `json:"read_at"`
	// Entries are the read's entries, in the order it merged them.
	Entries []knivesEntry `json:"entries"`
}

// freshAt is whether the kept list stands in for a read at now: it was read no more than
// knivesKeptFor before now, and not after now, which only a clock set back since can show.
func (k knivesKept) freshAt(now time.Time) bool {
	age := now.Sub(k.ReadAt)
	return age >= 0 && age <= knivesKeptFor
}

// keptKnives is the kept list for the registries listed names, and false where none is kept: no
// XDG_STATE_HOME, no file, a file or directory this user alone could not have written
// (readPrivateState), or a file that is not a kept list.
func (g *Guard) keptKnives(listed string) (knivesKept, bool) {
	path := g.knivesKeptPath(listed)
	if path == "" {
		return knivesKept{}, false
	}
	content, ok := readPrivateState(path)
	if !ok {
		return knivesKept{}, false
	}
	var kept knivesKept
	if json.Unmarshal(content, &kept) != nil || kept.ReadAt.IsZero() {
		return knivesKept{}, false
	}
	return kept, true
}

// keepKnives makes entries, read in full at readAt, the kept list for the registries listed names.
// It writes nothing where the guard's base environment sets no XDG_STATE_HOME, or where the
// directory cannot be made private (writePrivateState).
func (g *Guard) keepKnives(listed string, readAt time.Time, entries []knivesEntry) {
	path := g.knivesKeptPath(listed)
	if path == "" {
		return
	}
	kept := knivesKept{ReadAt: readAt.UTC(), Entries: entries}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return
	}
	writePrivateState(path, ".knives-", append(encoded, '\n'))
}

// knivesKeptPath is the kept list's file for the registries listed names: under
// $XDG_STATE_HOME/legion-gh/knives, named for the sha256 of the value itself, so that other rows
// are another file and a list read from them never answers for these; "" where the guard's base
// environment sets no XDG_STATE_HOME.
func (g *Guard) knivesKeptPath(listed string) string {
	state := value(g.base(), "XDG_STATE_HOME")
	if state == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(listed))
	return filepath.Join(state, "legion-gh", "knives", fmt.Sprintf("%x.json", sum))
}

// KnivesRegistriesValue is rows as KnivesRegistriesVar carries them to every role process the
// Kubernetes runtime launches: a JSON list of {"repo", "path", "ref"} rows in order, which
// knivesRows reads back, and "" with no row, where the runtime sets no such variable at all and a
// push has no knives pass.
func KnivesRegistriesValue(rows []KnivesRegistry) string {
	if len(rows) == 0 {
		return ""
	}
	value, err := json.Marshal(rows)
	if err != nil {
		// A list of structs of three strings always marshals.
		panic("leakguard: marshal the knives registries: " + err.Error())
	}
	return string(value)
}

// knivesRows are the rows listed holds. A value of any other shape is an error, never a list read
// as empty: not one JSON list, a row with a field more, a row with a field missing or empty, or a
// repo that is no <owner>/<name> ghrepo.Parse takes. No error carries the value or the decoder's
// message, which can quote it.
func knivesRows(listed string) ([]KnivesRegistry, error) {
	notRows := errors.New(KnivesRegistriesVar + ` is not a JSON list of {"repo", "path", "ref"} rows`)
	decoder := json.NewDecoder(strings.NewReader(listed))
	decoder.DisallowUnknownFields()
	// A pointer, so that a JSON null, which decodes into a nil slice, is told from a list.
	var rows *[]KnivesRegistry
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
// as well, and an origin, release or upstream it names must be a string; either error names the
// row, the entry (knivesEntryName) and the field. A file that is not TOML of that shape is an
// error, never a registry read as empty.
//
// The files are read at once, one gh each, and merged in row order after the last read ends: a
// read takes about as long as a push's other passes together (measured 2026-11-17 in an issue
// pod, 5 pushes each to a local bare: a median of 5.12 s with two rows read one after the other,
// 3.45 s with none), and the reads depend on nothing of one another.
func (g *Guard) knivesEntries(ctx context.Context, rows []KnivesRegistry) ([]knivesEntry, error) {
	if len(rows) == 0 {
		// An empty list names no registry, and so no entry.
		return []knivesEntry{}, nil
	}
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
	var registry knivesRegistryFile
	if err := toml.Unmarshal([]byte(files[0]), &registry); err != nil {
		return nil, notKnivesTOML(0, err)
	}
	entries := map[string]*knivesEntry{}
	for _, name := range slices.Sorted(maps.Keys(registry.Repos)) {
		read := registry.Repos[name]
		entry := &knivesEntry{Name: name, Forbidden: read.Forbidden}
		named := knivesEntryName(name, knivesTerms(entry.Forbidden))
		for _, remote := range []struct {
			field string
			read  any
			into  *string
		}{{"origin", read.Origin, &entry.Origin}, {"release", read.Release, &entry.Release}, {"upstream", read.Upstream, &entry.Upstream}} {
			value, ok := remote.read.(string)
			if !ok && remote.read != nil {
				return nil, fmt.Errorf("%s: %s %s is not a string", knivesRowName(0), named, remote.field)
			}
			*remote.into = value
		}
		if entry.Origin == "" {
			return nil, fmt.Errorf("%s: %s names no origin", knivesRowName(0), named)
		}
		if err := knivesChecked(0, entry); err != nil {
			return nil, err
		}
		entries[name] = entry
	}
	for i := 1; i < len(files); i++ {
		var overlay knivesOverlayFile
		if err := toml.Unmarshal([]byte(files[i]), &overlay); err != nil {
			return nil, notKnivesTOML(i, err)
		}
		for _, name := range slices.Sorted(maps.Keys(overlay.Repos)) {
			entry, ok := entries[name]
			if !ok {
				// The overlay's own table name is not printed: an overlay is the private half.
				return nil, fmt.Errorf("%s adds to an entry no earlier registry has; an overlay only adds to an entry the registry already has", knivesRowName(i))
			}
			entry.Forbidden = append(entry.Forbidden, overlay.Repos[name].Forbidden...)
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
func (g *Guard) knivesRegistry(ctx context.Context, i int, row KnivesRegistry) (string, error) {
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
	for _, term := range entry.Forbidden {
		if strings.TrimSpace(term) == "" {
			return fmt.Errorf("%s: the forbidden list of %s holds an empty term", knivesRowName(i), knivesEntryName(entry.Name, knivesTerms(entry.Forbidden)))
		}
		lowered := strings.ToLower(term)
		if seen[lowered] {
			return fmt.Errorf("%s: the forbidden list of %s lists a term twice, case aside", knivesRowName(i), knivesEntryName(entry.Name, knivesTerms(entry.Forbidden)))
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
//   - and where the remote is a name, the URLs its configuration names, as written there and
//     before any rewriting (Guard.remotes): its remote.<name>.pushurl values, and its
//     remote.<name>.url values as well where no push URL names a GitHub repository. A push URL
//     at a local bare repository, which a role sets for one run (`git remote set-url --push origin
//     <bare>`), leaves the remote's URL naming the fork, so the rehearsal is a push to the fork
//     as the real push is, and its commits are read. A push URL that names another GitHub
//     repository is where the push goes, and the remote's URL names nothing of it. A URL or path
//     that names no GitHub repository is read as a name too, and no remote is called that, so its
//     reads answer nothing.
//
// The configuration is read only here, for a push the knives pass has a list for.
func (g *Guard) destinationRepos(ctx context.Context, req PushRequest) ([]string, error) {
	var repos []string
	add := func(address string) bool {
		repo := RepoFromURL(address)
		if repo != "" {
			repos = append(repos, repo)
		}
		return repo != ""
	}
	add(req.URL)
	if req.Remote == "" || add(req.Remote) {
		return repos, nil
	}
	remotes, err := g.remotes(ctx, req.Dir)
	if err != nil {
		return nil, err
	}
	pushed := slices.IndexFunc(remotes, func(r remote) bool { return r.name == req.Remote })
	if pushed < 0 {
		return repos, nil
	}
	named := false
	for _, address := range remotes[pushed].pushurls {
		named = add(address) || named
	}
	if !named {
		for _, address := range remotes[pushed].urls {
			add(address)
		}
	}
	return repos, nil
}

// remote is one remote of a repository's configuration, its URLs as written there and before
// any rewriting.
type remote struct {
	// name is the remote's name, the subsection of its remote.<name>.* keys, as written.
	name string
	// urls are its remote.<name>.url values and pushurls its remote.<name>.pushurl values, each
	// in the order the configuration lists them.
	urls, pushurls []string
}

// remotes are the remotes of dir's configuration that name a URL or a push URL, in the order
// git config first names each. One git config reads them all:
//
//	git config -z --get-regexp '^remote\..*\.(url|pushurl)$'
//
// It prints "remote.<name>.<key>\n<value>" for each value, ending each with a zero byte, so a
// value with a newline in it is read whole; the name is all that lies between the first dot and
// the last, dots and slashes included. A key set with no value at all has no newline, and names
// no URL. git config answers 1 where no key matches, which is a configuration with no remote;
// any other status is an error.
func (g *Guard) remotes(ctx context.Context, dir string) ([]remote, error) {
	var stderr strings.Builder
	status, out, err := g.run(ctx, child{argv: gitArgv("config", "-z", "--get-regexp", `^remote\..*\.(url|pushurl)$`), dir: dir, stderr: &stderr})
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
	var remotes []remote
	for entry := range strings.SplitSeq(out, "\x00") {
		key, value, ok := strings.Cut(entry, "\n")
		if !ok {
			continue
		}
		subsection, ok := strings.CutPrefix(key, "remote.")
		dot := strings.LastIndexByte(subsection, '.')
		if !ok || dot < 0 {
			continue
		}
		name, variable := subsection[:dot], subsection[dot+1:]
		i := slices.IndexFunc(remotes, func(r remote) bool { return r.name == name })
		if i < 0 {
			i = len(remotes)
			remotes = append(remotes, remote{name: name})
		}
		if variable == "pushurl" {
			remotes[i].pushurls = append(remotes[i].pushurls, value)
		} else {
			remotes[i].urls = append(remotes[i].urls, value)
		}
	}
	return remotes, nil
}

// knivesApplying are the entries whose origin or release names one of repos (namesOneOf).
func knivesApplying(entries []knivesEntry, repos []string) []knivesEntry {
	var applying []knivesEntry
	for _, entry := range entries {
		if namesOneOf(entry.Origin, repos) || namesOneOf(entry.Release, repos) {
			applying = append(applying, entry)
		}
	}
	return applying
}

// namesOneOf is whether address, a URL read with RepoFromURL, names one of repos, case aside: how
// the knives pass matches an entry against the destination (knivesApplying).
func namesOneOf(address string, repos []string) bool {
	repo := RepoFromURL(address)
	return repo != "" && slices.ContainsFunc(repos, func(named string) bool { return strings.EqualFold(named, repo) })
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
		terms[e] = knivesTerms(entry.Forbidden)
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
					line += " of " + knivesEntryName(entries[e].Name, every)
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
