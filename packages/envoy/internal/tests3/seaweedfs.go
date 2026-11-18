// Package tests3 runs an S3-compatible server in a container for the tests that need a real one:
// the file store's own tests, which drive the AWS SDK's real client against it so what the store
// sends and how it reads the answers are exercised end to end, and the e2e harness when a run
// asks for one. SeaweedFS stands in for S3: MinIO's image left Docker Hub, and SeaweedFS answers
// the calls the store makes (HeadBucket, HeadObject, PutObject with a SHA-256 checksum, GetObject
// with checksum validation, and S3's own 404 and NoSuchKey shapes). A test that needs the fake's
// failure shapes uses files/filestest instead.
package tests3

import (
	"bytes"
	"context"
	_ "embed"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/sjawhar/envoy/internal/dispatch/files/filestest"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Image is the SeaweedFS image every S3-compatible test container runs: ghcr.io/cybermonklabs/legion/ci,
// the copy of seaweedfs:3.97 (.github/ci-images.yaml). The envoy-go CI job reads this declaration
// and pulls the image before its first test step, so no test reaches the registry mid-run; keep it
// a single-line string constant.
const Image = "ghcr.io/cybermonklabs/legion/ci@sha256:bb05d66d2963b1cc48073190781c3dd29e2c4c88a2ae2986bf58e38c86d89c6e"

// collection holds every object the server stores. one-collection.sh gives the server one rule,
// every path into this collection, and grows its volumes before anything is written, so no write
// grows a collection; that file says why a write must not.
const collection = "tests3"

// oneCollection is one-collection.sh, the one implementation of that rule, which the shard job of
// .github/workflows/dispatch-browser.yaml runs for the browser suite's server too.
//
//go:embed one-collection.sh
var oneCollection string

// scriptPath is where the container holds one-collection.sh, and serverLog where its server
// writes the log that script reads.
const (
	scriptPath = "/one-collection.sh"
	serverLog  = "/tmp/weed.log"
)

// scriptDeadline bounds one run of a step of one-collection.sh. The script bounds its own waits,
// each failing it by name when it runs out: 60 tries a second apart for the volume server, 10 for
// the grow, 60 s for each weed shell. prepare ended within seconds of the S3 port answering in
// this script's runs on a dev box, and 11 to 12 s after it with the volume server held back 10 s.
// So a step still running at 5 minutes is one the script did not end, a stuck docker exec among
// them, and it fails the test by name 15 minutes before go test's -timeout in the envoy-go job
// (.github/workflows/ci-job-envoy-go.yaml) ends the whole package with no name.
const scriptDeadline = 5 * time.Minute

// requestDeadline bounds one request to the server. Its answers come within a second, and a
// write that waits on storage being made gets its 500 when the filer gives up after 10 s, so a
// request still open at a minute is to a server that stopped answering.
const requestDeadline = time.Minute

// Start runs an S3-compatible server holding the empty bucket, removed when t ends, points this
// process's AWS SDK at it for the rest of t (filestest.PointSDKAt), so files.NewS3(ctx, bucket)
// reaches it exactly as a deployment's reaches its bucket, and returns the endpoint for a caller
// that hands it to another process. t.Setenv makes it unusable in a parallel test.
func Start(t testing.TB, bucket string) string {
	t.Helper()
	ctx := context.Background()
	container, endpoint := startServer(ctx, t, oneCollection)
	if code, output := runScript(ctx, t, container, "prepare"); code != 0 {
		t.Fatalf("give the S3-compatible server its one collection: exit %d:\n%s", code, output)
	}
	// The server creates a bucket on an anonymous PUT of its name.
	if status, body := call(ctx, t, http.MethodPut, endpoint+"/"+bucket, ""); status != http.StatusOK {
		t.Fatalf("create bucket %s: %d %s", bucket, status, body)
	}
	// The first object, written, read back and deleted, which proves the server takes one.
	object := endpoint + "/" + bucket + "/tests3-readiness"
	if status, body := call(ctx, t, http.MethodPut, object, "ready"); status != http.StatusOK {
		t.Fatalf("write the first object: %d %s", status, body)
	}
	if status, body := call(ctx, t, http.MethodGet, object, ""); status != http.StatusOK || body != "ready" {
		t.Fatalf("read back the first object: %d %q, want 200 %q", status, body, "ready")
	}
	if status, body := call(ctx, t, http.MethodDelete, object, ""); status != http.StatusNoContent {
		t.Fatalf("delete the first object: %d %s", status, body)
	}
	if code, output := runScript(ctx, t, container, "check"); code != 0 {
		t.Fatalf("check the S3-compatible server after its first write: exit %d:\n%s", code, output)
	}
	// The same check when the test is done with the server, so a write of the test's own that
	// grew storage fails it by name. A cleanup registered after CleanupContainer's runs before it.
	t.Cleanup(func() {
		if code, output := runScript(ctx, t, container, "check"); code != 0 {
			t.Errorf("check the S3-compatible server after the test: exit %d:\n%s", code, output)
		}
	})
	filestest.PointSDKAt(t, endpoint)
	return endpoint
}

// startServer runs the server in a container removed when t ends, with script at scriptPath,
// and answers the container and its S3 endpoint once that port answers.
func startServer(ctx context.Context, t testing.TB, script string) (testcontainers.Container, string) {
	t.Helper()
	container, err := testcontainers.Run(ctx, Image,
		// The image's own entrypoint, which adds the server's volume flags, with the server's
		// output in serverLog, where one-collection.sh reads it, rather than the container's log.
		testcontainers.WithEntrypoint("sh", "-c", `exec /entrypoint.sh "$@" > `+serverLog+` 2>&1`, "sh"),
		testcontainers.WithCmd("server", "-s3", "-dir=/data", "-ip=0.0.0.0", "-s3.port=8333"),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader:            strings.NewReader(script),
			ContainerFilePath: scriptPath,
			FileMode:          0o755,
		}),
		testcontainers.WithExposedPorts("8333/tcp"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/").WithPort("8333/tcp").WithStartupTimeout(90*time.Second)),
		// One CPU and 1 GiB, as every container the repository's tests start is capped on a shared
		// host: one process runs the master, volume, filer and S3 servers for a few small objects.
		testcontainers.WithHostConfigModifier(func(hostConfig *container.HostConfig) {
			hostConfig.NanoCPUs = 1_000_000_000
			hostConfig.Memory = 1 << 30
		}),
	)
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("start the S3-compatible server: %v", err)
	}
	endpoint, err := container.PortEndpoint(ctx, "8333/tcp", "http")
	if err != nil {
		t.Fatalf("S3-compatible server endpoint: %v", err)
	}
	return container, endpoint
}

// runScript runs step of one-collection.sh inside c, for collection and serverLog, and answers
// its exit code and its output; a step that has not ended within scriptDeadline fails t.
func runScript(ctx context.Context, t testing.TB, c testcontainers.Container, step string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, scriptDeadline)
	defer cancel()
	type result struct {
		code   int
		output []byte
		err    error
	}
	// c.Exec reads the step's output to its end before it returns, and that read does not stop
	// with ctx (the docker client's hijacked connection has no deadline of its own), so the run
	// goes on apart while this waits for it or for the deadline. A run the deadline left ends
	// when the container is removed with t.
	done := make(chan result, 1)
	go func() {
		code, reader, err := c.Exec(ctx, []string{scriptPath, step, collection, serverLog}, tcexec.Multiplexed())
		if err != nil {
			done <- result{err: err}
			return
		}
		output, err := io.ReadAll(reader)
		done <- result{code: code, output: output, err: err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("run one-collection.sh %s in the S3-compatible server's container: %v", step, r.err)
		}
		return r.code, string(r.output)
	case <-ctx.Done():
		t.Fatalf("one-collection.sh %s did not end within %s", step, scriptDeadline)
		return 0, ""
	}
}

// call sends one anonymous request with body and answers the status and the body of the answer.
func call(ctx context.Context, t testing.TB, method, url, body string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, requestDeadline)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = response.Body.Close() }()
	answer, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the answer to %s %s: %v", method, url, err)
	}
	return response.StatusCode, string(answer)
}
