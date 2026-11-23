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
