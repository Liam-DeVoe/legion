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

// collection holds every object the server stores. SeaweedFS keeps a bucket's objects in a
// collection of the bucket's name and makes that collection's volumes when the bucket's first
// object is written, and that growth races the volume server's heartbeat: the master can take
// the collection's first volume back while the write waits on it, and the write then fails after
// 10 s and every later write fails at once. So Start gives the filer one rule, every path into
// this collection, and grows its volumes before anything is written; a growth with no write
// waiting on it survives the race, since the next heartbeat lists the volume again. The shard job
// of .github/workflows/dispatch-browser.yaml does the same for the browser suite's server.
const collection = "tests3"

// rule is the first thing Start runs inside the container: the rule, then a directory made after
// it under /etc/seaweedfs. fs.configure only stores the rule; the filer puts it in force when it
// processes that change, in order with every change under /etc/seaweedfs, and logs each one
// ("procesing", spelt so), so the directory's log line says the rule before it is in force.
const rule = `printf 'fs.configure -locationPrefix=/ -collection=` + collection + ` -apply\nfs.mkdir /etc/seaweedfs/` + marker + `\n' |
  weed shell -master=localhost:9333 -filer=localhost:8888`

// marker is the directory rule makes after the rule.
const marker = "rule-in-force"

// grow makes the collection's seven volumes, retried while the volume server has not yet
// registered with the master.
const grow = `for attempt in $(seq 1 60); do
  wget -q -O /dev/null "http://localhost:9333/vol/grow?collection=` + collection + `&count=7" && exit 0
  sleep 1
done
exit 1`

// Start runs an S3-compatible server holding the empty bucket, removed when t ends, points this
// process's AWS SDK at it for the rest of t (filestest.PointSDKAt), so files.NewS3(ctx, bucket)
// reaches it exactly as a deployment's reaches its bucket, and returns the endpoint for a caller
// that hands it to another process. t.Setenv makes it unusable in a parallel test.
func Start(t testing.TB, bucket string) string {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.Run(ctx, Image,
		testcontainers.WithCmd("server", "-s3", "-dir=/data", "-ip=0.0.0.0", "-s3.port=8333"),
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
	if code, output := execIn(ctx, t, container, rule); code != 0 {
		t.Fatalf("give the S3-compatible server its one collection: exit %d: %s", code, output)
	}
	waitForRule(ctx, t, container)
	if code, output := execIn(ctx, t, container, grow); code != 0 {
		t.Fatalf("grow the S3-compatible server's one collection: exit %d: %s", code, output)
	}
	// The server creates a bucket on an anonymous PUT of its name.
	if status, body := call(ctx, t, http.MethodPut, endpoint+"/"+bucket, ""); status != http.StatusOK {
		t.Fatalf("create bucket %s: %d %s", bucket, status, body)
	}
	// The first object, written, read back and deleted. The rule held only if it went into the
	// one collection and the server holds no other, which collection.list then says.
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
	_, collections := execIn(ctx, t, container, "echo collection.list | weed shell -master=localhost:9333 -filer=localhost:8888")
	if strings.Count(collections, `collection:"`) != 1 || !strings.Contains(collections, `collection:"`+collection+`"`) {
		t.Fatalf("the S3-compatible server holds a collection other than %s, so a write can grow one: %s", collection, collections)
	}
	filestest.PointSDKAt(t, endpoint)
	return endpoint
}

// execIn runs script with sh inside c and answers its exit code and its output.
func execIn(ctx context.Context, t testing.TB, c testcontainers.Container, script string) (int, string) {
	t.Helper()
	code, reader, err := c.Exec(ctx, []string{"sh", "-c", script}, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("run %q in the S3-compatible server's container: %v", script, err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read the output of %q: %v", script, err)
	}
	return code, string(output)
}

// waitForRule waits, within a minute, for the server's log line that it processed marker.
func waitForRule(ctx context.Context, t testing.TB, c testcontainers.Container) {
	t.Helper()
	var logged string
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		reader, err := c.Logs(ctx)
		if err != nil {
			t.Fatalf("read the S3-compatible server's log: %v", err)
		}
		all, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatalf("read the S3-compatible server's log: %v", err)
		}
		logged = string(all)
		for _, line := range strings.Split(logged, "\n") {
			if strings.Contains(line, "procesing") && strings.Contains(line, marker) {
				return
			}
		}
	}
	t.Fatalf("the S3-compatible server did not put the rule for %s in force within a minute; its log:\n%s", collection, logged)
}

// call sends one anonymous request with body and answers the status and the body of the answer.
func call(ctx context.Context, t testing.TB, method, url, body string) (int, string) {
	t.Helper()
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
