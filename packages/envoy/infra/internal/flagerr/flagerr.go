// Package flagerr reads the flag package's parse errors for the envoy-infra commands that never
// echo a value they could not parse, since a seed may have been pasted there (envoy-infra render,
// envoy-infra buckets, hold and streams), and parses the flags of the writer hold's commands.
package flagerr

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/sjawhar/envoy/internal/bus"
)

// Malformed returns the name of the flag whose value the flag package could not parse, read from
// its message, "invalid value %q for flag -%s: %v" or, for a boolean flag, "invalid boolean value
// %q for -%s: %v"; "" for any other error. The quoted value comes first, so the name follows the
// message's last marker, and only a flag the set defines is returned.
func Malformed(fs *flag.FlagSet, err error) string {
	msg := err.Error()
	if !strings.HasPrefix(msg, "invalid value ") && !strings.HasPrefix(msg, "invalid boolean value ") {
		return ""
	}
	for _, marker := range []string{" for flag -", " for -"} {
		if i := strings.LastIndex(msg, marker); i >= 0 {
			if name, _, _ := strings.Cut(msg[i+len(marker):], ":"); fs.Lookup(name) != nil {
				return name
			}
		}
	}
	return ""
}

// Parse parses args, the words after word, on fs, whose name is the command's whole name
// ("envoy-infra hold set"), and answers -1 when the run goes on, or the exit code of --help (0) or
// of a usage error (2), with its line written to stderr and usage after it. No word given is
// echoed where the parse could not read it as one of its own, since a seed may have been pasted
// there: a stray word is named by its position after word, a value its flag cannot parse by the
// flag, and any other word the parse refused not at all. A flag value that holds an nkey seed
// (bus.HoldsSeed), whole or inside it, is refused naming the flag, never the value, before anything
// reads it, as seed and render refuse one: a value reaches the lines the command writes and the
// hold's records.
func Parse(fs *flag.FlagSet, args []string, word, usage string, stderr io.Writer) int {
	var parsed strings.Builder
	fs.SetOutput(&parsed)
	program := fs.Name()
	if err := fs.Parse(args); err != nil {
		switch {
		case errors.Is(err, flag.ErrHelp):
			fmt.Fprint(stderr, parsed.String())
			return 0
		case strings.HasPrefix(err.Error(), "flag needs an argument: -"):
			fmt.Fprintf(stderr, "%s: %v\n%s", program, err, usage)
		default:
			if name := Malformed(fs, err); name != "" {
				fmt.Fprintf(stderr, "%s: --%s: a malformed value (not echoed: a seed may have been pasted there)\n%s", program, name, usage)
			} else {
				fmt.Fprintf(stderr, "%s: a flag this command does not take, or a malformed one (not echoed: a seed may have been pasted there)\n%s", program, usage)
			}
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "%s: word %d after %q is no flag and no flag's value (not echoed: a seed may have been pasted there)\n%s", program, len(args)-fs.NArg()+2, word, usage)
		return 2
	}
	var seeded []string
	fs.Visit(func(set *flag.Flag) {
		if bus.HoldsSeed(set.Value.String()) {
			seeded = append(seeded, "--"+set.Name)
		}
	})
	if len(seeded) > 0 {
		fmt.Fprintf(stderr, "%s: %s: an nkey seed, which is never an argument (not echoed)\n", program, strings.Join(seeded, ", "))
		return 2
	}
	return -1
}

// StringList is a repeatable string flag.
type StringList []string

func (l *StringList) String() string     { return strings.Join(*l, ",") }
func (l *StringList) Set(v string) error { *l = append(*l, v); return nil }
