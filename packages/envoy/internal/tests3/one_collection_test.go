package tests3

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Each check of one-collection.sh fails when what it checks is broken, on the plain server and
// not only on the rare run where the growth race comes. Each case runs the script with parts
// cut, in a server of its own; TestS3AgainstAnS3CompatibleServer (internal/dispatch/files) runs
// the whole script through Start, which is the case that must pass. The shard job of
// .github/workflows/dispatch-browser.yaml runs the same script, so these cases hold for its
// server too. No case cuts the script's wait for a registered volume server, which every weed
// shell of it runs behind, so no case can leave a shell waiting for ever.
func TestOneCollectionFailsWhenTheRuleDoesNotHold(t *testing.T) {
	if os.Getenv("DISPATCH_TEST_NO_CONTAINERS") == "1" {
		t.Skip("DISPATCH_TEST_NO_CONTAINERS=1: this box runs no test containers")
	}
	// The grow aimed at another collection, so the collection has no volumes of its own before
	// the first write. The grow still runs, with the steps before it.
	growElsewhere := [2]string{"collection=$collection&count=7", "collection=elsewhere&count=7"}
	for _, c := range []struct {
		name string
		// Each cut's first text is replaced by its second, and must occur in the script exactly once.
		cuts [][2]string
		// write says whether an object is written between prepare and check.
		write bool
		// step is the step that must fail, and want what its error says.
		step, want string
	}{{
		// Then the first write would be the one that grows the collection, waiting on it.
		name: "grow aimed elsewhere: prepare fails before any write",
		cuts: [][2]string{growElsewhere},
		step: "prepare", want: "::error::before the first write the store holds no volumes of tests3 alone",
	}, {
		// weed shell exits 0 over the command it does not know, and the rule is not stored.
		name: "rule misspelt: prepare fails by the rule read back",
		cuts: [][2]string{{"fs.configure -locationPrefix=/", "fs.configurx -locationPrefix=/"}},
		step: "prepare", want: "::error::the filer holds no rule putting every path into tests3",
	}, {
		// The check before the first write cut too, so only check's own reading of the log can
		// see that the write grew the collection; the log is read before the collections are.
		name:  "a write grew storage: check fails by the server's log",
		cuts:  [][2]string{growElsewhere, {"\n  alone_before_writes\n", "\n"}},
		write: true,
		step:  "check", want: "::error::a write grew the store's storage",
	}, {
		// check pointed at a log that does not exist, while prepare reads the real one: a log
		// that cannot be read must not read as one with no growth in it.
		name: "no log: check fails rather than read nothing as no growth",
		cuts: [][2]string{{"  check) check ;;", "  check) log=/no-such.log; check ;;"}},
		step: "check", want: "::error::the server's log /no-such.log is missing or empty",
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			script := oneCollection
			for _, cut := range c.cuts {
				if n := strings.Count(script, cut[0]); n != 1 {
					t.Fatalf("the cut occurs %d times in one-collection.sh, not once: %q", n, cut[0])
				}
				script = strings.Replace(script, cut[0], cut[1], 1)
			}
			ctx := context.Background()
			server, endpoint := startServer(ctx, t, script)
			code, output := runScript(ctx, t, server, "prepare")
			if c.step == "prepare" {
				if code == 0 || !strings.Contains(output, c.want) {
					t.Fatalf("prepare: exit %d, want a failure saying %q:\n%s", code, c.want, output)
				}
				return
			}
			if code != 0 {
				t.Fatalf("prepare: exit %d, want 0:\n%s", code, output)
			}
			if c.write {
				const bucket = "cut-bucket"
				if status, body := call(ctx, t, http.MethodPut, endpoint+"/"+bucket, ""); status != http.StatusOK {
					t.Fatalf("create bucket %s: %d %s", bucket, status, body)
				}
				// This write grows the collection, so it meets the growth race the script exists
				// to keep from every other write, and its answer can be a 500. The master logs the
				// growth when the write's assign asks for it, before the race can act, and that
				// line is all check reads, so the answer is logged and not asserted.
				status, body := call(ctx, t, http.MethodPut, endpoint+"/"+bucket+"/object", "bytes")
				t.Logf("write an object: %d %s", status, body)
			}
			if code, output := runScript(ctx, t, server, "check"); code == 0 || !strings.Contains(output, c.want) {
				t.Fatalf("check: exit %d, want a failure saying %q:\n%s", code, c.want, output)
			}
		})
	}
}
