package buckets

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/sjawhar/envoy/infra/writerhold"
	"github.com/sjawhar/envoy/internal/holdrecord"
)

// Deps are what CommandOn reaches outside the process, in place of the broker, Secret Manager and
// the hub its flags name: a test of another package runs the bucket words on its own stack with
// them (writerhold/holdtest), as writerhold.StreamsCommandOn runs the held streams words. The
// hold's flags are still required and checked, and their values are not read.
type Deps struct {
	// Records opens the operator's client of the broker's hold routes, signing when signing.
	Records func(ctx context.Context, signing bool) (*holdrecord.Client, error)
	// Operator is the operator's reach of the hub.
	Operator func(ctx context.Context) (*writerhold.Operator, error)
	// Now is the clock the minter report is checked by; time.Now when nil.
	Now func() time.Time
}

// CommandOn is Command with deps in place of the broker, Secret Manager and hub its flags name:
// the caller-stack seam of `envoy-infra buckets`, as writerhold.StreamsCommandOn is of
// `envoy-infra streams`. A restore keeps its one rerun, as Command's does.
func CommandOn(ctx context.Context, args []string, stdout, stderr io.Writer, deps Deps) int {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	e := env{
		records: func(ctx context.Context, _ writerhold.HubFlags, signing bool) (*holdrecord.Client, error) {
			return deps.Records(ctx, signing)
		},
		hub: func(ctx context.Context, _ writerhold.HubFlags) (*writerhold.Operator, error) {
			return deps.Operator(ctx)
		},
		now:         now,
		http:        &http.Client{Timeout: 10 * time.Second},
		restoreRuns: 2,
	}
	return command(ctx, args, stdout, stderr, e)
}
