package leakguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// GhRequest is one gh call, as the caller is about to make it.
type GhRequest struct {
	// Args is the whole argv the caller would hand gh, gh's own name aside.
	Args []string
	// Stdin is what the caller would hand gh on its standard input, nil for nothing. It is read
	// only where the shared check asks for it, and what is read comes back from GhCall for the
	// caller to hand gh in its place.
	Stdin io.Reader
	// StdinOption is the option whose value named standard input (`-`, or `@-` for a field), by
	// its long name as gh spells it: `--input`, `--body-file`, `--notes-file`, `--from-file` or
	// `--field`. It says what the input is (stdinRecords): `gh api --input -` makes it the
	// request body, a JSON document GitHub parses, `-F query=@-` in a `gh api graphql` call the
	// query, a GraphQL document GitHub parses, and each other option text GitHub stores as it
	// stands. "" is a caller that does not know. Where such a caller also handed no Files, the
	// guard reads the option out of the argv (ghFileArgs); where nothing knows, it reads the
	// input both ways.
	StdinOption string
	// Repo is the destination repository, as "owner/name". Empty where the caller knows no
	// destination, which the guard reads as public (Guard.scanThrough).
	Repo string
	// Files are the files this call would send to GitHub as content, read by a caller that
	// parses the argv as gh does. `legion gh` is that caller: it reads the command's own help
	// for every option's arity before it reaches the guard (cmd/legion's ghTargetOf), so it
	// knows a `-dF <file>` and a `-F=<file>` for what they are, which a reader of the argv
	// alone cannot.
	//
	// Nil is a caller that parsed none, and the guard then reads the argv with a table of its
	// own (ghFileArgs). `legion gh` hands nil only from the answers of its parse that send
	// nothing (`--version`, a group with no leaf, `--help`), so in this binary the table is the
	// fail-closed answer for a future caller that hands the guard bare arguments, not a path
	// any write takes today. An empty, non-nil slice is a caller that parsed and found no file,
	// and the table is not consulted for it.
	Files []GhFile
	// WritesNothing is a call its caller found sends no text into any repository: a read, which
	// `legion gh` admits only by a grammar in its code of the words the call is typed with, one
	// the shared check reads as a read too (cmd/legion's ghWritesNothing holds the rule). Where
	// the resolved check is the one that rule was proven against (ProvenCheck), the guard runs
	// the shared check, which refuses a credential in every call it reads as a read, and skips
	// what only a write needs: the destination's visibility read and the private-name pass, which
	// protect the text a write leaves in a public repository. Under any other check the call gets
	// the whole guard.
	WritesNothing bool
	// ProvenCheck is the sha256, in hex, of the shared check's public_leak_check.py that the
	// caller's rule for WritesNothing was proven against. WritesNothing is honoured only where
	// the public_leak_check.py beside the resolved check hashes to it (provenCheck), so a check
	// of another version, or one edited under the same tag, gets the whole guard.
	ProvenCheck string
}

// GhAnswer is what GhCall cleared a call with.
type GhAnswer struct {
	// Stdin is the standard input the guard read, for the caller to hand gh in its place, and nil
	// where it read none and the caller's own is still whole.
	Stdin []byte
	// CheckedOnly is whether the shared check was the whole of the guard (GhRequest.WritesNothing
	// honoured): the call read no visibility and had no private-name step.
	CheckedOnly bool
	// NoRules is whether the call skipped the private-name step because no rule is named: it read
	// no visibility and no file the call sends.
	NoRules bool
}

// checkProgram is the shared check's program, beside the script Resolve finds, whose sha256 says
// which check is installed: the script only runs it.
const checkProgram = "public_leak_check.py"

// provenCheck says whether the check Resolve found is the one proven names: the sha256 of the
// public_leak_check.py beside it, the script's own directory once its symbolic links are
// followed, is proven. A check with no such file, one whose file cannot be read, and one whose
// file holds anything else are not, and neither is any check where proven is empty.
func provenCheck(check, proven string) bool {
	if proven == "" {
		return false
	}
	dir := filepath.Dir(check)
	if followed, err := filepath.EvalSymlinks(check); err == nil {
		dir = filepath.Dir(followed)
	}
	program, err := os.Open(filepath.Join(dir, checkProgram))
	if err != nil {
		return false
	}
	defer program.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, program); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(sum.Sum(nil)), proven)
}

// whatGhCall is what a refusal and a setup failure of this shape call the write. Both halves of
// the check — the first call and the one that follows it with the buffer — report the one call.
const whatGhCall = "the gh call"

// GhCall checks a gh call before it is made, in the shared check's gh-guard shape, and answers
// the standard input it read, whether the shared check was the whole of it, which it is for a call
// that writes nothing under the check its caller's rule was proven against
// (GhRequest.WritesNothing, GhRequest.ProvenCheck), and whether it skipped the private-name step
// because no rule is named.
//
// The check reads a call that takes its body on standard input in two goes: the first call exits
// 4, meaning "this call reads standard input; buffer it and ask me again", and the second call is
// handed the buffer. The buffer is a regular file, never a pipe: the check reads it twice, and
// the check itself refuses a pipe for that reason. The bytes come back so the caller can hand
// them to gh, because by then this package has read the caller's standard input and the caller
// cannot read it again; a nil Stdin means standard input was never read and the caller's own is
// still whole.
func (g *Guard) GhCall(ctx context.Context, req GhRequest) (GhAnswer, error) {
	var answer GhAnswer
	check, err := g.Resolve()
	if err != nil {
		return answer, &SetupFailure{What: whatGhCall, Why: whyNoCheck, Err: err}
	}
	argv := append([]string{check, "gh-guard"}, req.Args...)
	status, printed, err := g.run(ctx, child{argv: argv})
	if err != nil {
		return answer, sharedNotRun(whatGhCall, err, printed)
	}
	if status == checkWantsStdin {
		answer.Stdin, status, printed, err = g.askAgain(ctx, argv, req)
		if err != nil {
			return answer, err
		}
	}
	if err := readStatus(whatGhCall, status, printed); err != nil {
		return answer, err
	}
	if req.WritesNothing && provenCheck(check, req.ProvenCheck) {
		// A read leaves no text in any repository, so neither the destination's visibility nor
		// the private-name pass is read for it: the check above, which refuses a credential in a
		// call it reads as a read, is all it needs. Under a check its caller's rule was not
		// proven against, the check may read the call as a write, so the guard goes on whole.
		answer.CheckedOnly = true
		return answer, nil
	}
	rules, err := g.rules(whatGhCall)
	if err != nil {
		return answer, err
	}
	if rules == nil {
		answer.NoRules = true
		return answer, nil
	}
	public, err := g.scanThrough(ctx, whatGhCall, req.Repo)
	if err != nil {
		return answer, err
	}
	if !public {
		return answer, nil
	}
	// What the call would send, read here and not before the visibility answer: a private or
	// internal destination runs no rule pass, and reading its files would cost the opens for
	// nothing. The files, and the option that named standard input, are the caller's parse where
	// it handed one and the argv's table where it did not.
	files, stdinOption := req.Files, req.StdinOption
	if files == nil {
		if upload, found := ghUnreadUploadArg(req.Args); found {
			return answer, &SetupFailure{
				What: whatGhCall,
				Why:  upload + " uploads the file it names, which this guard does not read, so the call sends nothing; write the text in --body or --body-file, which it reads",
			}
		}
		var named string
		files, named = ghFileArgs(req.Args)
		if stdinOption == "" {
			stdinOption = named
		}
	}
	graphQL := ghAPIGraphQL(req.Args)
	records := ghArgRecords(req.Args, graphQL)
	if len(answer.Stdin) > 0 {
		stdin := Record{Label: "the gh call's standard input", Text: string(answer.Stdin)}
		records = append(records, stdinRecords(stdin, stdinOption, req.Args, graphQL)...)
	}
	for _, named := range files {
		text, err := readGhFile(named)
		if err != nil {
			return answer, err
		}
		record := Record{Label: named.Path, Text: text}
		switch {
		case graphQL && named.Field == graphQLQuery:
			records = append(records, graphQLRecords(record)...)
		case named.Option == inputOption:
			records = append(records, jsonBody(record, graphQL)...)
		default:
			records = append(records, record)
		}
	}
	return answer, rules.Check(ctx, whatGhCall, records...)
}

// ghArgRecords is the records of args, a gh call's argv: one of each argument, and none that
// joins two. An argument is "the gh call's argument <i>", i its index in args, and holds the
// argument as it was written. A field, the `<name>=<value>` given to -f, --raw-field, -F or
// --field in any spelling ghFieldArg reads (`-f name=value`, `-fname=value`,
// `--raw-field=name=value`, and in `gh api` a short cluster's `-iF name=value`), is two
// records: "the gh call's field <name>", which holds the value after the first `=`, and "the gh
// call's field <name> (name)", which holds the name. A call's fields are values GitHub stores
// apart — the title and the body of `gh api …/issues -f title=… -f body=…`, a gist's file name
// in `-f 'files[<name>][content]=…'` — and a rule that needs two words on one line matches only
// where one of them holds both. The joined argv would put every field on one line.
//
// A field's value is the literal string it is: gh does the JSON escaping, so a `\u0074` written
// in an argument reaches GitHub's REST API as those six characters, and the rules read what
// GitHub would show. The one field that is not is a GraphQL query, `query` in a `gh api
// graphql` call (graphQL), whose literals GitHub's own parser decodes: it is read one literal at
// a time (graphQLRecords). A `-F query=@<path>` reads the query from the file, which is read so
// (GhFile.Field), and a `-F query=@-` from standard input, which is read so too (stdinRecords);
// the field's own value is then the path or the `@-`, read as written.
//
// The reader knows no option's arity but the four fields', which is the direction it errs in:
// an argument it takes for a field is read as a name and a value, and one it takes for anything
// else is read whole. Arguments past a bare `--` are read as arguments, with no field among
// them: gh sends them to nobody, and reading them costs nothing.
func ghArgRecords(args []string, graphQL bool) []Record {
	records := make([]Record, 0, len(args))
	argument := func(i int) {
		records = append(records, Record{Label: "the gh call's argument " + strconv.Itoa(i), Text: args[i]})
	}
	api := len(args) > 0 && args[0] == "api"
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			for ; i < len(args); i++ {
				argument(i)
			}
			break
		}
		option, value, attached := ghFieldArg(args[i], api)
		if option == "" {
			argument(i)
			continue
		}
		if !attached {
			argument(i)
			if i+1 == len(args) {
				break
			}
			i++
			value = args[i]
		}
		name, text, found := strings.Cut(value, "=")
		if !found {
			// `-f <name>` with no value is gh's empty array (`key[]`), or the value of some
			// other command's option: either way the argument is read whole.
			argument(i)
			continue
		}
		label := "the gh call's field " + name
		records = append(records, Record{Label: label + " (name)", Text: name})
		field := Record{Label: label, Text: text}
		fromFile := (option == "-F" || option == "--field") && strings.HasPrefix(text, "@")
		if graphQL && name == graphQLQuery && !fromFile {
			records = append(records, graphQLRecords(field)...)
			continue
		}
		records = append(records, field)
	}
	return records
}

// ghFieldArg reads arg as a field option of gh's: the option, -f, --raw-field, -F or --field;
// its value where arg holds it (attached), as `--field=<value>`, `-F<value>` and, as pflag reads
// a short option, `-F=<value>` do; and "" for an argument that is no field option.
//
// A short option is read at the front of arg alone, but in a `gh api` call (api), whose short
// options are all known (ghAPIShortValueOptions). There a short cluster is read as pflag reads
// one: the letters before the first that takes a value take none (`-i` is `--include`), and that
// letter ends the cluster, taking the rest of arg, an `=` before it dropped, or the next argument
// where it is the last letter. So `-iF query=<v>`, `-iFquery=<v>` and `-if query=<v>` are fields,
// and a cluster whose value letter is any other (`-iX POST`) is no field option. Every other
// command's letters mean other things — `-dF` is `--draft --body-file` in `gh pr create` and
// `--desc F` in `gh gist create` — which only the leaf's own help tells apart.
func ghFieldArg(arg string, api bool) (option, value string, attached bool) {
	switch {
	case arg == "--raw-field" || arg == "--field":
		return arg, "", false
	case strings.HasPrefix(arg, "--raw-field=") || strings.HasPrefix(arg, "--field="):
		option, value, _ = strings.Cut(arg, "=")
		return option, value, true
	case len(arg) < 2 || arg[0] != '-' || arg[1] == '-':
		return "", "", false
	}
	letter := 1
	if api {
		letter += strings.IndexAny(arg[1:], ghAPIShortValueOptions)
	}
	switch {
	case letter == 0:
		return "", "", false
	case arg[letter] == 'f':
		option = "-f"
	case arg[letter] == 'F':
		option = "-F"
	default:
		return "", "", false
	}
	if letter == len(arg)-1 {
		return option, "", false
	}
	return option, strings.TrimPrefix(arg[letter+1:], "="), true
}

// stdinRecords is the records of record, the call's buffered standard input, by the option that
// named it (GhRequest.StdinOption). Named by `--input`, it is the request body, read as JSON
// (jsonBody). Named by any other option — `--body-file -`, `--notes-file -`, `--from-file -`,
// `-F <name>=@-` — it is text GitHub stores as it stands, one record: gh makes one JSON string
// of a field's bytes itself, and a body option's input is the body. Named by nothing known, it
// is read both ways, the raw record and the strings of a JSON document beside it, which errs
// toward reading more.
//
// The one field whose input is not text is the query of a `gh api graphql` call (graphQL): gh
// sends the bytes of `-F query=@-` as the query, which GitHub parses, so they are read one
// literal at a time (graphQLRecords), as the same query is from a file. Where a field named the
// input of such a call — the option is `--field` or `-F`, or nothing known and args, the argv,
// names a field of standard input — the input is read by the names of the fields whose value is
// `@-` (ghStdinFields). All of them `query` is the query alone, with no raw record, which would
// join its literals on one line. Some `query` and some not is a call whose one input goes to one
// field, which gh's order of reading decides and the guard does not guess: the raw record and
// the literals are both read. None `query` is text, as above.
func stdinRecords(record Record, option string, args []string, graphQL bool) []Record {
	if graphQL {
		if names := ghStdinFields(args); option == "--field" || option == "-F" || option == "" && len(names) > 0 {
			if !slices.Contains(names, graphQLQuery) {
				return []Record{record}
			}
			literals := graphQLRecords(record)
			if !slices.ContainsFunc(names, func(name string) bool { return name != graphQLQuery }) {
				return literals
			}
			if len(literals) == 1 && literals[0] == record {
				// A query that does not lex is read as written already.
				return literals
			}
			return append([]Record{record}, literals...)
		}
	}
	switch option {
	case inputOption:
		return jsonBody(record, graphQL)
	case "":
		fields, _ := jsonFields(record, graphQL)
		return append([]Record{record}, fields...)
	}
	return []Record{record}
}

// ghStdinFields are the names of the fields of args, a gh call's argv, whose value is standard
// input: each `-F <name>=@-` and `--field <name>=@-`, in every spelling ghFieldArg reads, before a
// bare `--` and in the order the argv writes them. A -f or --raw-field sends `@-` as those two
// characters and reads no input, so it names none.
func ghStdinFields(args []string) []string {
	api := len(args) > 0 && args[0] == "api"
	var names []string
	for i := 0; i < len(args) && args[i] != "--"; i++ {
		option, value, attached := ghFieldArg(args[i], api)
		if option == "" {
			continue
		}
		if !attached {
			if i+1 == len(args) {
				break
			}
			i++
			value = args[i]
		}
		name, after, found := strings.Cut(value, "=")
		if found && after == "@-" && (option == "-F" || option == "--field") {
			names = append(names, name)
		}
	}
	return names
}

// jsonBody is the records of record, a request body GitHub parses as JSON: the strings of the
// document where its text is exactly one (jsonFields), and record itself, raw, where it is not.
// A text that is not one document is no body GitHub parses, and is read as the one record it
// was: a Markdown body named by `--input` by mistake is still read.
//
// Two of a gh call's records are such a body: `gh api --input <file>`, whose whole file is the
// request body, and standard input named by `gh api --input -`, which the shared check's exit-4
// handshake has buffered. A `-F <name>=@<path>` field's file is not, since gh makes one JSON
// string of its bytes, escaping them itself: the file holds the text GitHub shows.
func jsonBody(record Record, graphQL bool) []Record {
	if fields, ok := jsonFields(record, graphQL); ok {
		return fields
	}
	return []Record{record}
}

// jsonFields is the records of the strings record's text holds where that text is exactly one
// JSON document, and false where it is not: no JSON at all, a document with more after it, or
// two documents. A string is one record, an object's names and values alike, labelled
// "<record>, JSON <path>" and a name "<record>, JSON <path> (name)", the path written as jq
// writes it (`.body`, `.comments[0].body`, `."a b"`, and `.` for the document itself) within the
// bounds jsonLabel keeps. An empty string makes no record: it holds no text a rule can match.
//
// A body is read one string at a time because GitHub stores it one string at a time: a review's
// body and each of its comments' bodies are texts of their own, shown apart, and a rule that
// needs two words on one line (`<name>[^\n]*<word>`) matches only where one stored text holds
// both. A compact document puts every field on one line, so a raw read of it would join a name
// in the review's body to an unrelated word in its fifth comment, and refuse a write that
// publishes neither beside the other.
//
// No raw read of one document is needed beside its strings. Every text GitHub stores from a JSON
// body is a decoded string value, and the bytes between the strings — the punctuation, the
// numbers, `true`, `false`, `null` and the whitespace — hold none: a number and the three
// literals are values of the schema's, stored as no text of the caller's, and make no record. The
// decoded string is also the one a rule must read: a string's escapes hide its text from the
// rules, `\u0074` being a letter no pattern reads in the bytes, and an escape inside a name parts
// the halves of a word. The document is read as tokens rather than as a value, so every value of
// a duplicate key is read, though a parser keeps one of them: which one is GitHub's parser's to
// say, and the guard does not guess.
//
// In a `gh api graphql` call (graphQL), the document's top-level `query` is a GraphQL query, and
// is read one literal at a time (graphQLRecords), labelled "<record>, JSON .query, GraphQL string
// <k>".
func jsonFields(record Record, graphQL bool) ([]Record, bool) {
	decoder := json.NewDecoder(strings.NewReader(record.Text))
	// A number is kept as its text rather than parsed, so that one past a float64's range is a
	// number still, and not an error that sends the document to the raw read.
	decoder.UseNumber()
	var fields []Record
	// open are the arrays and objects the next token stands inside, the outermost first, and path
	// the steps from the document to the innermost of them, one for each but the document itself.
	// A label is rendered from path when its string is read (jsonLabel), so that no string's
	// label costs the length of the path it stands at.
	var open []jsonOpen
	var path []string
read:
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		// at is the path of the value token is or opens: path and, inside an array or an object,
		// the step to that value.
		at := path
		if n := len(open); n > 0 {
			inner := &open[n-1]
			switch {
			case token == json.Delim('}') || token == json.Delim(']'):
				open = open[:n-1]
				if len(open) == 0 {
					break read
				}
				path = path[:len(path)-1]
				continue
			case inner.array:
				at = append(path, "["+strconv.Itoa(inner.next)+"]")
				inner.next++
			case inner.member == "":
				// The decoder hands an object nothing but a string where a name stands.
				name, _ := token.(string)
				inner.member = jqMember(name)
				if name != "" {
					fields = append(fields, Record{Label: jsonLabel(record.Label, append(path, inner.member)) + " (name)", Text: name})
				}
				continue
			default:
				at = append(path, inner.member)
				inner.member = ""
			}
		}
		switch value := token.(type) {
		case json.Delim:
			if len(open) > 0 {
				path = at
			}
			open = append(open, jsonOpen{array: value == '['})
			continue
		case string:
			if value == "" {
				break
			}
			field := Record{Label: jsonLabel(record.Label, at), Text: value}
			if graphQL && len(at) == 1 && at[0] == "."+graphQLQuery {
				fields = append(fields, graphQLRecords(field)...)
			} else {
				fields = append(fields, field)
			}
		}
		if len(open) == 0 {
			break
		}
	}
	// The document is the whole text only where what follows it is whitespace to the end.
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return fields, true
}

// jsonOpen is an array or an object of a JSON document being read (jsonFields): for an array,
// the index of its next element; and for an object, the step to the member whose value comes
// next (jqMember), "" where a name comes next.
type jsonOpen struct {
	array  bool
	next   int
	member string
}

// The bounds of a JSON path in a label (jsonLabel), so that a label stays short whatever the
// document is: a body can nest thousands of objects, and a label holding the whole path would
// cost each string of such a body the length of its path, which for the whole body grows with the
// square of its depth. A path of more than maxPathSteps steps is shown as its first
// pathStepsShownBefore steps, "…", and its last pathStepsShownAfter; and a step is shown to
// maxStepShown bytes (jqMember).
const (
	maxPathSteps         = 8
	pathStepsShownBefore = 2
	pathStepsShownAfter  = 5
	maxStepShown         = 64
)

// jsonLabel is the label of the string at path, its steps from the document, of the document
// record labels, within the bounds above.
func jsonLabel(record string, path []string) string {
	var label strings.Builder
	label.WriteString(record)
	label.WriteString(", JSON ")
	if len(path) == 0 {
		label.WriteString(".")
		return label.String()
	}
	if len(path) > maxPathSteps {
		for _, step := range path[:pathStepsShownBefore] {
			label.WriteString(step)
		}
		label.WriteString("…")
		path = path[len(path)-pathStepsShownAfter:]
	}
	for _, step := range path {
		label.WriteString(step)
	}
	return label.String()
}

// jqMember is the step of a jq path to the member name: `.name` for a name jq writes bare, a
// letter or an underscore and then letters, digits and underscores, and `."name"` quoted as JSON
// quotes it for every other, so that a name holding a newline or a quote cannot break the label
// it stands in. A step longer than maxStepShown bytes is cut there, on a rune boundary, and "…"
// marks the cut: a name can be a whole body long, and its step is in the label of every string
// beneath it.
func jqMember(name string) string {
	bare := name != ""
	for i, c := range name {
		letter := c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
		bare = bare && (letter || i > 0 && '0' <= c && c <= '9')
	}
	step := "." + name
	if !bare {
		var quoted strings.Builder
		encoder := json.NewEncoder(&quoted)
		encoder.SetEscapeHTML(false)
		// A string always encodes: the error is a writer's, and a Builder's write never fails.
		_ = encoder.Encode(name)
		step = "." + strings.TrimSuffix(quoted.String(), "\n")
	}
	if len(step) > maxStepShown {
		step = step[:runeBack(step, maxStepShown, 0)] + "…"
	}
	return step
}

// maxGhFile is the most this package reads from one file a gh call would send, and from the
// standard input it buffers. GitHub itself refuses a pull request or issue body longer than
// 65,536 characters, and a body is the largest thing these options carry: `gh api --input`
// wraps one in a JSON request, which that escaping and the rest of the request leave well
// inside a mebibyte. A mebibyte is sixteen times that body, so no call GitHub would take meets
// the bound, and a file past it is one an argument names by mistake. Such a file is a setup
// failure rather than a pass — the call is refused either way — and the rule pass never reads a
// gigabyte into memory to run its regexes over.
const maxGhFile = 1 << 20

// GhFile is one file a gh call would send as content: the option that named it, which a setup
// failure names; the path the argv wrote, which is the record's label; and, for a file a field
// names (`--field <name>=@<path>`, `-F <name>=@<path>`), the field's name, "" for every other
// option. The name is what says that a file is a GraphQL query, `query` in a `gh api graphql`
// call, which is read one literal at a time (graphQLRecords).
type GhFile struct {
	Option, Path, Field string
}

// inputOption is `gh api`'s request body, the one option whose file, and whose standard input,
// is itself a JSON document (jsonBody, stdinRecords).
const inputOption = "--input"

// ghFileOptions are the options whose whole value is the path of a file gh sends to GitHub as
// content, in gh 2.98.0's spelling — the version the worker image pins, whose help is
// cmd/legion/testdata/gh-help, which is where each of these was read.
var ghFileOptions = map[string]bool{
	// The body of a pull request, an issue, a comment, a review or a revert. Twelve commands of
	// the pinned reference carry it: `awk '/^### /{cmd=$0} /--body-file/{print cmd}'
	// testdata/gh-help/reference.txt | sort -u`.
	"--body-file": true,
	// `gh release create` and `gh release edit`'s release notes.
	"--notes-file": true,
	// `gh api`'s HTTP request body.
	"--input": true,
	// `gh agent-task create`'s task description.
	"--from-file": true,
}

// ghUnreadUploads are the options whose value names a file gh uploads to GitHub that this
// package does not read: gh 2.104.0's --attach, of `gh issue` and `gh pr` comment, create and
// edit and of `gh issue artifact create` and `edit`, which uploads an image (an SVG's text among
// them, up to 10 MiB) or a video (up to 100 MiB) as an asset GitHub serves. It has no short
// spelling. The fallback refuses a call naming one (ghUnreadUploadArg) rather than read a file
// of that kind and size; `legion gh`, which hands the guard the files it parsed, refuses it
// before the guard runs.
var ghUnreadUploads = map[string]bool{"--attach": true}

// ghUnreadUploadArg is the first option of ghUnreadUploads args names, in its separate or its
// attached spelling (`--attach=<path>`), before a bare `--`, and whether there is one. It reads
// the argv with no gh to say which options take a value, so an option's value that is itself
// `--attach` refuses the call too, the direction ghFileArgs errs in as well.
func ghUnreadUploadArg(args []string) (string, bool) {
	for _, arg := range args {
		if arg == "--" {
			return "", false
		}
		name, _, _ := strings.Cut(arg, "=")
		if ghUnreadUploads[name] {
			return name, true
		}
	}
	return "", false
}

// ghFileArgs are the files args would have gh send to GitHub as content, each named once and in
// the order the argv writes them, and the option that named standard input. A gh call carries
// its text in a file as readily as in an argument — `gh api --method PATCH
// repos/<owner>/<repo>/pulls/<n> -F body=@body.md` is how a Legion role writes a pull request's
// body — so each file's text is a record of its own beside the records of the argv, which hold
// the file's name and never its text.
//
// This is the fallback, for a caller that hands the guard a bare argv (GhRequest.Files nil). The
// caller that can do better does: `legion gh` parses the command with gh's own help of the leaf,
// so it knows every option's arity and reads a shape no table can — a short cluster whose last
// letter is the option (`-dF <path>`, which is `--draft --body-file <path>` in `gh pr create`
// and `--desc F` in `gh gist create`) — and hands the files it found. What is read here instead
// is ghFileOptions, each in its separate and its attached spelling (`--body-file=<path>`), and
// two more shapes of one short option:
//
//   - `-F <value>`: gh gives -F to --body-file, --notes-file, --from-file and --field, and to
//     nothing else (`grep -ho -- '-F, --[a-z-]*' testdata/gh-help/*.txt | sort -u`), so a -F is
//     read by its value rather than its name. A value holding no `=` is one of the three file
//     options' (`-F body.md`); a value of the shape `name=@<path>` is --field's file; any other
//     `name=value` is a literal field and names no file.
//   - `--field <name>=@<path>`: `gh api`'s field read from a file. A --field whose value holds
//     no `=` names no file — `gh project field-list --field <name>` names a field of a project —
//     so only the `@` shape is read.
//
// The attached short spellings `-F<value>` and `-F=<value>` are read as well, the two split as
// ghArgRecords splits a field (ghFieldArg), which is how pflag reads them: `-F=body=@notes.md` is
// the field body read from notes.md. Outside `gh api` they are the only short cluster read: a
// `-F` anywhere but the front of a short argument is the value of the option before it (`-dF` is
// `--desc F`), and reading that as a file would refuse a call over a file nobody named. `gh api`'s
// short letters are all known, so its clusters are read as pflag reads them (ghFieldArg):
// `-iF body=@notes.md` is the same field.
//
// This reads the argv with no gh to tell it which other options take a value, so an option's
// value that itself begins with `-F` (a `--body` whose first word is `-Fixed`) is read as a -F
// of its own and refuses the call, naming a file nobody meant. That is the direction this errs
// in: the other way round is `gh release create -F notes.md`, the documented short spelling,
// going to GitHub unread.
//
// A value of `-`, and an `@-`, is standard input rather than a file. The shared check's exit-4
// handshake buffers that input and GhCall records it already (askAgain), so there is nothing
// here to open. What is answered instead is the option that named it, which says what the input
// is (GhRequest.StdinOption), by its long name: `--input -`, `--input=-`, `--body-file -` and the
// other file options answer their own name, and `-F <name>=@-` and `--field <name>=@-` answer
// `--field`. A `-F -` answers `-F`: it is --body-file, --notes-file or --from-file as the leaf
// says, and each of the three makes its input text GitHub stores as it stands. Two options that
// name standard input and differ answer "", since only one of them can have the input, and the
// guard then reads it both ways.
//
// Four things are deliberately left out:
//
//   - `-f`/`--raw-field`, which sends its value literally: `gh api -f body=@x` posts the two
//     characters `@x` and opens no file.
//   - `--env-file` (`gh secret set`, `gh variable set`), whose values are secrets GitHub stores
//     and never publishes. A secret is the shared check's to read, not the private-name rules',
//     which are names a deployment keeps out of public repositories.
//   - `--template`, which names a template of the repository (`gh pr create --template
//     "pull_request_template.md"`) or a Go template for gh's own output (`-t, --template
//     string`), neither of them a file the command sends.
//   - `gh gist create <filename>…`, whose positional arguments are files gh publishes, and
//     every other positional file: `gh release create <tag> <asset>…`, `gh alias import
//     <filename>`. Reading a positional would mean a copy of gh's grammar in here. An option's
//     name says what its value is in every command that carries it, while a positional's
//     meaning is the leaf's — a local file in `gist create`, a name inside the gist in `gist
//     edit <id> <filename>`, a number in `pr comment <number>` — and this reads argv with no gh
//     to ask, as cmd/legion's own reader asks gh's help. Half a copy would read as cover where
//     there is none: `gist edit --add <path>` publishes a file too, and its short `-a` is
//     `--all` in other commands, so it cannot be added to the table above. What these files
//     keep is the shared check, which reads every argument of the call, and the spec's own line
//     on release assets, that reading inside them is the check's policy and not this issue's
//     (dispatch://INFRA-1527, Goals and non-goals). No Legion role writes a gist: the roles
//     post pull request and issue text, which the options above carry.
//
// Arguments past a bare `--` are read as nothing: gh's parser takes no option after one, so a
// `--body-file` there is a word of some other program's and names no file gh would send.
func ghFileArgs(args []string) ([]GhFile, string) {
	var files []GhFile
	stdin, stdinNamed := "", false
	api := len(args) > 0 && args[0] == "api"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		// A field option is split as ghArgRecords splits it (ghFieldArg), and any other option at
		// its first `=`. A -f or --raw-field then reads as neither --field nor a file option, and
		// names no file.
		option, value, attached := ghFieldArg(arg, api)
		if option == "" {
			option = arg
			if name, after, found := strings.Cut(arg, "="); found {
				option, value, attached = name, after, true
			}
		}
		field := option == "-F" || option == "--field"
		if !field && !ghFileOptions[option] {
			continue
		}
		if !attached {
			if i+1 == len(args) {
				// An option with no value at all: gh refuses the call itself, and there is no
				// file here to read.
				break
			}
			i++
			value = args[i]
		}
		path, named, fieldName := value, option, ""
		if field {
			name, after, found := strings.Cut(value, "=")
			switch {
			case found && strings.HasPrefix(after, "@"):
				path, named, fieldName = strings.TrimPrefix(after, "@"), "--field", name
			case found || option != "-F":
				continue
			}
		}
		// A path of "-" is standard input, and a file named twice is read once: a second record
		// of one file is a second open and a second line of the same report.
		if path == "-" {
			switch {
			case !stdinNamed:
				stdin, stdinNamed = named, true
			case stdin != named:
				stdin = ""
			}
			continue
		}
		if path == "" {
			continue
		}
		if slices.ContainsFunc(files, func(file GhFile) bool { return file.Path == path }) {
			continue
		}
		files = append(files, GhFile{Option: option, Path: path, Field: fieldName})
	}
	return files, stdin
}

// readGhFile is the text of one file a gh call would send, read to maxGhFile (readBounded). A
// file that cannot be opened, cannot be read, or is longer than maxGhFile is a setup failure
// naming the option that named it and the path as it was written, each as shownText shows it: a
// guard that could not read what the call publishes has cleared nothing, and a call it cleared
// would be the hole this closes. The error under it is what went wrong alone, since the message
// names the path already.
func readGhFile(named GhFile) (string, error) {
	text, err := readBounded(named.Path, maxGhFile)
	var failed *boundedReadError
	if !errors.As(err, &failed) {
		return string(text), err
	}
	why := failed.phrase
	if failed.err == nil {
		why += ", which is more text than GitHub takes through it"
	}
	return "", &SetupFailure{
		What: whatGhCall,
		Why:  "the file " + shownText(named.Option) + " names, " + shownText(named.Path) + ", " + why,
		Err:  failed.err,
	}
}

// askAgain buffers the caller's standard input to a regular file and asks the check again, with
// that file as a descriptor of the child's and PUBLIC_LEAK_CHECK_STDIN naming it. The file is the
// test's and the pod's own temporary directory's, and it is gone before this returns: the bytes
// go back to the caller in memory, which is where they came from.
//
// The read is bounded as a named file's is, by maxGhFile and for the same reasons: the bytes are
// held in memory and scanned there, and no body GitHub would take comes near a mebibyte. Input
// past the bound is a setup failure, which refuses the call — the guard has not read what it
// would publish — and never a truncated buffer handed on as if it were the whole.
func (g *Guard) askAgain(ctx context.Context, argv []string, req GhRequest) ([]byte, int, string, error) {
	buffered := []byte{}
	if req.Stdin != nil {
		// One byte past the bound, so input at the bound is read whole and longer input is known
		// to be longer without reading it.
		read, err := io.ReadAll(io.LimitReader(req.Stdin, maxGhFile+1))
		if err != nil {
			return nil, 0, "", &SetupFailure{What: whatGhCall, Why: "the call's standard input could not be read", Err: err}
		}
		if len(read) > maxGhFile {
			return nil, 0, "", &SetupFailure{
				What: whatGhCall,
				Why:  fmt.Sprintf("the call's standard input is longer than %d bytes, which is more text than GitHub takes through it", maxGhFile),
			}
		}
		buffered = read
	}
	file, err := os.CreateTemp("", "leakguard-stdin-")
	if err != nil {
		return buffered, 0, "", &SetupFailure{What: whatGhCall, Why: "the call's standard input could not be buffered", Err: err}
	}
	defer func() {
		file.Close()
		os.Remove(file.Name())
	}()
	if _, err := file.Write(buffered); err != nil {
		return buffered, 0, "", &SetupFailure{What: whatGhCall, Why: "the call's standard input could not be buffered", Err: err}
	}
	// The child's descriptor is a dup of this one and shares its offset, so the file is rewound
	// here and not by whoever reads it.
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return buffered, 0, "", &SetupFailure{What: whatGhCall, Why: "the call's standard input could not be buffered", Err: err}
	}
	// ExtraFiles' first entry is the child's descriptor 3, which is the one named here.
	const buffer = 3
	status, printed, err := g.run(ctx, child{
		argv:  argv,
		files: []*os.File{file},
		env: []string{
			fmt.Sprintf("PUBLIC_LEAK_CHECK_STDIN=/dev/fd/%d", buffer),
			// The check's own advice on exit 4 is for a caller that cannot buffer. This one
			// just did, so the advice is turned off.
			"PUBLIC_LEAK_CHECK_STDIN_BUFFERS=1",
		},
	})
	if err != nil {
		return buffered, 0, "", sharedNotRun(whatGhCall, err, printed)
	}
	if status == checkWantsStdin {
		return buffered, 0, printed, &SetupFailure{
			What: whatGhCall,
			Why:  "the shared public leak check asked for standard input again after it was given",
			Err:  printedError(printed),
		}
	}
	return buffered, status, printed, nil
}
