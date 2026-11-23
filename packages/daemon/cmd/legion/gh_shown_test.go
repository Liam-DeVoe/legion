package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// ghShownCanary is the made-up value the canary rows put in every slot of the call they make —
// each argument, each option's value, each selector, each environment value and each search query
// — shaped as the slot reads it (a token in a URL's user info, an owner's place, a search word), so
// that no refusal of legion gh can print any part of it but by quoting a slot's value whole.
const ghShownCanary = "zqcanary0123"

// ghCanaryRow is one call that reaches a refusal path (ghShownPaths) with ghShownCanary in its
// slots: argv and env for ghTargetOf, under savedGhHelp unless help is set and with no git remotes
// unless remotes is set, or run for a path ghTargetOf does not reach; names is what the refusal
// must say, the place the path names the value by, which shows the row reached that path.
type ghCanaryRow struct {
	argv    []string
	env     map[string]string
	help    func(path []string) (string, error)
	remotes func() ([]ghRemote, error)
	run     func() error
	names   string
}

// ghCanaryRows are the rows of every refusal path, keyed as ghShownPaths keys a call of ghShown:
// the function that makes it, and the source of the place it names the value by.
var ghCanaryRows = map[string][]ghCanaryRow{
	`ghTargetOf: "another host than github.com"`: {
		{argv: []string{"pr", "list", "--search", ghShownCanary}, env: map[string]string{"GH_HOST": "x-access-token:" + ghShownCanary + "@ghe.example.com"}, names: "GH_HOST names another host than github.com"},
		{argv: []string{"pr", "list"}, env: map[string]string{"GH_HOST": ghShownCanary + ".example.com"}, names: "GH_HOST names another host than github.com"},
	},
	`ghTargetOf: "an option given before the command's name"`: {
		{argv: []string{"--token=" + ghShownCanary, "pr", "list", "--search", ghShownCanary}, names: "an option given before the command's name is refused"},
		{argv: []string{"--" + ghShownCanary, "pr", "list"}, names: "an option given before the command's name is refused"},
	},
	`ghHelpTarget: "an option given to gh help"`: {
		{argv: []string{"help", "--token=" + ghShownCanary, "pr"}, names: "an option given to gh help is refused"},
	},
	`ghWalk.check: "the command's first word"`: {
		{argv: []string{ghShownCanary, "list", "--body", ghShownCanary}, names: "the command's first word is no command gh's own help lists"},
		{argv: []string{"x-access-token:" + ghShownCanary + "@ghe.example.com", "list"}, names: "the command's first word is no command gh's own help lists"},
	},
	`ghWalk.named: "a subcommand its help does not list"`: {
		{argv: []string{"pr", ghShownCanary, "--body", ghShownCanary}, names: "`gh pr` and a subcommand its help does not list"},
		{argv: []string{"issue", "x-access-token:" + ghShownCanary, "1"}, names: "`gh issue` and a subcommand its help does not list"},
	},
	`ghCommand.parse: "an option the command's help does not list"`: {
		{argv: []string{"secret", "set", "DEPLOY_KEY", "--bdy=" + ghShownCanary, "-R", "acme/widgets"}, names: "an option the command's help does not list is given to `gh secret set`"},
		{argv: []string{"secret", "set", "DEPLOY_KEY", "--body " + ghShownCanary, "-R", "acme/widgets"}, names: "an option the command's help does not list is given to `gh secret set`"},
		{argv: []string{"pr", "list", "--token=" + ghShownCanary}, names: "an option the command's help does not list is given to `gh pr list`"},
		{argv: []string{"pr", "list", "--" + ghShownCanary}, names: "an option the command's help does not list is given to `gh pr list`"},
	},
	`ghCommand.parse: "a short option the command's help does not list"`: {
		{argv: []string{"secret", "set", "DEPLOY_KEY", "-Z" + ghShownCanary, "-R", "acme/widgets"}, names: "a short option the command's help does not list is given to `gh secret set`"},
		{argv: []string{"search", "issues", "bug", "-repo:x-access-token:" + ghShownCanary}, names: "a short option the command's help does not list is given to `gh search issues`"},
	},
	`ghCloneGitFlags: "a git flag after --"`: {
		{argv: []string{"repo", "clone", "acme/app", "--", "--config=http.https://github.com/.extraheader=AUTHORIZATION: basic " + ghShownCanary}, names: "hands a git flag after -- to git clone"},
		{argv: []string{"repo", "clone", "acme/app", "--", "--config=http.extraheader=AUTHORIZATION: bearer " + ghShownCanary}, names: "hands a git flag after -- to git clone"},
		{argv: []string{"repo", "clone", "acme/app", "--", "-chttp.extraheader=AUTHORIZATION: bearer " + ghShownCanary}, names: "hands a git flag after -- to git clone"},
		{argv: []string{"repo", "clone", "acme/app", "--", "dir-" + ghShownCanary, ghShownCanary}, names: "hands a git flag after -- to git clone"},
	},
	`selectors.fromAPI: "the q field"`: {
		{argv: []string{"api", "-X", "GET", "search/issues", "-F", "q=@" + ghShownCanary}, names: "the q field reads the search query from a file"},
	},
	`selectors.fromOptions: "a repository named by its name alone"`: {
		{argv: []string{"secret", "set", "DEPLOY_KEY", "--user", "--repos", ghShownCanary, "--body", ghShownCanary}, names: "a repository named by its name alone is given to --repos, and no --org says whose"},
	},
	`selectors.query: "a word of the search query"`: {
		{argv: []string{"search", "code", "password: hunter" + ghShownCanary}, names: "a word of the search query is a search qualifier this cannot read"},
		{argv: []string{"search", "issues", "label:\"" + ghShownCanary + "\""}, names: "a word of the search query is a search qualifier this cannot read"},
	},
	`selectors.query: "a -repo:, -owner:, -user: or -org: qualifier of the search query"`: {
		{argv: []string{"search", "issues", "--", "-repo:x-access-token:" + ghShownCanary + "@ghe.example.com"}, names: "a -repo:, -owner:, -user: or -org: qualifier of the search query excludes an owner"},
	},
	`selectors.query: "an OR, a NOT or a parenthesis of the search query"`: {
		{argv: []string{"search", "issues", "repo:acme/widgets", "(" + ghShownCanary}, names: "an OR, a NOT or a parenthesis of the search query joins"},
	},
	`selectors.endpoint: "the endpoint's host"`: {
		{argv: []string{"api", "https://x-access-token:" + ghShownCanary + "@ghe.example.com/repos/acme/app"}, names: "the endpoint's host is another host than api.github.com"},
	},
	`selectors.endpoint: "the endpoint's repositories/<id> path"`: {
		{argv: []string{"api", "repositories/1?access_token=" + ghShownCanary}, names: "the endpoint's repositories/<id> path names a repository by its id"},
	},
	`selectors.endpoint: "the endpoint's query string"`: {
		{argv: []string{"api", "search/issues?q=x&access_token=" + ghShownCanary + "%zz"}, names: "the endpoint's query string is one this cannot read"},
	},
	`repositoryTarget: kind`: {
		{argv: []string{"pr", "list", "-R", "x-access-token:" + ghShownCanary + "@ghe.example.com/acme/widgets"}, names: "a -R value holds an \"@\", a \":\", a \"?\" or a \"#\""},
		{argv: []string{"pr", "list", "-R", "ghe.example.com/x-access-token:" + ghShownCanary + "@ghe.example.com/acme/app"}, names: "a -R value holds an \"@\", a \":\", a \"?\" or a \"#\""},
		{argv: []string{"pr", "list", "-R", "https://admin:hunter'" + ghShownCanary + "@ghe.example/acme/app"}, names: "a -R value holds a user name or a password"},
		{argv: []string{"pr", "list", "-R", "https://x-access-token:" + ghShownCanary + "@github.com/acme/widgets"}, names: "a -R value holds a user name or a password"},
		{argv: []string{"pr", "list", "-R", "https://ghe.example.com/" + ghShownCanary + "/widgets"}, names: "a -R value names another host than github.com"},
		{argv: []string{"pr", "list", "-R", ghShownCanary}, names: "a -R value names no owner"},
		{argv: []string{"pr", "list", "-R", "acme/" + ghShownCanary + "%zz"}, names: "a -R value names a repository whose name holds a character"},
		{argv: []string{"pr", "list"}, env: map[string]string{"GH_REPO": "x-access-token:" + ghShownCanary + "@github.com/acme/widgets"}, names: "the GH_REPO value holds an \"@\""},
		{argv: []string{"pr", "list"}, env: map[string]string{"GH_REPO": "https://ghe.example.com/acme/" + ghShownCanary}, names: "the GH_REPO value names another host than github.com"},
		{argv: []string{"repo", "view", "https://x-access-token:" + ghShownCanary + "@github.com/acme/widgets"}, names: "a repository argument holds a user name or a password"},
		{argv: []string{"search", "issues", "--", "repo:" + ghShownCanary}, names: "a repo: qualifier names no owner"},
		{argv: []string{"search", "issues", "repo:x-access-token:" + ghShownCanary + "@ghe.example/acme/app"}, names: "a repo: qualifier holds an \"@\""},
		{argv: []string{"api", "repos/x-access-token:" + ghShownCanary + "@ghe.example/acme"}, names: "the endpoint holds an \"@\""},
	},
	`checkedOwner: "an owner that is not a GitHub login"`: {
		{argv: []string{"repo", "list", "x-access-token:" + ghShownCanary}, names: "an owner argument names an owner that is not a GitHub login"},
		{argv: []string{"search", "repos", "--owner", "x-access-token:" + ghShownCanary}, names: "a --owner value names an owner that is not a GitHub login"},
		{argv: []string{"pr", "list", "-R", "https://github.com/x-access-token:" + ghShownCanary + "@github.com/acme/app"}, names: "a -R value names an owner that is not a GitHub login"},
		{argv: []string{"pr", "list", "-R", "github.com/" + ghShownCanary + ".x/app"}, names: "a -R value names an owner that is not a GitHub login"},
		{argv: []string{"api", "orgs/" + ghShownCanary + ".x/repos"}, names: "the endpoint names an owner that is not a GitHub login"},
	},
	`withGitHubUseHTTPPath: "a value that is not printed"`: {
		{run: func() error {
			_, err := withGitHubUseHTTPPath([]string{"GIT_CONFIG_COUNT=" + ghShownCanary})
			return err
		}, names: "GIT_CONFIG_COUNT holds a value that is not printed, which is no count"},
	},
}

// ghShownPaths is every call of ghShown in this package's non-test gh*.go files, the refusal
// paths of legion gh that put a person's value into a refusal, each keyed by the function that
// makes it (a method by its receiver's type) and the source of its place argument, with the file
// and line it stands at. It is read from the source, so a call added to the code is a path here
// without anyone listing it. A place two calls of one function share would key both as one, and
// is refused.
func ghShownPaths(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("gh*.go")
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, name, source, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			owner := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				recv := fn.Recv.List[0].Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				if ident, ok := recv.(*ast.Ident); ok {
					owner = ident.Name + "." + owner
				}
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if ident, ok := call.Fun.(*ast.Ident); !ok || ident.Name != "ghShown" {
					return true
				}
				at := fset.Position(call.Pos()).String()
				if len(call.Args) < 2 {
					t.Fatalf("%s: ghShown takes a value and a place, given %d arguments", at, len(call.Args))
				}
				place := call.Args[1]
				key := owner + ": " + string(source[fset.Position(place.Pos()).Offset:fset.Position(place.End()).Offset])
				if prior, seen := paths[key]; seen {
					t.Fatalf("%s and %s both call ghShown as %s: give each path a place of its own", prior, at, key)
				}
				paths[key] = at
				return true
			})
		}
	}
	return paths
}

// Every refusal path of legion gh that puts a value a person gave it into a refusal — every call
// of ghShown the source holds — has rows, and every row's path is one the source holds: a new
// path fails this until it joins the table. Each row puts ghShownCanary in every slot of its call
// and reaches its path, which names the value by its place (the row's names), and the canary
// reaches neither stdout nor stderr: ghTargetOf's refusal printed as legion gh prints it
// (printGhRefusal), and, for a row that needs no help, remotes or code of its own, `legion gh`
// itself, run as a pane runs it, with the saved help's stand-in gh, whose own output the help read
// never prints. A source holding no path at all is refused: the table would test nothing.
func TestNoRefusalPathPrintsACanary(t *testing.T) {
	paths := ghShownPaths(t)
	if len(paths) == 0 {
		t.Fatal("UNKNOWN: the source holds no call of ghShown, so this reads no refusal path")
	}
	for _, key := range slices.Sorted(maps.Keys(paths)) {
		if len(ghCanaryRows[key]) == 0 {
			t.Errorf("the refusal path %s (%s) has no row in ghCanaryRows: a new path joins the table", key, paths[key])
		}
	}
	for _, key := range slices.Sorted(maps.Keys(ghCanaryRows)) {
		if _, ok := paths[key]; !ok {
			t.Errorf("ghCanaryRows names the refusal path %s, which no call of ghShown in the source has", key)
		}
	}
	root, defaultDir := gitCredentialBundle(t)
	help, err := filepath.Abs(ghHelpDir)
	if err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(stub, []byte(ghStandInPrefix(help)+"printf 'gh ran %s\\n' \"$*\"\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	pane := map[string]string{
		"LEGION_GH_PATH": stub, "GH_CONFIG_DIR": defaultDir, "LEGION_GH_OWNERS_DIR": root,
		"PATH": ghWrapperDir(t) + ":" + os.Getenv("PATH"), "PUBLIC_LEAK_CHECK": leakguardtest.Write(t, t.TempDir(), ghCheckPasses),
	}
	rows, ran := 0, 0
	for _, key := range slices.Sorted(maps.Keys(ghCanaryRows)) {
		for i, row := range ghCanaryRows[key] {
			rows++
			t.Run(key+"/"+strconv.Itoa(i), func(t *testing.T) {
				err := ghCanaryRefusal(row)
				if err == nil {
					t.Fatalf("%v with %v = nil error, want a refusal by the path %s", row.argv, row.env, key)
				}
				var stderr bytes.Buffer
				printGhRefusal(&stderr, err)
				if strings.Contains(stderr.String(), ghShownCanary) {
					t.Errorf("the path %s: legion gh printed %q, which holds the canary", key, stderr.String())
				}
				if !strings.Contains(stderr.String(), row.names) {
					t.Errorf("the path %s: legion gh printed %q, want it to name %q", key, stderr.String(), row.names)
				}
				if row.help != nil || row.remotes != nil || row.run != nil {
					return
				}
				ran++
				env := maps.Clone(pane)
				maps.Copy(env, row.env)
				code, stdout, stderrOf := legionGh(t, env, row.argv...)
				if code != 2 || strings.Contains(stdout, ghShownCanary) || strings.Contains(stderrOf, ghShownCanary) || !strings.Contains(stderrOf, row.names) {
					t.Errorf("the path %s: legion gh %q = exit %d, stdout %q, stderr %q; want exit 2 naming %q, the canary in neither", key, row.argv, code, stdout, stderrOf, row.names)
				}
			})
		}
	}
	t.Logf("%d refusal paths, %d rows, %d of them run as legion gh too", len(paths), rows, ran)
}

// ghCanaryRefusal is the refusal row makes: its own run's, or ghTargetOf's of its argv and env.
func ghCanaryRefusal(row ghCanaryRow) error {
	if row.run != nil {
		return row.run()
	}
	help, remotes := row.help, row.remotes
	if help == nil {
		help = savedGhHelp
	}
	if remotes == nil {
		remotes = noGitRepository
	}
	_, err := ghTargetOf(row.argv, row.env, help, remotes, ownersOwnDirectories)
	return err
}

// ghFormattedValues are the places in the package's non-test gh*.go files that quote or format a
// value themselves, without ghShown, each keyed as ghFormatSites keys it and given the reason it is
// no value a person typed in a slot a refusal names: a name gh's own help or this command's tables
// gave, a position, a kind built from literals, a configured path. A new such place fails
// TestNoRefusalFormatsAPersonsValueItself until it joins this table, which is where a reviewer
// reads it.
var ghFormattedValues = map[string]string{
	"ghShown: strconv.Quote(value)":                                       "the helper itself: a value its slot's strict form admits",
	"ghCallLogValue: strconv.Quote(value)":                                "the call log's fields (role, issue, a placed repository, gh's command path, a visibility), no refusal",
	"appendGhCallLog: %s ghCallLogValue(commandPath)":                     "the call log's field, quoted by ghCallLogValue",
	"appendGhCallLog: %s ghCallLogValue(env[\"LEGION_ISSUE\"])":           "the call log's field, quoted by ghCallLogValue",
	"appendGhCallLog: %s ghCallLogValue(env[\"LEGION_ROLE\"])":            "the call log's field, quoted by ghCallLogValue",
	"appendGhCallLog: %s ghCallLogValue(repo)":                            "the call log's field, quoted by ghCallLogValue",
	"appendGhCallLog: %s ghCallLogValue(visibility)":                      "the call log's field, quoted by ghCallLogValue",
	"appendGhCallLog: %s time.Now().UTC().Format(time.RFC3339Nano)":       "a clock reading",
	"cannotPlace: %s advice":                                              "this command's own advice, a literal",
	"cannotPlace: %s name":                                                "its callers' name, each held in this table or a ghShown call or a literal",
	"cannotPlace: %s why":                                                 "its callers' reason, built from literals and help-listed names",
	"ghArgs.set: %q \"--\"+name":                                          "an option the leaf's help lists, by the help's name",
	"ghArgs.set: %s why":                                                  "a reason of ghRefusedOptions, a table",
	"ghCloneGitFlags: %q name":                                            "a git clone flag of ghCloneGitFlagTakes, by the table's spelling",
	"ghCloneGitFlags: %s ghCloneGitFlagList()":                            "the flags ghCloneGitFlagTakes holds",
	"ghCloneGitFlags: %s leaf (2 places)":                                 "the leaf's command path, as gh's help spelled it",
	"ghCommand.addFlag: %q strings.TrimSpace(line) (5 places)":            "a line of gh's own help, which gh printed for no argument of the call",
	"ghCommand.addFlag: %s match[1]":                                      "a short option of gh's own help",
	"ghCommand.addFlag: %s match[2]":                                      "an option of gh's own help",
	"ghCommand.addFlag: %s prior":                                         "an option of gh's own help",
	"ghCommand.mayRead: %q word (2 places)":                               "a gh alias or extension gh's own help lists",
	"ghCommand.parse: cannotPlace(strconv.Quote(\"--\"+name)) (4 places)": "an option the leaf's help lists, by the help's name",
	"ghCommand.parse: strconv.Quote(\"--\"+name) (4 places)":              "an option the leaf's help lists, by the help's name",
	"ghConfigShape: %s key":                                               "a key of ghRoutingKeys, a table",
	"ghConfigShape: %s path (8 places)":                                   "a file of the gh directory GH_CONFIG_DIR names, a configured path",
	"ghReentryRefusal: %s real":                                           "LEGION_GH_PATH, a configured path",
	"ghRefusedCommand: %s refused":                                        "a command path of a table of refused commands",
	"ghRefusedCommand: %s why":                                            "a reason of that table",
	"ghRemote.what: %q r.name":                                            "a git remote's name, which git config holds and the refusals of remotes name",
	"ghRemote.what: %s r.name":                                            "a git remote's name, which git config holds and the refusals of remotes name",
	"ghRemote.what: %s remoteShown(r.url)":                                "a remote's URL through remoteShown, the URL rule's",
	"ghRemote.what: %s resolved":                                          "a gh-resolved marker through remoteShown, the URL rule's",
	"ghRepoGitConfigRefusal: %s key (8 places)":                           "a git config key, which printGhRefusal passes through the URL rule",
	"ghRepoGitConfigRefusal: %s origin (4 places)":                        "the config file git names for the key, a path",
	"ghRepoGitConfigRefusal: %s strings.TrimSpace(stderr.String())":       "git's own error output, which printGhRefusal passes through the URL rule",
	"ghRepoGitConfigRefusal: %v err":                                      "git's exit status",
	"ghTargetOf: %s name (2 places)":                                      "the name of the token variable set, GH_TOKEN or GITHUB_TOKEN",
	"ghWalk.check: %s path":                                               "a command path of ghRunsAnotherProgram, a table",
	"ghWrapperPath: %s wrapper (2 places)":                                "the wrapper's own path on PATH",
	"ghWrapperPath: %v err":                                               "the error of resolving that path",
	"gitLines: %s strings.Join(args, \" \")":                              "this command's own git arguments",
	"gitLines: %s strings.TrimSpace(stderr.String())":                     "git's own error output, which printGhRefusal passes through the URL rule",
	"gitLines: %v err":                                                    "git's exit status",
	"ordinalArgument: %s suffix":                                          "an ordinal's suffix",
	"parseGhHelp: %q entry (2 places)":                                    "a line of gh's own help",
	"parseGhHelp: %q placeholder":                                         "a usage placeholder of gh's own help",
	"parseGhHelp: %q strings.TrimSpace(line)":                             "a line of gh's own help",
	"parseGhHelp: %s section (3 places)":                                  "a section heading of gh's own help",
	"parseGhHelp: %s strings.Join(cmd.path, \" \")":                       "the command path gh's help names",
	"parseGhHelp: %s strings.Join(slices.Sorted(maps.Keys(ghPassedOnPlaceholders)), \", \")": "a table's placeholders",
	"parseGhHelp: %s what (9 places)":                                       "the command as ghWalk.named names it, help-listed words or places",
	"printGhRefusal: %s shown.String()":                                     "the refusal through the URL rule",
	"readGhCommand: %s what":                                                "the command as ghWalk.named names it, help-listed words or places",
	"readGhHelp: %s bound (2 places)":                                       "a duration",
	"readGhHelp: %v context.Cause(ctx) (2 places)":                          "the cause of a stop: a signal or a deadline",
	"readGhHelp: %v err":                                                    "gh's exit status",
	"readGitRemotes: %q name":                                               "a git remote's name, the text before the tab of git remote -v's line",
	"refuseLegionGhAsRealGh: %q gh":                                         "LEGION_GH_PATH, a configured path, no argument of the call",
	"refuseLegionGhAsRealGh: %s gh":                                         "LEGION_GH_PATH, a configured path, no argument of the call",
	"runGh: %s ghUsage":                                                     "this command's usage, a literal",
	"runGh: %s name":                                                        "the name of an environment variable",
	"runGhChild: %s gh (3 places)":                                          "LEGION_GH_PATH, a configured path",
	"checkedOwner: cannotPlace(kind)":                                       "a slot's kind, built from literals and a help-listed option name",
	"repositoryTarget: cannotPlace(shown) (5 places)":                       "ghShown's words for the value, with the repository's strict form",
	"selectors.dir: %s filepath.Base(dir)":                                  "an owner's directory, a login the bundle holds",
	"selectors.dir: %s strings.Join(listed, \" and \")":                     "each owner and the slots naming it (ghNamed.shown), logins and kinds",
	"selectors.dir: %s strings.Join(routed[dir], \", \")":                   "each slot naming that directory (ghNamed.shown)",
	"selectors.fromAPI: cannotPlace(ordinalArgument(2))":                    "a position",
	"selectors.fromCommand: %s leaf":                                        "the leaf's command path, as gh's help spelled it",
	"selectors.fromCommand: %s unknown":                                     "a usage placeholder of gh's own help",
	"selectors.fromCommand: cannotPlace(ordinalArgument(i+1))":              "a position",
	"selectors.fromCommand: cannotPlace(ordinalArgument(len(cmd.slots)+1))": "a position",
	"selectors.fromOptions: cannotPlace(kind)":                              "a slot's kind, built from literals and a help-listed option name",
	"selectors.fromOptions: cannotPlace(strconv.Quote(\"--\"+name))":        "an option the leaf's help lists, by the help's name",
	"selectors.fromOptions: strconv.Quote(\"--\"+name)":                     "an option the leaf's help lists, by the help's name",
	"selectors.fromWorkingDirectory: %s advice (2 places)":                  "this command's own advice, a literal",
	"selectors.fromWorkingDirectory: %s remote.what()":                      "a remote as ghRemote.what names it",
	"selectors.fromWorkingDirectory: %s why":                                "a reason built from literals",
	"selectors.fromWorkingDirectory: %v err":                                "a git read's error, which printGhRefusal passes through the URL rule",
}

// ghFormatSites is every place in the package's non-test gh*.go files that quotes or formats a
// value without ghShown: each strconv.Quote call (`fn: strconv.Quote(<arg>)`), each argument a
// %q verb of an fmt call's literal format formats (`fn: %q <arg>`), and each cannotPlace call whose
// name is neither a ghShown call nor a literal (`fn: cannotPlace(<name>)`), fn the function that
// holds it, a method by its receiver's type.
func ghFormatSites(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("gh*.go")
	if err != nil {
		t.Fatal(err)
	}
	sites, found := map[string]string{}, map[string][]string{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, name, source, 0)
		if err != nil {
			t.Fatal(err)
		}
		text := func(node ast.Node) string {
			return string(source[fset.Position(node.Pos()).Offset:fset.Position(node.End()).Offset])
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			owner := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				recv := fn.Recv.List[0].Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				if ident, ok := recv.(*ast.Ident); ok {
					owner = ident.Name + "." + owner
				}
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				at := fset.Position(call.Pos()).String()
				add := func(key string) { found[key] = append(found[key], at) }
				switch callee := text(call.Fun); {
				case callee == "strconv.Quote" && len(call.Args) == 1:
					add(owner + ": strconv.Quote(" + text(call.Args[0]) + ")")
				case callee == "cannotPlace" && len(call.Args) > 0:
					first := call.Args[0]
					if _, literal := first.(*ast.BasicLit); literal {
						break
					}
					if inner, ok := first.(*ast.CallExpr); ok && text(inner.Fun) == "ghShown" {
						break
					}
					add(owner + ": cannotPlace(" + text(first) + ")")
				case strings.HasPrefix(callee, "fmt.") && len(call.Args) > 0:
					formatAt := 0
					if callee == "fmt.Fprintf" {
						formatAt = 1
					}
					if formatAt >= len(call.Args) {
						break
					}
					literal, ok := call.Args[formatAt].(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						break
					}
					format, err := strconv.Unquote(literal.Value)
					if err != nil {
						break
					}
					for i, verb := range formatVerbs(format) {
						arg := formatAt + 1 + i
						if arg >= len(call.Args) || !strings.ContainsRune("sqv", verb) {
							continue
						}
						if _, literal := call.Args[arg].(*ast.BasicLit); literal {
							continue
						}
						if inner, ok := call.Args[arg].(*ast.CallExpr); ok && slices.Contains([]string{"ghShown", "ordinalArgument"}, text(inner.Fun)) {
							continue
						}
						add(owner + ": %" + string(verb) + " " + text(call.Args[arg]))
					}
				}
				return true
			})
		}
	}
	for key, at := range found {
		if len(at) > 1 {
			key += " (" + strconv.Itoa(len(at)) + " places)"
		}
		sites[key] = strings.Join(at, ", ")
	}
	return sites
}

// formatVerbs is the verb of each argument format formats, in order; %% takes none.
func formatVerbs(format string) []rune {
	var verbs []rune
	runes := []rune(format)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '%' {
			continue
		}
		i++
		for i < len(runes) && strings.ContainsRune("+-# 0123456789.[]*", runes[i]) {
			i++
		}
		if i < len(runes) && runes[i] != '%' {
			verbs = append(verbs, runes[i])
		}
	}
	return verbs
}

// No refusal of legion gh formats a person's value itself: every place in the source that quotes
// or formats a value without ghShown (ghFormatSites) is one ghFormattedValues holds, with the
// reason its value is none a person typed, and every place that table holds is still in the
// source. A place added to the code, such as a cannotPlace(strconv.Quote(word), …) of a search
// word, fails here naming its function and its argument. A source holding no such place at all
// is refused: the walk would have read nothing.
func TestNoRefusalFormatsAPersonsValueItself(t *testing.T) {
	sites := ghFormatSites(t)
	if len(sites) == 0 {
		t.Fatal("UNKNOWN: the source holds no strconv.Quote call, no q verb and no cannotPlace name, so this read nothing")
	}
	for _, key := range slices.Sorted(maps.Keys(sites)) {
		if _, ok := ghFormattedValues[key]; !ok {
			t.Errorf("%s formats a value itself, at %s, which ghFormattedValues does not hold: print a person's value through ghShown, or name why this one is none", key, sites[key])
		}
	}
	for _, key := range slices.Sorted(maps.Keys(ghFormattedValues)) {
		if _, ok := sites[key]; !ok {
			t.Errorf("ghFormattedValues holds %s, which the source no longer has", key)
		}
	}
	t.Logf("%d places format a value without ghShown, each reviewed", len(sites))
}
