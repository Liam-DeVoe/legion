package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/architecture/notes"
	"github.com/sjawhar/envoy/internal/dispatch/specgraph"
)

// runTree is "spec-check tree --dir <dir> [--allow-empty] [--retired <file>] [--people <file>]
// [--projects <file>] [--server-url <url>]": it judges one directory by the rules of specgraph and
// the form and the terms of specform, and answers 0 for a valid or empty tree, 1 for a refused
// one, and 2 for a directory or a file it cannot read or arguments it does not take.
func runTree(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("tree", flag.ContinueOnError)
	dirFlag := flags.String("dir", "", "the directory of spec files")
	allowEmpty := flags.Bool("allow-empty", false, "read a directory that does not exist or holds no spec file as the empty tree")
	retiredFlag := flags.String("retired", "", "a file of the ids the approved spec removed, one id a line")
	peopleFlag := flags.String("people", "", "a file of the emails of the people who signed in, one email a line")
	projectsFlag := flags.String("projects", "", "a file of the project keys, one key a line")
	serverFlag := flags.String("server-url", "", "the address of the Dispatch server")
	if code, ok := parse(flags, args, stderr, "dir"); !ok {
		return code
	}
	// A server address with no scheme or no host matches no link, so the check would pass every
	// full address in silence.
	if *serverFlag != "" {
		if address, err := url.Parse(*serverFlag); err != nil || address.Scheme == "" || address.Host == "" {
			say(stderr, "%s: --server-url %q is not an address with a scheme and a host", flags.Name(), *serverFlag)
			fmt.Fprintln(stderr, usage)
			return 2
		}
	}
	dir, ok := absolute(stderr, *dirFlag)
	if !ok {
		return 2
	}
	var o specgraph.Options
	var people, projects []string
	for _, list := range []struct {
		path string
		into *[]string
	}{{*retiredFlag, &o.Retired}, {*peopleFlag, &people}, {*projectsFlag, &projects}} {
		if list.path == "" {
			continue
		}
		path, ok := absolute(stderr, list.path)
		if !ok {
			return 2
		}
		entries, err := readList(path)
		if err != nil {
			say(stderr, "%s: cannot be read: %s", path, err)
			return 2
		}
		*list.into = entries
	}

	j, err := judge(stderr, dir, o)
	var refused *notes.SizeRefusal
	switch {
	case errors.As(err, &refused):
		// The importer refuses a set with a file above the bound before it parses any, so the tree
		// is refused with no block read.
	case errors.Is(err, notes.ErrNoDirectory) && *allowEmpty:
		// Git keeps no empty directory, so a repository with no spec file yet has no directory, and
		// the importer reads a missing directory as the empty set.
	case err != nil:
		return cannotJudge(stderr, dir, err)
	case j.files == 0 && !*allowEmpty:
		say(stderr, "%s: holds no spec file", dir)
		return 2
	}
	if o.Retired == nil {
		say(stderr, "%s: no --retired list, so no id is checked against the ids the approved spec removed", dir)
	}
	if people == nil {
		say(stderr, "%s: no --people list, so no text is checked for a person's name", dir)
	}
	if projects == nil {
		say(stderr, "%s: no --projects list, so no text is checked for a project key", dir)
	}
	if *serverFlag == "" {
		say(stderr, "%s: no --server-url, so a link to a Dispatch server by its full address is not checked", dir)
	}
	if refused != nil {
		for _, problem := range refused.Problems {
			say(stderr, "%s", problem)
		}
		say(stdout, "%s: %s; the tree is refused", dir, j.counts())
		return 1
	}
	j.problems = append(j.problems, formProblems(formContext(people, projects, *serverFlag), j.tree)...)
	sortProblems(j.problems)
	j.sayProblems(stderr)
	switch {
	case len(j.problems) > 0:
		say(stdout, "%s: %s; the tree is refused", dir, j.counts())
		return 1
	case j.files == 0:
		say(stdout, "%s: %s; the tree is empty", dir, j.counts())
	default:
		say(stdout, "%s: %s; the tree is valid", dir, j.counts())
	}
	return 0
}

// readList reads the entries of a list file: one entry a line, white space around it trimmed,
// and lines that start with "#" and blank lines passed over. An empty file gives an empty list,
// not nil, so a list that holds no entry is still a list that was read.
func readList(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	entries := []string{}
	for line := range strings.Lines(string(content)) {
		if entry := strings.TrimSpace(line); entry != "" && !strings.HasPrefix(entry, "#") {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}
