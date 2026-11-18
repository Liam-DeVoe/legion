package tests3

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Each check of one-collection.sh fails when what it checks is broken, on the plain server and
// not only on the rare run where the growth race comes. Each case runs the script with one part
// cut, in a server of its own; TestS3AgainstAnS3CompatibleServer (internal/dispatch/files) runs
// the whole script through Start, which is the case that must pass. The shard job of
// .github/workflows/dispatch-browser.yaml runs the same script, so these cases hold for its
// server too.
func TestOneCollectionFailsWhenTheRuleDoesNotHold(t *testing.T) {
	if os.Getenv("DISPATCH_TEST_NO_CONTAINERS") == "1" {
		t.Skip("DISPATCH_TEST_NO_CONTAINERS=1: this box runs no test containers")
	}
	for _, c := range []struct {
		name string
		// cut is replaced by put in the script, and must occur in it exactly once.
		cut, put string
		// write says whether an object is written between prepare and check.
		write bool
		// step is the step that must fail, and want what its error says.
		step, want string
	}{{
		// Then the first write would be the one that grows the collection, waiting on it.
		name: "no grow: prepare fails before any write",
		cut:  "\n  grow\n  configure\n", put: "\n  configure\n",
		step: "prepare", want: "::error::before the first write the store holds no volumes of tests3 alone",
	}, {
		// weed shell exits 0 over the command it does not know, and the rule is not stored.
		name: "rule misspelt: prepare fails by the rule read back",
		cut:  "fs.configure -locationPrefix=/", put: "fs.configurx -locationPrefix=/",
		step: "prepare", want: "::error::the filer holds no rule putting every path into tests3",
	}, {
		// The checks before the first write cut too, so only check's own reading of the log can
		// see that the write grew the collection, which is still the store's only one.
		name:  "a write grew storage: check fails by the server's log",
		cut:   "\n  grow\n  configure\n  read_back\n  wait_in_force\n  alone_before_writes\n",
		put:   "\n  configure\n  read_back\n  wait_in_force\n",
		write: true,
		step:  "check", want: "::error::a write grew the store's storage",
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if n := strings.Count(oneCollection, c.cut); n != 1 {
				t.Fatalf("the cut occurs %d times in one-collection.sh, not once: %q", n, c.cut)
			}
			ctx := context.Background()
			server, endpoint := startServer(ctx, t, strings.Replace(oneCollection, c.cut, c.put, 1))
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
				if status, body := call(ctx, t, http.MethodPut, endpoint+"/"+bucket+"/object", "bytes"); status != http.StatusOK {
					t.Fatalf("write an object: %d %s", status, body)
				}
			}
			if code, output := runScript(ctx, t, server, "check"); code == 0 || !strings.Contains(output, c.want) {
				t.Fatalf("check: exit %d, want a failure saying %q:\n%s", code, c.want, output)
			}
		})
	}
}
