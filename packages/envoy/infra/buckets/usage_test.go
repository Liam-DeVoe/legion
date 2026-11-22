package buckets

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/infra/writerhold"
)

// The usage names every flag `envoy-infra buckets` takes, for each of its words: each flag the
// parse defines, read from the flag package's own help listing of `<word> --help`, is in the usage
// text as `--<name>`. It replaces #4296's TestTheUsageListsEveryFlag, which went with the
// best-effort command this one replaced. Its control is a go test -overlay copy of buckets.go whose
// usage leaves out `--connz`, under which it fails naming connz.
func TestTheUsageNamesEveryFlag(t *testing.T) {
	flagLine := regexp.MustCompile(`(?m)^\s+-([a-z][a-z-]*)\b`)
	for _, word := range []string{"create", "recreate", "restore", "backup"} {
		var stdout, stderr bytes.Buffer
		if code := CommandOn(context.Background(), []string{word, "--help"}, &stdout, &stderr, writerhold.Deps{}); code != 0 {
			t.Fatalf("buckets %s --help = %d, want 0\n%s", word, code, stderr.String())
		}
		names := flagLine.FindAllStringSubmatch(stderr.String(), -1)
		if len(names) < 10 {
			t.Fatalf("buckets %s --help listed %d flags, want every one of them:\n%s", word, len(names), stderr.String())
		}
		for _, m := range names {
			if !strings.Contains(usage, "--"+m[1]) {
				t.Errorf("buckets %s takes --%s, which the usage does not name", word, m[1])
			}
		}
	}
}
