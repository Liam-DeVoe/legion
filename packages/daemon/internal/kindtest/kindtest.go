// Package kindtest is the kind rig a Go test of the Sandbox runtime runs on: a throwaway kind
// cluster with Agent Sandbox's controller and gVisor, a registry the node pulls from, the fake
// GitHub (internal/fakegithub), which the cluster's pods reach as github.com and api.github.com,
// and the worker image under test, beside an image built FROM it that trusts the fake GitHub's
// authority. Start brings it up from the test process on an amd64 Linux host with Docker, which
// the kind job of ci.yaml is, and tears it down when the test ends, keeping the cluster's state
// when the test failed (LogDirEnv).
//
// The cluster's pieces are scripts/e2e/lib/kind-sandbox.sh's, the one place the rig's pins are
// written; each runs in a bash of its own here. The test process reaches the cluster as the host
// does, through the API server's port on 127.0.0.1 (Kubeconfig, Context, Issuer), and the cluster's
// pods reach the test process at the kind network's gateway on this host (Gateway), which is where
// the fake GitHub listens.
//
// Every piece Start runs, and every piece a test times with Measure, is logged by name with how
// long it took, and Report logs them together: the measurements a test of the rig is read for.
package kindtest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/testprereq"
	"github.com/sjawhar/legion/daemon/internal/fakegithub"
)

// The variables the kind job hands the rig. The worker image under test comes by one of the first
// two: ImageEnv names it by digest in a registry this host can pull from (the image a
// pull request's worker-image job pushed), and ImageArchiveEnv names a `docker save` archive of it
// (the image the kind job built itself). LogDirEnv names the directory a failed test's cluster
// state goes to, which the job uploads.
const (
	ImageEnv        = "LEGION_TEST_KIND_IMAGE"
	ImageArchiveEnv = "LEGION_TEST_KIND_IMAGE_ARCHIVE"
	LogDirEnv       = "LEGION_TEST_KIND_LOG_DIR"
)

// job is the ci.yaml job that runs the kind tests and gives them what Start needs.
const job = "kind"

// Rig is one kind cluster and what a test reaches it with.
type Rig struct {
	// Name is the cluster's name, and Kubeconfig and Context reach its API server from this
	// process. Node is its one node, a container on the kind network.
	Name, Kubeconfig, Context, Node string
	// Issuer is the API server's service-account issuer, https://127.0.0.1:<its port>, whose
	// discovery document and keys this process reads with no credential.
	Issuer string
	// Gateway is the kind network's gateway on this host, the address a pod reaches this process
	// at.
	Gateway string
	// Registry is the local registry as the host and the node name it, localhost:<port>.
	Registry string
	// GitHub is the fake GitHub, which the cluster's pods reach as github.com and api.github.com.
	GitHub *fakegithub.Server
	// WorkerImage is the worker image under test, unmodified, by its digest in Registry: the
	// image the checks of the image itself start (the image probe, the leak guard's probe).
	WorkerImage string
	// Image is the image every other pod of a kind test starts, by its digest in Registry, and
	// already in the node's containerd: built FROM WorkerImage, with the fake GitHub's authority
	// added to its system trust store (update-ca-certificates), so git, curl and every Go program
	// of the image trust the fake as GitHub.
	Image string

	lib, work, kind string
	evidence        string
	measured        []measurement
}

type measurement struct {
	piece string
	took  time.Duration
}

// Start brings the rig up for t and tears it down when t ends, failing t at the first piece that
// fails. Each piece is logged by name with how long it took: kind (the pinned kind's build),
// cluster, agent-sandbox, gvisor, registry, fake-github, worker-image (the pull of ImageEnv's image,
// or the load of ImageArchiveEnv's archive), trusting-image (the build FROM it), registry-push
// (both images into Registry) and image-load (Image into the node's containerd).
func Start(t *testing.T) *Rig {
	t.Helper()
	r := prepare(t)
	r.Measure(t, "kind", func() {
		bin := filepath.Join(r.work, "bin")
		r.shell(t, "kind_sandbox_install_kind", bin)
		r.kind = filepath.Join(bin, "kind")
	})
	r.Measure(t, "cluster", func() {
		port := freePort(t)
		r.shell(t, "kind_sandbox_create", r.Name, r.work, strconv.Itoa(port))
		r.Issuer = "https://127.0.0.1:" + strconv.Itoa(port)
		r.checkIssuer(t)
	})
	r.Measure(t, "agent-sandbox", func() { r.shell(t, "kind_sandbox_install_agent_sandbox") })
	r.Measure(t, "gvisor", func() { r.shell(t, "kind_sandbox_install_gvisor", r.work) })
	r.Measure(t, "registry", func() {
		port := freePort(t)
		r.shell(t, "kind_sandbox_start_registry", strconv.Itoa(port))
		r.Registry = "localhost:" + strconv.Itoa(port)
	})
	r.Measure(t, "fake-github", func() {
		r.Gateway = gateway(t)
		r.GitHub = fakegithub.Start(t, fakegithub.Options{Listen: net.JoinHostPort(r.Gateway, "0")})
		_, port, err := net.SplitHostPort(r.GitHub.Addr())
		if err != nil {
			t.Fatal(err)
		}
		r.shell(t, "kind_sandbox_route_github", r.Gateway, port)
	})
	var worker, trusting string
	r.Measure(t, "worker-image", func() { worker = r.loadWorkerImage(t) })
	r.Measure(t, "trusting-image", func() { trusting = r.buildTrustingImage(t, worker) })
	r.Measure(t, "registry-push", func() {
		r.WorkerImage = r.push(t, worker, "legion-worker")
		r.Image = r.push(t, trusting, "legion-worker-trusting")
		// The host's copies go once the registry holds both: the node pulls from the registry,
		// and three copies of a multi-gigabyte image is more disk than a runner can spare. A copy
		// that will not go costs disk and nothing else, so it is logged rather than failed.
		names := []string{worker, trusting, r.Registry + "/legion-worker:rig", r.Registry + "/legion-worker-trusting:rig"}
		if out, err := exec.Command("docker", append([]string{"image", "rm", "--force"}, names...)...).CombinedOutput(); err != nil {
			t.Logf("docker image rm %s: %v: %s", strings.Join(names, " "), err, out)
		}
	})
	r.Measure(t, "image-load", func() { r.shell(t, "kind_sandbox_load_image", r.Image) })
	return r
}

// prepare holds the machine to what the rig needs, names the cluster and its files, and registers
// the teardown, which runs after every cleanup a test registers later.
func prepare(t *testing.T) *Rig {
	t.Helper()
	testprereq.Require(t, runtime.GOOS == "linux" && runtime.GOARCH == "amd64", job,
		"the kind rig runs on amd64 Linux alone, since gVisor's release it installs is x86_64; this is %s/%s", runtime.GOOS, runtime.GOARCH)
	for _, tool := range []string{"docker", "kubectl", "go", "git", "bash", "curl", "zstd", "tar", "sha512sum"} {
		_, err := exec.LookPath(tool)
		testprereq.Require(t, err == nil, job, "the kind rig runs %s, which is not on PATH", tool)
	}
	testprereq.Require(t, os.Getenv(ImageEnv) != "" || os.Getenv(ImageArchiveEnv) != "", job,
		"the kind rig starts the worker image under test, which %s names by digest or %s names an archive of; neither is set", ImageEnv, ImageArchiveEnv)
	r := &Rig{
		lib:  filepath.Join(repositoryRoot(t), "scripts", "e2e", "lib", "kind-sandbox.sh"),
		work: t.TempDir(), kind: "kind",
		Name: fmt.Sprintf("legion-rig-%d", os.Getpid()),
	}
	r.Kubeconfig, r.Context, r.Node = filepath.Join(r.work, "kubeconfig"), "kind-"+r.Name, r.Name+"-control-plane"
	r.evidence = evidenceDir(t)
	t.Cleanup(func() { r.teardown(t) })
	return r
}

// Measure runs do as the rig's piece named piece, and logs how long it took.
func (r *Rig) Measure(t *testing.T, piece string, do func()) {
	t.Helper()
	started := time.Now()
	defer func() {
		took := time.Since(started).Round(100 * time.Millisecond)
		r.measured = append(r.measured, measurement{piece: piece, took: took})
		t.Logf("kind rig: %s took %s", piece, took)
	}()
	do()
}

// Report logs every piece measured so far, in order, and their sum.
func (r *Rig) Report(t *testing.T) {
	t.Helper()
	var lines strings.Builder
	var total time.Duration
	for _, m := range r.measured {
		fmt.Fprintf(&lines, "  %-16s %8.1fs\n", m.piece, m.took.Seconds())
		total += m.took
	}
	fmt.Fprintf(&lines, "  %-16s %8.1fs", "all", total.Seconds())
	t.Logf("kind rig measurements:\n%s", lines.String())
}

// Kubectl runs kubectl against the rig's cluster and returns its standard output; a failure fails
// t with what kubectl said.
func (r *Rig) Kubectl(t *testing.T, args ...string) string {
	t.Helper()
	out, err := r.kubectl(nil, args...)
	if err != nil {
		t.Fatalf("kubectl %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// Apply applies manifest to the rig's cluster.
func (r *Rig) Apply(t *testing.T, manifest string) {
	t.Helper()
	if _, err := r.kubectl(strings.NewReader(manifest), "apply", "-f", "-"); err != nil {
		t.Fatalf("kubectl apply: %v\n%s", err, manifest)
	}
}

// StartNFSServer starts the rig's NFS server (kind_sandbox_start_nfs_server), exporting /team,
// which holds seed.txt with seed, and /scratch, and returns its address, which the node itself
// has mounted /team from.
func (r *Rig) StartNFSServer(t *testing.T, seed string) string {
	t.Helper()
	r.shell(t, "kind_sandbox_start_nfs_server", seed)
	return strings.TrimSpace(r.Kubectl(t, "-n", "nfs", "get", "service", "ganesha", "-o", "jsonpath={.spec.clusterIP}"))
}

// Pod is what a test reads of a pod.
type Pod struct {
	Name             string
	Phase            string
	QOSClass         string
	RuntimeClassName string
	// ExitCode is the first container's, once it terminated; -1 before.
	ExitCode int
}

// WaitPod waits up to within for the one pod of namespace that selector selects to end
// (Succeeded or Failed), and returns it; at the bound it fails t with the pod's description and
// its namespace's events.
func (r *Rig) WaitPod(t *testing.T, namespace, selector string, within time.Duration) Pod {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		out, err := r.kubectl(nil, "-n", namespace, "get", "pods", "-l", selector, "-o", "json")
		if err == nil {
			if pod, ok := endedPod(out); ok {
				return pod
			}
		}
		if time.Now().After(deadline) {
			described, _ := r.kubectl(nil, "-n", namespace, "describe", "pods", "-l", selector)
			events, _ := r.kubectl(nil, "-n", namespace, "get", "events", "--sort-by=.lastTimestamp")
			t.Fatalf("no pod %s of %s ended within %s:\n%s\n%s", selector, namespace, within, described, events)
		}
		time.Sleep(2 * time.Second)
	}
}

// endedPod is the one pod a `kubectl get pods -o json` list holds, once it ended.
func endedPod(out string) (Pod, bool) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				RuntimeClassName string `json:"runtimeClassName"`
			} `json:"spec"`
			Status struct {
				Phase             string `json:"phase"`
				QOSClass          string `json:"qosClass"`
				ContainerStatuses []struct {
					State struct {
						Terminated *struct {
							ExitCode int `json:"exitCode"`
						} `json:"terminated"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal([]byte(out), &list) != nil || len(list.Items) != 1 {
		return Pod{}, false
	}
	item := list.Items[0]
	if item.Status.Phase != "Succeeded" && item.Status.Phase != "Failed" {
		return Pod{}, false
	}
	pod := Pod{Name: item.Metadata.Name, Phase: item.Status.Phase, QOSClass: item.Status.QOSClass,
		RuntimeClassName: item.Spec.RuntimeClassName, ExitCode: -1}
	if statuses := item.Status.ContainerStatuses; len(statuses) > 0 && statuses[0].State.Terminated != nil {
		pod.ExitCode = statuses[0].State.Terminated.ExitCode
	}
	return pod, true
}

// DiskFree is `df -h` of this host, for a test to log what the rig took of the runner's disk.
func DiskFree(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("df", "-h").CombinedOutput()
	if err != nil {
		return fmt.Sprintf("df -h: %v: %s", err, out)
	}
	return string(out)
}

// NodeDiskFree is `df -h` of the node's root and of its containerd's store, which hold the images
// the node pulled and every pod's writable layer.
func (r *Rig) NodeDiskFree(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("docker", "exec", r.Node, "df", "-h", "/", "/var/lib/containerd").CombinedOutput()
	if err != nil {
		return fmt.Sprintf("docker exec %s df -h: %v: %s", r.Node, err, out)
	}
	return string(out)
}

// shell runs one function of the rig's library in a bash of its own, after kind_sandbox_use names
// the cluster, with the caller's note and fail, and logs what it printed as it prints it.
func (r *Rig) shell(t *testing.T, function string, args ...string) {
	t.Helper()
	const script = `set -euo pipefail
note() { printf '   %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
. "$1"
kind_sandbox_use "$2" "$3"
shift 3
"$@"`
	command := exec.Command("bash", append([]string{"-c", script, "kind-rig", r.lib, r.Name, r.work, function}, args...)...)
	command.Env = append(os.Environ(), "LEGION_E2E_KIND="+r.kind)
	logged := &lineLogger{t: t, prefix: function + ": "}
	command.Stdout, command.Stderr = logged, logged
	err := command.Run()
	logged.flush()
	if err != nil {
		t.Fatalf("%s %s: %v", function, strings.Join(args, " "), err)
	}
}

// kubectl runs kubectl against the rig's cluster with stdin, returning its standard output, or an
// error carrying what it printed on standard error.
func (r *Rig) kubectl(stdin io.Reader, args ...string) (string, error) {
	command := exec.Command("kubectl", append([]string{"--kubeconfig", r.Kubeconfig, "--context", r.Context}, args...)...)
	command.Stdin = stdin
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		return string(out), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// docker runs docker and returns its combined output; a failure fails t with it.
func (r *Rig) docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// checkIssuer reads the issuer's discovery document from this process with no credential, trusting
// the cluster's own authority alone, and fails t unless it names the issuer and keys under it.
func (r *Rig) checkIssuer(t *testing.T) {
	t.Helper()
	encoded := r.Kubectl(t, "config", "view", "--raw", "--minify", "-o", "jsonpath={.clusters[0].cluster.certificate-authority-data}")
	authority, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		t.Fatalf("read the cluster's authority from its kubeconfig: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(authority) {
		t.Fatal("the cluster's kubeconfig holds no authority certificate")
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	response, err := client.Get(r.Issuer + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatalf("read the issuer's discovery document: %v", err)
	}
	defer response.Body.Close()
	var discovery struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &discovery) != nil || discovery.Issuer != r.Issuer ||
		discovery.JWKSURI != r.Issuer+"/openid/v1/jwks" {
		t.Fatalf("the issuer's discovery document, read with no credential, answered %d %s; want issuer %s and its keys under it",
			response.StatusCode, body, r.Issuer)
	}
	t.Logf("the API server's issuer %s serves its discovery document and keys (%s) to a reader with no credential", discovery.Issuer, discovery.JWKSURI)
}

// loadWorkerImage puts the worker image under test into this host's Docker and returns the name
// Docker holds it under: a pull of ImageEnv's reference, or a load of ImageArchiveEnv's archive.
func (r *Rig) loadWorkerImage(t *testing.T) string {
	t.Helper()
	if image := os.Getenv(ImageEnv); image != "" {
		if !strings.Contains(image, "@sha256:") {
			t.Fatalf("%s must name the worker image by digest (<image>@sha256:<64 hex>), got %s", ImageEnv, image)
		}
		r.docker(t, "pull", "--quiet", image)
		t.Logf("pulled the worker image %s", image)
		return image
	}
	archive := os.Getenv(ImageArchiveEnv)
	out := r.docker(t, "load", "--input", archive)
	loaded := regexp.MustCompile(`(?m)^Loaded image(?: ID)?: (\S+)$`).FindStringSubmatch(out)
	if loaded == nil {
		t.Fatalf("docker load of %s named no image: %s", archive, out)
	}
	t.Logf("loaded the worker image %s from %s", loaded[1], archive)
	// The archive's bytes are in Docker now.
	if err := os.Remove(archive); err != nil {
		t.Logf("could not remove %s after loading it: %v", archive, err)
	}
	return loaded[1]
}

// buildTrustingImage builds, FROM worker, the image every other pod of a kind test starts: the fake
// GitHub's authority added to the system trust store, under the user worker runs as. Debian's
// update-ca-certificates is called by its full path, /usr/sbin/update-ca-certificates, since the
// worker image's PATH (its ENV, packages/daemon/docker/worker.Dockerfile) holds no /usr/sbin and a
// RUN of the bare name exits 127.
func (r *Rig) buildTrustingImage(t *testing.T, worker string) string {
	t.Helper()
	dir := filepath.Join(r.work, "trusting-image")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fake-github-ca.crt"), r.GitHub.CAPEM(), 0o644); err != nil {
		t.Fatal(err)
	}
	user := strings.TrimSpace(r.docker(t, "image", "inspect", "--format", "{{.Config.User}}", worker))
	dockerfile := "FROM " + worker + "\nUSER root\n" +
		"COPY fake-github-ca.crt /usr/local/share/ca-certificates/fake-github-ca.crt\n" +
		"RUN /usr/sbin/update-ca-certificates\n"
	if user != "" {
		dockerfile += "USER " + user + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := "legion-worker-trusting:" + r.Name
	r.docker(t, "build", "--quiet", "--tag", tag, dir)
	t.Logf("built %s FROM %s, trusting the fake GitHub's authority, as user %q", tag, worker, user)
	return tag
}

// pushDigest is the digest `docker push` names for what it pushed.
var pushDigest = regexp.MustCompile(`digest: (sha256:[0-9a-f]{64})`)

// push tags image as Registry's repository and pushes it, returning its reference by the digest the
// registry holds it under.
func (r *Rig) push(t *testing.T, image, repository string) string {
	t.Helper()
	tag := r.Registry + "/" + repository + ":rig"
	r.docker(t, "tag", image, tag)
	out := r.docker(t, "push", tag)
	digest := pushDigest.FindStringSubmatch(out)
	if digest == nil {
		t.Fatalf("docker push %s named no digest: %s", tag, out)
	}
	pushed := r.Registry + "/" + repository + "@" + digest[1]
	t.Logf("the registry holds %s as %s", image, pushed)
	return pushed
}

// teardown keeps what the cluster held and the fake GitHub's requests when the test failed, then
// deletes the cluster and the registry.
func (r *Rig) teardown(t *testing.T) {
	status := "0"
	if t.Failed() {
		status = "1"
		if r.GitHub != nil {
			requests := strings.Join(r.GitHub.Requests(), "\n") + "\n"
			if err := os.WriteFile(filepath.Join(r.evidence, "fake-github-requests.txt"), []byte(requests), 0o644); err != nil {
				t.Logf("keep the fake GitHub's requests: %v", err)
			}
		}
		t.Logf("the rig's state at the failure is in %s", r.evidence)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	const script = `note() { printf '   %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
. "$1"
kind_sandbox_use "$2" "$3"
kind_sandbox_teardown "$4" "$5"`
	command := exec.CommandContext(ctx, "bash", "-c", script, "kind-rig", r.lib, r.Name, r.work, status, r.evidence)
	command.Env = append(os.Environ(), "LEGION_E2E_KIND="+r.kind)
	if out, err := command.CombinedOutput(); err != nil || len(out) > 0 {
		t.Logf("teardown of %s: %v: %s", r.Name, err, out)
	}
}

// evidenceDir is where a failed test's cluster state goes: a directory of the test's own under
// LogDirEnv, which the kind job uploads when it fails, or, where that is unset, a directory that
// outlives the test, named in the log.
func evidenceDir(t *testing.T) string {
	t.Helper()
	name := regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(t.Name(), "_")
	if root := os.Getenv(LogDirEnv); root != "" {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	dir, err := os.MkdirTemp("", "legion-kind-rig-"+name+"-")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// repositoryRoot is the checkout this test runs in: the nearest directory above the working
// directory that holds the rig's library.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "scripts", "e2e", "lib", "kind-sandbox.sh")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no directory above the test's holds scripts/e2e/lib/kind-sandbox.sh")
		}
		dir = parent
	}
}

// freePort is a port of 127.0.0.1 nothing listens on now.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// gateway is the kind network's IPv4 gateway: this host's address on the network the node is on,
// which a pod's traffic to it leaves the node for.
func gateway(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("docker", "network", "inspect", "kind", "--format",
		`{{range .IPAM.Config}}{{.Subnet}} {{.Gateway}}{{"\n"}}{{end}}`).CombinedOutput()
	if err != nil {
		t.Fatalf("docker network inspect kind: %v: %s", err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		subnet, gateway, _ := strings.Cut(strings.TrimSpace(line), " ")
		_, network, err := net.ParseCIDR(subnet)
		if err != nil || network.IP.To4() == nil {
			continue
		}
		if ip := net.ParseIP(gateway); ip != nil && ip.To4() != nil {
			return ip.String()
		}
		first := network.IP.To4()
		return net.IPv4(first[0], first[1], first[2], first[3]+1).String()
	}
	t.Fatalf("the kind network has no IPv4 subnet: %s", out)
	return ""
}

// lineLogger logs each line written to it as a line of the test's log, with a prefix.
type lineLogger struct {
	t       *testing.T
	prefix  string
	pending []byte
}

func (l *lineLogger) Write(b []byte) (int, error) {
	l.pending = append(l.pending, b...)
	for {
		i := bytes.IndexByte(l.pending, '\n')
		if i < 0 {
			return len(b), nil
		}
		l.t.Log(l.prefix + string(l.pending[:i]))
		l.pending = l.pending[i+1:]
	}
}

func (l *lineLogger) flush() {
	if len(l.pending) > 0 {
		l.t.Log(l.prefix + string(l.pending))
		l.pending = nil
	}
}
