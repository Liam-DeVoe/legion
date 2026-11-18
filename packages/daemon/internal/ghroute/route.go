// Package ghroute reads which repositories a gh command acts on, as gh itself resolves them, and
// routes them to the one gh directory of a Legion credential bundle that answers for them all
// (DirFor), so the caller runs gh with that directory and no other: `legion gh`, the gh every
// pane and pod runs, and the daemon's own gh tool for an agent that is not running. DirFor is
// also the pick `legion git-credential` answers git with. The package also says whether the
// command writes nothing, by a rule proven against the shared leak check the worker image pins
// (Target.WritesNothing, ReadsProvenCheckSHA256).
package ghroute

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sjawhar/legion/daemon/internal/leakguard"
)

// Target is what TargetOf reads off a gh command: Dir, the gh directory every selector of the
// command is routed to (selectors.dir), and "" for a command naming no owner, whose token stays
// the one GH_CONFIG_DIR already names; Owner, the lowercased owner of the repository it acts on,
// "" where the selectors named no owner or two; Repo, that repository's name keyed as gh reads a
// name of it (ghconfig.RepositoryName) where every selector of the command named that one
// repository of that owner, and "" where they named more than one, where one named the owner
// alone, and where none named a repository at all; Command, the path of the leaf command it runs
// as gh's own help names it (`pr list` for `pr ls`), nil when it runs none (`gh`, a group alone,
// `gh help`, `gh --version`); Files, the files the command would send to GitHub as content
// (sentFiles), for the leak guard a caller runs before gh (leakguard.GhRequest.Files); and
// WritesNothing, whether the call is in a read grammar (writesNothing), false for a help call,
// so that under the shared check that rule was proven against (ReadsProvenCheckSHA256) a
// caller's guard runs that check alone for it (leakguard.GhRequest.WritesNothing). Such a call
// sends no file: no grammar names an option of sentFileOptions
// (TestGhReadGrammarNamesEachOptionAsItsHelpDoes).
type Target struct {
	Dir           string
	Owner         string
	Repo          string
	Command       []string
	Files         []leakguard.GhFile
	WritesNothing bool
}

// sentFileOptions are the options of gh 2.98.0, by long name, whose value names a file gh
// sends to GitHub as content: true where the whole value is the path, and false for `gh api`'s
// --field, whose file is the `@<path>` half of a `<name>=@<path>` value. They are the same four
// plus the field that leakguard's own table holds (its ghFileArgs), read here by their long
// names because the leaf's own help has already said which option each argument is: a `-F` is
// --body-file in `gh pr create`, --notes-file in `gh release create` and --field in `gh api`,
// and a short cluster (`-dF <path>`) and an attached value (`-F=<path>`) are read as pflag
// reads them.
var sentFileOptions = map[string]bool{
	"body-file":  true,
	"notes-file": true,
	"input":      true,
	"from-file":  true,
	"field":      false,
}

// sentFiles are the files the parsed command would send to GitHub as content, each named once
// and each named by the option it came from, for the leak guard to read the text of
// (leakguard.GhRequest.Files). The answer is never nil, so a command that sends no file is a
// command the guard reads no file of, rather than one it reads the argv of again with a table
// of its own.
//
// A value of `-`, and an `@-`, is standard input, which the guard reads through the shared
// check's own handshake and never opens as a file. The options are read in the order their
// names sort, since a parse holds each option's values together and not the argv's order
// across options, and a report that named two files must name them the same way twice.
func sentFiles(parsed ghArgs) []leakguard.GhFile {
	files := []leakguard.GhFile{}
	for _, name := range slices.Sorted(maps.Keys(sentFileOptions)) {
		whole := sentFileOptions[name]
		for _, value := range parsed.values[name] {
			path := value
			if !whole {
				_, after, found := strings.Cut(value, "=")
				if !found || !strings.HasPrefix(after, "@") {
					// A field given a literal value, and `gh project field-list --field <name>`,
					// name no file.
					continue
				}
				path = strings.TrimPrefix(after, "@")
			}
			if path == "" || path == "-" {
				continue
			}
			if slices.ContainsFunc(files, func(file leakguard.GhFile) bool { return file.Path == path }) {
				continue
			}
			files = append(files, leakguard.GhFile{Option: "--" + name, Path: path})
		}
	}
	return files
}

// CallerToken is the first of GH_TOKEN and GITHUB_TOKEN that env sets, in gh's own order, and
// "" where env sets neither. gh sends that token in place of any gh directory's token, so
// TargetOf refuses a call under it, and `legion gh` runs only the guard's own visibility read
// under it (cmd/legion's runGh).
func CallerToken(env map[string]string) string {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if env[name] != "" {
			return name
		}
	}
	return ""
}

// TargetOf reads the Target of the gh command argv (gh's arguments, without `gh`): the gh
// directory it runs with, the repository it acts on, the leaf command it runs, and whether the
// call writes nothing (writesNothing, over argv as typed and the leaf's path). It fails closed:
// whenever it cannot be certain which gh directory the command needs it is an error, which names
// the argument it could not place, and nothing is sent. `gh --version` alone, and a command whose
// last --help is true, name none, since gh only prints.
//
// It reads the command as gh does, from gh's own help of each command on the way (help): the
// path of subcommands down to the leaf the arguments run, every option's arity, and the arguments
// the leaf's USAGE line names. A first word gh's own help does not list, a command that runs
// another program (`gh copilot`, `gh extension exec`), and a word its parent's help lists as a gh
// alias, whose expansion may name a repository of its own, or as an extension, which reads gh's
// token itself, are errors before its help is read, since reading it could run them (ghWalk); so
// is such a word as the target of `gh help`, which for an extension gh runs (ghHelpTarget), and a
// word whose help is another command's that lists no such alias of its own. Before the leaf only
// the inherited -R/--repo and --help/-h may come, read with the leaf's own options; anything else
// there, a subcommand gh does not know, help that lists an option incompletely or not at all, an
// option the leaf's help does not list or names a selector of in a way this does not know, and an
// argument its USAGE has no place for or names in a way this does not know (ghSlotKinds) are
// errors. The leaf's options are read with pflag's rules, and after `--` the rest are its
// arguments, unless its USAGE passes them on (`gh repo clone <repository> [<directory>] [--
// <gitflags>...]`).
//
// Every selector the leaf's help names counts: a `<repository>` (or another repository) argument,
// before `--` or, for a leaf passing the rest on, the first after it; an `<owner>`; a URL argument
// that is a github.com URL (through gh's pr finder, which reads any other as a branch of the
// command's own repository, only a pull request's names its repository); OWNER/REPO#N where the
// leaf reads it (ghLeafQuirks); a search `<query>`'s repo:/owner:/user:/org: qualifiers; `gh api`'s
// endpoint (repos/<owner>/<repo>/…, repos/<owner>/, orgs/<owner>/, users/<owner>/, a search
// endpoint's q) and a GraphQL query's owner variable (`-F owner=<login>`); and the options naming
// a repository, an owner, a team or an issue by URL (ghOptionKinds). So does the command's own
// repository when gh resolves it, because no argument names it and the leaf reads one, or
// whatever names it for a leaf that reads every remote (fromOwnRepository): GH_REPO for a leaf
// that reads it, gh api's :owner and {repo} placeholders among them, and otherwise every
// github.com repository the working directory's git remotes name (remotes), read as gh reads
// them; a command naming no owner at all counts those too. Every repository they name, and every
// owner one names alone — `orgs/<owner>/`, an owner:/org:/user: qualifier, an `<owner>` argument,
// a team, a project, an owner option, since such a command reaches whatever else of that owner's
// gh resolves — is routed through dirFor, the bundle's own pick (DirFor), and they must all route
// to one directory; two are an error (selectors.dir). The target's repository is the one
// repository they all name, which is none where a selector names an owner alone. A GraphQL query
// naming its repository otherwise, by a node id or in the query text, names none gh could
// resolve. GH_HOST naming another host than github.com, where gh would send no token of the
// bundle's, and a GH_TOKEN or GITHUB_TOKEN, which gh sends in place of any directory's token, are
// errors.
func TargetOf(argv []string, env map[string]string, help func(path []string) (string, error), remotes func() ([]Remote, error), dirFor func(owner, name string) (string, error)) (Target, error) {
	if host := env["GH_HOST"]; host != "" && !strings.EqualFold(host, "github.com") {
		return Target{}, fmt.Errorf("GH_HOST is %q, another host than github.com, so it sends no token; unset GH_HOST", host)
	}
	if name := CallerToken(env); name != "" {
		return Target{}, fmt.Errorf("%s is set, and gh sends it in place of the token of the owner the command targets, so it sends no token; unset %s", name, name)
	}
	walk := ghWalk{help: help}
	var rest []string
	var cmd *ghCommand
	for i := 0; i < len(argv) && cmd == nil; i++ {
		arg := argv[i]
		switch {
		case len(walk.path) == 0 && arg == "--version":
			if len(argv) == 1 {
				return Target{}, nil
			}
			// gh reads a word after it as a command, an alias's or an extension's among them.
			return Target{}, cannotPlace(arg, "comes before the command's name, and only -R/--repo and --help may (gh's version is `gh --version` alone)", false)
		case len(walk.path) == 0 && arg == "help":
			return Target{}, ghHelpTarget(argv[i+1:], help)
		case arg == "-R" || arg == "--repo":
			if i+1 == len(argv) {
				return Target{}, cannotPlace(arg, "has no value", false)
			}
			rest = append(rest, arg, argv[i+1])
			i++
		case strings.HasPrefix(arg, "--repo=") || (strings.HasPrefix(arg, "-R") && len(arg) > 2) ||
			arg == "--help" || strings.HasPrefix(arg, "--help=") || arg == "-h":
			// The leaf reads these as its own, in order with the rest of its arguments.
			rest = append(rest, arg)
		case strings.HasPrefix(arg, "-"):
			return Target{}, cannotPlace(arg, "comes before the command's name, and only -R/--repo and --help may", false)
		default:
			if err := walk.check(arg); err != nil {
				return Target{}, err
			}
			read, err := walk.read(arg)
			if err != nil {
				return Target{}, err
			}
			if !read.group {
				cmd = &read
				rest = append(rest, argv[i+1:]...)
			}
		}
	}
	if cmd == nil {
		// No leaf: `gh`, or a group alone, which prints its help.
		return Target{}, nil
	}
	parsed, err := cmd.parse(rest)
	if err != nil {
		return Target{}, err
	}
	if parsed.help {
		// No read grammar names --help or -h (readLeaves, apiReadOptions), so a help call is no
		// call that writes nothing (writesNothing), and a caller's guard reads it whole.
		return Target{Command: cmd.path}, nil
	}
	sel := selectors{api: cmd.isAPI()}
	if err := sel.fromCommand(cmd, parsed); err != nil {
		return Target{}, err
	}
	if err := sel.fromOwnRepository(cmd, parsed, env, remotes); err != nil {
		return Target{}, err
	}
	files := sentFiles(parsed)
	reads := writesNothing(argv, cmd.path)
	dir, err := sel.dir(dirFor)
	if err != nil {
		return Target{}, err
	}
	return Target{Dir: dir, Owner: sel.onlyOwner(), Repo: sel.onlyRepository(), Command: cmd.path, Files: files, WritesNothing: reads}, nil
}

// cannotPlace is TargetOf's refusal of an argument it cannot place: what it is and why, and how
// to name the repository instead, which for gh api is its endpoint (it has no -R).
func cannotPlace(arg, why string, api bool) error {
	advice := "pass -R <owner>/<repo>"
	if api {
		advice = "name it in the endpoint, repos/<owner>/<repo>/…"
	}
	return fmt.Errorf("cannot tell which repository the command targets: %q %s, so it sends no token; %s", arg, why, advice)
}

// ghArgs is a leaf's arguments as pflag reads them: its positional arguments in order, every
// value given each option, by long name, and whether gh only prints the leaf's help.
type ghArgs struct {
	positionals []string
	values      map[string][]string
	help        bool
}

// parse reads args, the arguments of the leaf with its path taken out, as pflag does, refusing an
// option the leaf's help does not list or does not say the arity of. --help is a boolean whose
// last value decides; a -h no option of the leaf takes is pflag's request for help, which ends
// the reading there. After `--` every argument is positional, but for a leaf whose usage passes
// them on (`gh repo clone <repository> [<directory>] [-- <gitflags>...]`), where gh, as cobra
// hands it every positional, takes the first one as the repository even after the dash and passes
// the rest on: so the first argument after `--` is positional when none came before it.
func (c *ghCommand) parse(args []string) (ghArgs, error) {
	api, leaf := c.isAPI(), c.quoted()
	parsed := ghArgs{values: map[string][]string{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			rest := args[i+1:]
			if c.passthrough && len(parsed.positionals) > 0 {
				rest = nil
			} else if c.passthrough && len(rest) > 1 {
				rest = rest[:1]
			}
			parsed.positionals = append(parsed.positionals, rest...)
			return parsed, nil
		case strings.HasPrefix(arg, "--"):
			name, value, hasValue := strings.Cut(arg[2:], "=")
			flag, ok := c.flags[name]
			if !ok {
				return ghArgs{}, cannotPlace(arg, "is no option "+leaf+"'s help lists", api)
			}
			if flag.uncertain {
				return ghArgs{}, cannotPlace(arg, "is an option whose help does not say whether it takes a value", api)
			}
			if !hasValue {
				switch {
				case !flag.value:
					value, hasValue = "true", true
				case flag.optional:
					// Its default, which names nothing.
				case i+1 == len(args):
					return ghArgs{}, cannotPlace(arg, "has no value", api)
				default:
					i++
					value, hasValue = args[i], true
				}
			}
			if hasValue {
				if err := parsed.set(name, flag, value, arg, api); err != nil {
					return ghArgs{}, err
				}
			}
		case strings.HasPrefix(arg, "-") && arg != "-":
			// pflag's shorthands, each in turn: `-f=value` gives any its value; a boolean or an
			// optional value takes its default and leaves the rest of the word to the next
			// shorthand; any other takes the rest of the word, or the next argument.
			for shorts := arg[1:]; shorts != ""; {
				short, rest := shorts[0], shorts[1:]
				name, ok := c.shorts[short]
				if !ok {
					if short == 'h' {
						parsed.help = true
						return parsed, nil
					}
					return ghArgs{}, cannotPlace(arg, fmt.Sprintf("names -%c, which %s's help does not list", short, leaf), api)
				}
				flag := c.flags[name]
				if flag.uncertain {
					return ghArgs{}, cannotPlace(arg, "is an option whose help does not say whether it takes a value", api)
				}
				value, hasValue := "", true
				switch {
				case len(rest) > 1 && rest[0] == '=':
					value, rest = rest[1:], ""
				case !flag.value:
					value = "true"
				case flag.optional:
					hasValue = false
				case rest != "":
					value, rest = rest, ""
				case i+1 < len(args):
					i++
					value = args[i]
				default:
					return ghArgs{}, cannotPlace(arg, "has no value", api)
				}
				if hasValue {
					if err := parsed.set(name, flag, value, arg, api); err != nil {
						return ghArgs{}, err
					}
				}
				shorts = rest
			}
		default:
			parsed.positionals = append(parsed.positionals, arg)
		}
	}
	return parsed, nil
}

// set records value given the option name: --help's as a boolean, any other's taking a value in
// the order given.
func (p *ghArgs) set(name string, flag ghFlag, value, arg string, api bool) error {
	switch {
	case name == "help":
		help, err := strconv.ParseBool(value)
		if err != nil {
			return cannotPlace(arg, "gives --help a value that is no boolean", api)
		}
		p.help = help
	case flag.value:
		p.values[name] = append(p.values[name], value)
	}
	return nil
}

// ReadHelp is the real gh's help of the command path, `gh <path> --help` (`gh --help` for
// gh's own), refused when gh answers it with an error.
func ReadHelp(gh string, path []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, gh, append(slices.Clone(path), "--help")...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gh answered its help with %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() == 0 {
		return "", errors.New("gh printed no help")
	}
	return stdout.String(), nil
}
