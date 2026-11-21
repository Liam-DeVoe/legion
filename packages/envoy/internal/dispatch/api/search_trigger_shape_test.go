package api

import (
	"context"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// sqlStringEnd answers the index of the quote that closes the string literal opening at open in
// src, a doubled quote being one quote of the text, or len(src) when none closes it.
func sqlStringEnd(src string, open int) int {
	for i := open + 1; i < len(src); i++ {
		if src[i] != '\'' {
			continue
		}
		if i+1 < len(src) && src[i+1] == '\'' {
			i++
			continue
		}
		return i
	}
	return len(src)
}

// sqlCallEnd answers the index just past the parenthesis that closes the one at open in src,
// skipping string literals, or -1 when none closes it.
func sqlCallEnd(src string, open int) int {
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '\'':
			i = sqlStringEnd(src, i)
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// sqlCalls answers each call of the function name in src, whole, and its top-level arguments.
func sqlCalls(src, name string) (calls []string, args [][]string) {
	opener := regexp.MustCompile(`(?i)(?:\b[a-z_]+\.)?\b` + name + `\s*\(`)
	for _, at := range opener.FindAllStringIndex(src, -1) {
		end := sqlCallEnd(src, at[1]-1)
		if end < 0 {
			continue
		}
		calls = append(calls, src[at[0]:end])
		var split []string
		inner, depth, start := src[at[1]:end-1], 0, 0
		for i := 0; i < len(inner); i++ {
			switch inner[i] {
			case '\'':
				i = sqlStringEnd(inner, i)
			case '(':
				depth++
			case ')':
				depth--
			case ',':
				if depth == 0 {
					split = append(split, strings.TrimSpace(inner[start:i]))
					start = i + 1
				}
			}
		}
		args = append(args, append(split, strings.TrimSpace(inner[start:])))
	}
	return calls, args
}

// withoutCalls answers src with every call of each named function removed whole.
func withoutCalls(src string, names ...string) string {
	for _, name := range names {
		calls, _ := sqlCalls(src, name)
		for _, call := range calls {
			src = strings.ReplaceAll(src, call, "")
		}
	}
	return src
}

// withoutSQLComments answers src with each `--` comment removed, leaving a `--` inside a string
// literal where it is.
func withoutSQLComments(src string) string {
	var kept strings.Builder
	for i := 0; i < len(src); i++ {
		switch {
		case src[i] == '\'':
			end := min(sqlStringEnd(src, i), len(src)-1)
			kept.WriteString(src[i : end+1])
			i = end
		case strings.HasPrefix(src[i:], "--"):
			for i < len(src) && src[i] != '\n' {
				i++
			}
			if i < len(src) {
				kept.WriteByte('\n')
			}
		default:
			kept.WriteByte(src[i])
		}
	}
	return kept.String()
}

// normalisedCall reports whether argument is wholly one call of search_text or
// search_document_text: the call's own parenthesis closes it.
func normalisedCall(argument string) bool {
	opener := regexp.MustCompile(`(?i)^(search_text|search_document_text)\s*\(`).FindStringIndex(argument)
	return opener != nil && sqlCallEnd(argument, opener[1]-1) == len(argument)
}

var (
	sqlString = regexp.MustCompile(`'(?:[^']|'')*'`)
	newRead   = regexp.MustCompile(`(?i)\bnew\b[^;,\n]*`)
	// directParse is a call of any of Postgres's functions that parse text into a vector:
	// to_tsvector, json_to_tsvector, jsonb_to_tsvector, quoted or qualified.
	directParse = regexp.MustCompile(`(?i)"?\w*to_tsvector"?\s*\(`)
	// searchCall is a call that reaches the parser or a normaliser, which makes a trigger function
	// one this test reads.
	searchCall = regexp.MustCompile(`(?i)\b(search_vector|issue_search_vector|title_lexemes|search_text|search_document_text)\s*\(|"?\w*to_tsvector"?\s*\(`)
)

// Each search trigger hands Postgres's text-search parser its text only through the shared
// normalising functions, read from the catalog as the database runs them: search_document_text for
// a column that holds ask blocks (a document's markdown, an issue's spec), which removes them and
// then applies search_text, and search_text for any other text. Removing ask blocks after
// search_text joins underscore runs that search_text broke, which the parser reads in quadratic
// time (20261121060636), and a text read raw reaches the parser with every run whole.
//
// The test reads, outside comments and string literals, the source of every trigger function in
// the database that calls the parser or a normaliser; the set must be the six it knows, so a new
// one is read here before it merges. In each: no call of a parsing function (to_tsvector and its
// json forms, in any spelling); each argument of search_vector is an empty literal or wholly one
// normalising call; markdown, in any spelling, only inside search_document_text; and once the
// normalising calls and the vector functions read below are removed, no read through `new` is
// left - new.x, (new).x, row_to_json(new) - but the vector it writes and the artifact_id it looks
// a row up by. It reads search_document_text's own body too, which must end in search_text, and
// each parameter of the two vector functions those triggers call, which must appear only inside its
// normaliser. A read of the row through another name, such as a variable assigned from new, is
// beyond it; the parser-input test holds what reaches the parser at run time.
func TestEverySearchTriggerHandsTheParserOnlyNormalisedText(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	rows, err := database.Pool.Query(ctx, `
		select distinct p.proname::text, p.prosrc
		  from pg_trigger t join pg_proc p on p.oid = t.tgfoid
		 where not t.tgisinternal`)
	if err != nil {
		t.Fatalf("read the trigger functions: %v", err)
	}
	triggers := map[string]string{}
	for rows.Next() {
		var name, source string
		if err := rows.Scan(&name, &source); err != nil {
			t.Fatalf("read a trigger function: %v", err)
		}
		source = withoutSQLComments(source)
		if searchCall.MatchString(sqlString.ReplaceAllString(source, "''")) {
			triggers[name] = source
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the trigger functions: %v", err)
	}
	want := []string{"artifact_versions_issues_search", "artifact_versions_search", "asks_search", "comments_search", "issues_search", "messages_search"}
	if got := slices.Sorted(maps.Keys(triggers)); !slices.Equal(got, want) {
		t.Fatalf("the trigger functions that reach the parser are %v, want %v: read a new one here", got, want)
	}

	source := func(name string) string {
		var source string
		if err := database.Pool.QueryRow(ctx,
			`select prosrc from pg_proc where proname = $1 and pronamespace = 'public'::regnamespace`, name).Scan(&source); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return withoutSQLComments(source)
	}
	body := strings.TrimSpace(source("search_document_text"))
	if selected, ok := strings.CutPrefix(strings.ToLower(body), "select"); !ok || !normalisedCall(strings.TrimSpace(selected)) ||
		!strings.HasPrefix(strings.TrimSpace(selected), "search_text") {
		t.Errorf("search_document_text answers %q, which is not one search_text(…) call: a change made after search_text can join its runs again", body)
	}
	vectorFunctions := map[string]map[string]string{
		"issue_search_vector": {"p_spec": "search_document_text", "p_key": "search_text", "p_title": "search_text"},
		"title_lexemes":       {"title": "search_text"},
	}
	for name, parameters := range vectorFunctions {
		text := source(name)
		for parameter, wrapper := range parameters {
			rest := sqlString.ReplaceAllString(withoutCalls(text, wrapper), "''")
			if regexp.MustCompile(`(?i)\b` + parameter + `\b`).MatchString(rest) {
				t.Errorf("%s reads %s outside %s:\n%s", name, parameter, wrapper, text)
			}
		}
	}

	for name, source := range triggers {
		code := sqlString.ReplaceAllString(source, "''")
		if parse := directParse.FindString(code); parse != "" {
			t.Errorf("%s calls the parser itself (%s…): it must build its vector through search_vector or a vector function", name, parse)
		}
		_, args := sqlCalls(source, "search_vector")
		for _, arguments := range args {
			for _, argument := range arguments {
				if argument != "''" && !normalisedCall(argument) {
					t.Errorf("%s hands search_vector %q, which is neither '' nor one call of search_text or search_document_text", name, argument)
				}
			}
		}
		if regexp.MustCompile(`(?i)\bmarkdown\b`).MatchString(sqlString.ReplaceAllString(withoutCalls(source, "search_document_text"), "''")) {
			t.Errorf("%s reads markdown, which holds ask blocks, outside search_document_text:\n%s", name, source)
		}
		rest := sqlString.ReplaceAllString(withoutCalls(source,
			"search_document_text", "search_text", "issue_search_vector", "title_lexemes", "issue_embedding_spec"), "''")
		for _, read := range newRead.FindAllString(rest, -1) {
			read = strings.ToLower(strings.TrimSpace(read))
			if !regexp.MustCompile(`^new\.(search|title_lexemes)\s*:=|^new\.artifact_id\b|^new$`).MatchString(read) {
				t.Errorf("%s reads the row raw (%s), outside the normalising functions:\n%s", name, read, source)
			}
		}
	}
}
