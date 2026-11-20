package policy

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sjawhar/envoy/internal/broker/airlock"
	"github.com/sjawhar/envoy/internal/broker/rulehost"
	"github.com/sjawhar/envoy/internal/broker/secrets"
)

// Profiles is the deployment's profiles file, BROKER_PROFILES_FILE, as ParseProfiles read it: the
// names a caller asks for, each naming the stored secret it is made from, the scope it is cut to
// and, with a proxy block, where its credential may go. It is immutable once read, and the zero
// value is a deployment with no profiles file, which holds no profile and no proxy block.
type Profiles struct {
	list []Profile
}

// Profile is one entry of the profiles file. A field the file leaves out is the source's: Source
// is the profile's own name, and Tier, Requesters and MaxGrant are empty. Its blocks are nil when
// the file has none.
type Profile struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	// Tier is agent or human, or "" for the source's.
	Tier string `json:"tier,omitempty"`
	// Requesters is the people and services that may ask for it, sorted and each once, or nil for
	// the source's.
	Requesters []string `json:"requesters,omitempty"`
	// MaxGrant is the longest grant of it, or 0 for the source's.
	MaxGrant time.Duration `json:"maxGrant,omitempty"`
	GitHub   *GitHubBlock  `json:"github,omitempty"`
	Google   *GoogleBlock  `json:"google,omitempty"`
	Proxy    *ProxyBlock   `json:"proxy,omitempty"`
}

// GitHubBlock is a GitHub App profile's scope: the installations its token is minted on, the
// repositories it is cut to, and the permissions it asks GitHub for.
type GitHubBlock struct {
	// Accounts are GitHub logins, lowercased, as GitHub compares them, and sorted.
	Accounts []string `json:"accounts"`
	// Repositories are "account/name", the account lowercased, sorted and each once; nil is
	// every repository the installation reaches.
	Repositories []string `json:"repositories,omitempty"`
	// Permissions maps each GitHub App permission to its level: read, write or admin.
	Permissions map[string]string `json:"permissions"`
	// Value is ValueInstallations or ValueToken.
	Value string `json:"value"`
}

// The two forms a GitHub profile's value takes (GitHubBlock.Value): today's JSON of one entry per
// account, with its installation_id, token and expires_at, which is the default, or the bare token
// of the one account in effect.
const (
	ValueInstallations = "installations"
	ValueToken         = "token"
)

// GoogleBlock is a google-service-account profile's scope: the scopes its token carries, each the
// part after https://www.googleapis.com/auth/ (ParseScopes), sorted.
type GoogleBlock struct {
	Scopes []string `json:"scopes"`
}

// ProxyBlock is where a profile's credential may go: the fields of an airlock rules entry, with
// their meanings there (airlock.SecretRule), under the profile's name. A profile with one is
// behind the proxy (ProxyRules).
type ProxyBlock struct {
	Hosts   []string        `json:"hosts"`
	Headers []string        `json:"headers"` // nil: the default ["authorization"]; empty: no header
	Allow   []airlock.Allow `json:"allow"`   // nil: any method and path; empty: nothing allowed
	Kind    string          `json:"kind"`    // "", "aws" or "azure"
	Judge   *uint32         `json:"judge"`   // the judge's longest lifetime, in seconds
	// Guidance is what the model judge reads when it decides a request for the profile. It is
	// no part of Digest's fingerprint.
	Guidance string `json:"guidance,omitempty"`
}

// profileEntry is one profile as the file writes it, which Profile is but for maxGrant: a Go
// duration in the file and a time.Duration once read. The other fields' tags are Profile's.
type profileEntry struct {
	Name       string       `json:"name"`
	Source     string       `json:"source"`
	Tier       string       `json:"tier"`
	Requesters []string     `json:"requesters"`
	MaxGrant   string       `json:"maxGrant"`
	GitHub     *GitHubBlock `json:"github"`
	Google     *GoogleBlock `json:"google"`
	Proxy      *ProxyBlock  `json:"proxy"`
}

// maxGuidanceRunes is the longest guidance a proxy block may give the judge. The judge shows the
// whole text to its model beside the request's reason and what Dispatch holds of it, all within
// one bounded prompt, so a text past this would be cut rather than read; it is refused instead,
// at the boot and in `profiles check`, where its author reads why.
const maxGuidanceRunes = 2000

// permissionName is what a GitHub App permission's name is: lowercase letters and underscores
// (contents, pull_requests, organization_administration).
var permissionName = regexp.MustCompile(`^[a-z_]+$`)

// permissionLevels are the levels GitHub gives a permission.
var permissionLevels = []string{"read", "write", "admin"}

// writeOnlyPermissions are the permissions GitHub grants at write alone, the one level its REST
// description (components.schemas.app-permissions) gives them, so a profile that asks for them at
// read would be refused by GitHub at every mint.
var writeOnlyPermissions = []string{"profile", "workflows"}

// ParseProfiles reads a profiles file and refuses one the broker would not serve. It decodes the
// file strictly (airlock.Decode: a field the broker does not know, and anything after the file's
// one object, are refused), and then holds each profile, in the file's order, to the file's shape:
// a name a caller can ask for, held once; a source of that form; a tier of agent or human;
// requesters of a person's or a service's shape, and never an empty list, which would serve the
// profile to nobody; a maxGrant of at least a second; a github block of GitHub logins, repositories
// of those accounts, at most secrets.MaxNarrowedRepositories of them, and permissions at levels
// GitHub gives; a google block of scope names; never both; and a proxy block whose hosts are in
// the one grammar a rules host is written in (rulehost.Refusal), which airlock.Validate, the Lean
// model's answer, accepts, and whose guidance the judge reads whole. Each refusal names the
// profile by its index and name and the field it refuses.
//
// It reads no secret: whether a profile fits its source, and is no looser than it, is the policy
// load's question. Nor does it read the deployment's services: CheckRequesters does, at the boot.
func ParseProfiles(data []byte) (Profiles, error) {
	entries, err := decodeProfiles(data)
	if err != nil {
		return Profiles{}, err
	}
	seen := make(map[string]int, len(entries))
	for i, e := range entries {
		if err := e.check(i, seen); err != nil {
			return Profiles{}, err
		}
		seen[e.Name] = i
	}
	return readProfiles(entries)
}

// DecodeProfiles reads a profiles file as ParseProfiles does, into the same form, and holds it to
// none of the file's shape rules. It exists for `profiles check --against`, which reports what a
// change does to the file in force: that file may be one a stricter rule of this broker refuses,
// and a report that refused it would also refuse the pull request that fixes it. It still refuses
// a file that is not JSON or holds a field the broker does not know, and a maxGrant that is no
// duration, since such a file has no profiles to compare.
func DecodeProfiles(data []byte) (Profiles, error) {
	entries, err := decodeProfiles(data)
	if err != nil {
		return Profiles{}, err
	}
	return readProfiles(entries)
}

// decodeProfiles is the strict decode both readers share.
func decodeProfiles(data []byte) ([]profileEntry, error) {
	var file struct {
		Profiles []profileEntry `json:"profiles"`
	}
	if err := airlock.Decode(data, &file); err != nil {
		return nil, err
	}
	return file.Profiles, nil
}

// readProfiles answers each entry in the one form the broker serves it in. The entries have been
// checked when ParseProfiles calls it, so the forms it falls back to for a value the rules refuse
// (a login as written) are DecodeProfiles' alone; a maxGrant that is no duration has no form.
func readProfiles(entries []profileEntry) (Profiles, error) {
	list := make([]Profile, 0, len(entries))
	for i, e := range entries {
		p := Profile{
			Name:       e.Name,
			Source:     cmp.Or(e.Source, e.Name),
			Tier:       e.Tier,
			Requesters: sortedOnce(e.Requesters),
			GitHub:     e.GitHub.read(),
			Google:     e.Google.read(),
			Proxy:      e.Proxy,
		}
		if e.MaxGrant != "" {
			maxGrant, err := ParseMaxGrant(e.MaxGrant)
			if err != nil {
				return Profiles{}, refusal(i, e.Name, "maxGrant", fmt.Sprintf("%q: %v", e.MaxGrant, err))
			}
			p.MaxGrant = maxGrant
		}
		list = append(list, p)
	}
	return Profiles{list: list}, nil
}

// read is a github block in the form the broker serves: logins lowercased and sorted, each
// repository's account lowercased, the repositories sorted and each once, and the value's default
// written out. A login ParseGitHubLogin refuses is kept as written, for DecodeProfiles.
func (b *GitHubBlock) read() *GitHubBlock {
	if b == nil {
		return nil
	}
	read := &GitHubBlock{Permissions: maps.Clone(b.Permissions), Value: cmp.Or(b.Value, ValueInstallations)}
	for _, account := range b.Accounts {
		read.Accounts = append(read.Accounts, loginOrAsWritten(account))
	}
	slices.Sort(read.Accounts)
	if b.Repositories != nil {
		read.Repositories = make([]string, 0, len(b.Repositories))
		for _, repository := range b.Repositories {
			if account, name, ok := strings.Cut(repository, "/"); ok {
				repository = loginOrAsWritten(account) + "/" + name
			}
			read.Repositories = append(read.Repositories, repository)
		}
		slices.Sort(read.Repositories)
		read.Repositories = slices.Compact(read.Repositories)
	}
	return read
}

// loginOrAsWritten is ParseGitHubLogin's lowercased login, or the text as written when it is no
// login.
func loginOrAsWritten(raw string) string {
	if login, err := ParseGitHubLogin(raw); err == nil {
		return login
	}
	return raw
}

// read is a google block with its scopes sorted.
func (b *GoogleBlock) read() *GoogleBlock {
	if b == nil {
		return nil
	}
	scopes := slices.Clone(b.Scopes)
	slices.Sort(scopes)
	return &GoogleBlock{Scopes: scopes}
}

// sortedOnce is values sorted and each once, nil for nil.
func sortedOnce(values []string) []string {
	if values == nil {
		return nil
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return slices.Compact(sorted)
}

// refusal is ParseProfiles' refusal of one field of the profile at index i: the profile by its
// index and name, the field, and why.
func refusal(i int, name, field, why string) error {
	return fmt.Errorf("profiles[%d] %q: %s %s", i, name, field, why)
}

// check holds the entry at index i to the file's shape (ParseProfiles), with seen the index of
// each name an earlier entry holds. It reads the fields in the file's order and answers the first
// refusal.
func (e profileEntry) check(i int, seen map[string]int) error {
	refuse := func(field, why string) error { return refusal(i, e.Name, field, why) }
	switch _, err := NameToSlug(e.Name); {
	case e.Name == "":
		return refuse("name", "is required: it is the name a caller asks for")
	case err != nil:
		return refuse("name", fmt.Sprintf("is %v, so no caller can ask for it", err))
	}
	if j, ok := seen[e.Name]; ok {
		return refuse("name", fmt.Sprintf("repeats profiles[%d]'s name, and a caller asks for one profile by it", j))
	}
	if e.Source != "" {
		if _, err := NameToSlug(e.Source); err != nil {
			return refuse("source", fmt.Sprintf("%q is %v, so it names no stored secret", e.Source, err))
		}
	}
	if e.Tier != "" && e.Tier != TierAgent && e.Tier != TierHuman {
		return refuse("tier", fmt.Sprintf("%q is neither %s nor %s", e.Tier, TierAgent, TierHuman))
	}
	if err := e.checkRequesters(refuse); err != nil {
		return err
	}
	if e.MaxGrant != "" {
		if _, err := ParseMaxGrant(e.MaxGrant); err != nil {
			return refuse("maxGrant", fmt.Sprintf("%q: %v", e.MaxGrant, err))
		}
	}
	if e.GitHub != nil && e.Google != nil {
		return refuse("github and google", "are both set: a profile scopes one source, a github-app secret or a google-service-account one, never both")
	}
	if err := e.GitHub.check(refuse); err != nil {
		return err
	}
	if err := e.Google.check(refuse); err != nil {
		return err
	}
	return e.Proxy.check(e.Name, refuse)
}

// checkRequesters holds the requesters list to a requesters tag's shape (ParseRequesterShapes),
// one entry per person or service, and refuses an empty list, which would serve the profile to
// nobody where leaving it out takes the source's.
func (e profileEntry) checkRequesters(refuse func(field, why string) error) error {
	if e.Requesters == nil {
		return nil
	}
	if len(e.Requesters) == 0 {
		return refuse("requesters", "is empty, which serves the profile to nobody; leave it out to take the source's")
	}
	for j, entry := range e.Requesters {
		field := fmt.Sprintf("requesters[%d]", j)
		if _, err := ParseRequesterShapes(entry); err != nil {
			return refuse(field, fmt.Sprintf("is refused: %v", err))
		}
		if fields := strings.Fields(entry); len(fields) != 1 || fields[0] != entry {
			return refuse(field, fmt.Sprintf("%q is not one entry: write each person and service as an entry of its own", entry))
		}
	}
	return nil
}

// check holds a github block to its shape: accounts that are GitHub logins, each once whatever
// its case; repositories, when present, at least one and at most secrets.MaxNarrowedRepositories,
// each account/name of an account the block names; and permissions, at least one, each a
// permission's name at a level GitHub gives it.
func (b *GitHubBlock) check(refuse func(field, why string) error) error {
	if b == nil {
		return nil
	}
	if len(b.Accounts) == 0 {
		return refuse("github.accounts", "is required, with at least one GitHub login of the source's github-owners tag")
	}
	accounts := make([]string, 0, len(b.Accounts))
	for j, raw := range b.Accounts {
		field := fmt.Sprintf("github.accounts[%d]", j)
		login, err := ParseGitHubLogin(raw)
		if err != nil {
			return refuse(field, fmt.Sprintf("is refused: %v", err))
		}
		if slices.Contains(accounts, login) {
			return refuse(field, fmt.Sprintf("names %s twice: GitHub compares logins without regard to case", login))
		}
		accounts = append(accounts, login)
	}
	if err := checkRepositories(b.Repositories, accounts, refuse); err != nil {
		return err
	}
	if len(b.Permissions) == 0 {
		return refuse("github.permissions", "is required, with at least one permission: a token request always names the permissions it asks for")
	}
	for _, permission := range slices.Sorted(maps.Keys(b.Permissions)) {
		level := b.Permissions[permission]
		field := "github.permissions." + permission
		switch {
		case !permissionName.MatchString(permission):
			return refuse("github.permissions", fmt.Sprintf("names %q, which is no GitHub App permission's name: lowercase letters and underscores", permission))
		case !slices.Contains(permissionLevels, level):
			return refuse(field, fmt.Sprintf("is %q, not a level GitHub gives: read, write or admin", level))
		case slices.Contains(writeOnlyPermissions, permission) && level != "write":
			return refuse(field, fmt.Sprintf("is %q, but GitHub grants %s at write alone", level, permission))
		}
	}
	if b.Value != "" && b.Value != ValueInstallations && b.Value != ValueToken {
		return refuse("github.value", fmt.Sprintf("%q is neither %s nor %s", b.Value, ValueInstallations, ValueToken))
	}
	return nil
}

// checkRepositories holds github.repositories to its shape. Absent names none, so the token
// reaches every repository of the installation; present but empty is refused, since a list that
// matched nothing would otherwise get that widest token.
func checkRepositories(repositories, accounts []string, refuse func(field, why string) error) error {
	switch {
	case repositories == nil:
		return nil
	case len(repositories) == 0:
		return refuse("github.repositories", "is empty; leave it out for every repository the installation reaches")
	case len(repositories) > secrets.MaxNarrowedRepositories:
		return refuse("github.repositories", fmt.Sprintf("names %d repositories; a token takes at most %d", len(repositories), secrets.MaxNarrowedRepositories))
	}
	for j, repository := range repositories {
		field := fmt.Sprintf("github.repositories[%d]", j)
		account, name, ok := strings.Cut(repository, "/")
		if !ok {
			return refuse(field, fmt.Sprintf("%q is not account/name", repository))
		}
		login, err := ParseGitHubLogin(account)
		if err != nil {
			return refuse(field, fmt.Sprintf("%q is not account/name: %v", repository, err))
		}
		if !slices.Contains(accounts, login) {
			return refuse(field, fmt.Sprintf("%q is a repository of %s, which github.accounts does not name", repository, login))
		}
		if !gitHubRepositoryName.MatchString(name) || name == "." || name == ".." {
			return refuse(field, fmt.Sprintf("%q names a repository GitHub cannot hold: a name is ASCII letters, digits, '.', '-' and '_', and neither . nor ..", repository))
		}
	}
	return nil
}

// check holds a google block to its shape: at least one scope, each a scopes tag's entry
// (ParseScopes), each once.
func (b *GoogleBlock) check(refuse func(field, why string) error) error {
	if b == nil {
		return nil
	}
	if len(b.Scopes) == 0 {
		return refuse("google.scopes", "is required, with at least one scope of the source's scopes tag")
	}
	for j, scope := range b.Scopes {
		field := fmt.Sprintf("google.scopes[%d]", j)
		if got, err := ParseScopes(scope); err != nil || got != scope {
			return refuse(field, fmt.Sprintf("%q is not one scope: %v", scope, cmp.Or(err, ErrScopesEntry)))
		}
		if slices.Contains(b.Scopes[:j], scope) {
			return refuse(field, fmt.Sprintf("names %s twice", scope))
		}
	}
	return nil
}

// check holds a proxy block to what the airlock serves, in the order the boot always has: a
// block names at least one host, since a block binds its credential to the hosts it names; then
// each host in the one grammar a rules host is written in (rulehost.Refusal), whose refusal names
// the host's reason and repeats no text of it but the form to write; then airlock.Validate's
// answer for the block as the entry ProxyRules makes of it, which is the Lean model's (a judge
// above 900 seconds, a kind other than "", aws or azure); and last the guidance's length, this
// program's own rule and not the model's. The grammar serves a host only where Validate's
// airlock.HostShapeOK accepts it, so Validate refuses no host here.
func (b *ProxyBlock) check(name string, refuse func(field, why string) error) error {
	if b == nil {
		return nil
	}
	if len(b.Hosts) == 0 {
		return refuse("proxy.hosts", "is required, with at least one host: a proxy block binds its credential to the hosts it names")
	}
	for j, host := range b.Hosts {
		if why := rulehost.Refusal(host); why != "" {
			return refuse(fmt.Sprintf("proxy.hosts[%d]", j), why)
		}
	}
	if err := airlock.Validate(airlock.Rules{Secrets: []airlock.SecretRule{b.rule(name)}}); err != nil {
		// Validate names the entry as the one rules file it read: index 0 of a file of one.
		// The profile is named already, so its words follow the field's.
		return refuse("proxy", strings.TrimPrefix(err.Error(), fmt.Sprintf("secrets[0] %q: ", name)))
	}
	if n := utf8.RuneCountInString(b.Guidance); n > maxGuidanceRunes {
		return refuse("proxy.guidance", fmt.Sprintf("is %d characters, longer than the %d the judge is shown", n, maxGuidanceRunes))
	}
	return nil
}

// rule is the airlock rules entry of this block under the profile's name.
func (b *ProxyBlock) rule(name string) airlock.SecretRule {
	return airlock.SecretRule{Name: name, Hosts: b.Hosts, Headers: b.Headers, Allow: b.Allow, Kind: b.Kind, Judge: b.Judge, Guidance: b.Guidance}
}

// CheckRequesters reads each profile's requesters against the deployment's registered services
// (ParseRequesters), as the loader reads a requesters tag. ParseProfiles holds a requester to its
// shape alone, since `profiles check` runs where no services setting is; the boot, which holds
// that setting, calls this too, so a profile naming a service the deployment does not register
// refuses the start rather than serving the profile to nobody of that name.
func (p Profiles) CheckRequesters(services []string) error {
	for i, profile := range p.list {
		if profile.Requesters == nil {
			continue
		}
		if _, err := ParseRequesters(strings.Join(profile.Requesters, " "), services); err != nil {
			return refusal(i, profile.Name, "requesters", fmt.Sprintf("is refused: %v", err))
		}
	}
	return nil
}

// Lookup finds the profile a caller asks for by name, or nil. The profile points into p, so
// every Lookup of one name answers the one profile the file was read into.
func (p Profiles) Lookup(name string) *Profile {
	for i := range p.list {
		if p.list[i].Name == name {
			return &p.list[i]
		}
	}
	return nil
}

// Names is each profile's name in the file's order. It exists for `profiles check`, whose report
// walks the names two files hold; a reader of one profile asks Lookup.
func (p Profiles) Names() []string {
	names := make([]string, len(p.list))
	for i, profile := range p.list {
		names[i] = profile.Name
	}
	return names
}

// ProxyRules is the airlock's rules: one entry for each proxy block, under its profile's name, in
// the file's order. A profile with no proxy block has no entry, so its credential is released as
// a name the rules do not hold is.
func (p Profiles) ProxyRules() airlock.Rules {
	var rules airlock.Rules
	for _, profile := range p.list {
		if profile.Proxy != nil {
			rules.Secrets = append(rules.Secrets, profile.Proxy.rule(profile.Name))
		}
	}
	return rules
}

// Digest is the fingerprint of one profile, "" when p holds none of that name. It is the SHA-256,
// in hex, of the profile's JSON as encoding/json marshals a Profile, in the form ParseProfiles
// answers it, with its proxy block's guidance left out: two spellings of one profile fingerprint
// alike, and every field that decides what a grant of it may do is in the fingerprint. The broker
// folds it into its policy version, so an edit of a profile's entry ends that profile's grants and
// no others. Guidance is left out because it tells the judge how to decide requests to come and
// changes nothing a live grant may do, so writing it ends no grant.
func (p Profiles) Digest(name string) string {
	profile := p.Lookup(name)
	if profile == nil {
		return ""
	}
	fingerprinted := *profile
	if profile.Proxy != nil {
		block := *profile.Proxy
		block.Guidance = ""
		fingerprinted.Proxy = &block
	}
	// Marshal cannot fail for a Profile: every field is a string, a duration, a slice or map of
	// strings, a slice of airlock.Allow, a *uint32 or a pointer to a struct of those.
	encoded, _ := json.Marshal(fingerprinted)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
