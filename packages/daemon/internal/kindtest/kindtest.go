// Package kindtest is the kind rig a Go test of the Sandbox runtime runs on: a throwaway kind
// cluster with Agent Sandbox's controller and gVisor, a registry the node pulls from, the fake
// GitHub (internal/fakegithub), which the cluster's pods reach as github.com and api.github.com,
// the stand-ins for the public TLS hosts the pods reach beside it (TLSSites: the npm registry and
// the pods' web-search provider), and an image built
// FROM the worker image under test that reads those stand-ins as the real services. Start
// brings it up from the test process on an amd64 Linux host with Docker, which the kind job of
// ci.yaml is, and tears it down when the test ends, keeping the cluster's state when the test
// failed (LogDirEnv).
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
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/testprereq"
	"github.com/sjawhar/legion/daemon/internal/appauth"
	"github.com/sjawhar/legion/daemon/internal/fakegithub"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/githubtest"
	"github.com/sjawhar/legion/daemon/internal/leakguard"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// The variables the kind job hands the rig. The worker image under test comes by one of the first
// two: ImageEnv names it by digest in a registry this host can pull from (the image a
// pull request's worker-image job pushed), and ImageArchiveEnv names a docker archive of it (the
// image the kind job built itself), compressed with zstd when its name ends in .zst. LogDirEnv
// names the directory each test's evidence goes to, which the job uploads: on every run the TLS
// stand-ins' requests and each node's containerd journal, and a failed test's cluster state.
// NPMTarballsEnv names a directory of npm tarballs, which a test serves from the rig's npm
// stand-in (TLSSites.ServeNPMLock): the job's setup fetches them by a lock's resolved URLs, since
// a test's pods reach no registry outside the runner. FetchedEnv names the directory
// kind_sandbox_fetch filled in a set-up step (the pinned kind, gVisor's release) after pulling
// the node and registry images, so Start fetches nothing: the kind job runs its tests in a cgroup
// whose outbound connections reach loopback and the kind network's bridge alone
// (.github/scripts/test-fence.sh). Where it is unset, Start runs kind_sandbox_fetch itself, into
// the test's own directory.
const (
	ImageEnv        = "LEGION_TEST_KIND_IMAGE"
	ImageArchiveEnv = "LEGION_TEST_KIND_IMAGE_ARCHIVE"
	LogDirEnv       = "LEGION_TEST_KIND_LOG_DIR"
	NPMTarballsEnv  = "LEGION_TEST_KIND_NPM_TARBALLS"
	FetchedEnv      = "LEGION_TEST_KIND_FETCHED"
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
	// Sites is the stand-in for the public TLS hosts the pods reach beside GitHub (the sites the
	// daemon's image probe reaches, the npm registry and the web-search provider), which the
	// cluster's pods reach under the hosts' own names.
	Sites *TLSSites
	// Image is the image every pod of a kind test starts, the image probe's included, by its digest
	// in Registry, and already in the node's containerd: built FROM the worker image under test to
	// read the stand-ins as the real services (buildStandInImage).
	Image string

	// Evidence is the directory a failed test's cluster state goes to (LogDirEnv), where a test
	// keeps its own logs for the same upload.
	Evidence string

	lib, work, kind, fetched string
	measured                 []measurement
}

type measurement struct {
	piece string
	// took is the piece's own time, and wall how long Start waited on it: all of took for a piece
	// run alone, and for worker-image, which runs beside the pieces before it, what was left of it
	// when they ended.
	took, wall time.Duration
}

// Start brings the rig up for t and tears it down when t ends, failing t at the first piece that
// fails. Each piece is logged by name with how long it took: fetch (kind_sandbox_fetch, where
// FetchedEnv names no directory it filled already), cluster, agent-sandbox, gvisor, registry,
// stand-ins (the fake GitHub and the TLS sites, and the cluster's route to them), worker-image
// (the pull of ImageEnv's image, or the load of ImageArchiveEnv's archive), stand-in-image (the
// build FROM it), registry-push (Image into Registry) and image-load (Image into the node's
// containerd).
//
// worker-image reads only its archive or its registry and this host's Docker, and was the
// longest piece before stand-in-image (88 to 149 s in CI run 45309357623, against about 54 s for
// cluster to stand-ins), so it runs on a goroutine of its own beside cluster, agent-sandbox,
// gvisor, registry and stand-ins, timed from its own start, and is joined before stand-in-image.
func Start(t *testing.T) *Rig {
	t.Helper()
	r := prepare(t)
	r.Measure(t, "fetch", func() {
		if r.fetched = os.Getenv(FetchedEnv); r.fetched == "" {
			r.fetched = filepath.Join(r.work, "fetched")
			r.shell(t, "kind_sandbox_fetch", r.fetched)
		}
		r.kind = filepath.Join(r.fetched, "kind")
	})
	load := r.startWorkerImage(t)
	r.Measure(t, "cluster", func() {
		port := freePort(t)
		r.shell(t, "kind_sandbox_create", r.Name, r.work, strconv.Itoa(port))
		r.Issuer = "https://127.0.0.1:" + strconv.Itoa(port)
		r.checkIssuer(t)
	})
	r.Measure(t, "agent-sandbox", func() { r.shell(t, "kind_sandbox_install_agent_sandbox") })
	r.Measure(t, "gvisor", func() { r.shell(t, "kind_sandbox_install_gvisor", r.fetched, r.work) })
	r.Measure(t, "registry", func() {
		port := freePort(t)
		r.shell(t, "kind_sandbox_start_registry", strconv.Itoa(port))
		r.Registry = "localhost:" + strconv.Itoa(port)
	})
	r.Measure(t, "stand-ins", func() {
		r.Gateway = gateway(t)
		r.GitHub = fakegithub.Start(t, fakegithub.Options{Listen: net.JoinHostPort(r.Gateway, "0")})
		r.Sites = StartTLSSites(t, net.JoinHostPort(r.Gateway, "0"))
		_, githubPort, err := net.SplitHostPort(r.GitHub.Addr())
		if err != nil {
			t.Fatal(err)
		}
		_, sitesPort, err := net.SplitHostPort(r.Sites.Addr())
		if err != nil {
			t.Fatal(err)
		}
		r.shell(t, "kind_sandbox_route_stand_ins", append([]string{r.Gateway, githubPort, sitesPort}, r.Sites.Hosts()...)...)
	})
	worker := load.join(t)
	var standIn string
	r.Measure(t, "stand-in-image", func() { standIn = r.buildStandInImage(t, worker) })
	r.Measure(t, "registry-push", func() {
		r.Image = r.push(t, standIn, "legion-worker-stand-ins")
		// The host's copies go once the registry holds the image: the node pulls from the
		// registry, and more copies of a multi-gigabyte image is more disk than a runner can spare.
		// A copy that will not go costs disk and nothing else, so it is logged rather than failed.
		names := []string{worker, standIn, r.Registry + "/legion-worker-stand-ins:rig"}
		if out, err := exec.Command("docker", append([]string{"image", "rm", "--force"}, names...)...).CombinedOutput(); err != nil {
			t.Logf("docker image rm %s: %v: %s", strings.Join(names, " "), err, out)
		}
	})
	r.Measure(t, "image-load", func() { r.shell(t, "kind_sandbox_load_image", r.Image) })
	return r
}

// workerLoad is Start's worker-image piece, which runs loadWorkerImage on a goroutine of its own
// (startWorkerImage). That goroutine touches no t: what it did is read on the test's goroutine,
// after done closes.
type workerLoad struct {
	rig         *Rig
	done        chan struct{}
	started     time.Time
	took        time.Duration
	image, note string
	err         error
	joined      bool
}

// startWorkerImage starts loadWorkerImage on a goroutine of its own, under t's context, which the
// testing package cancels when t ends, before t's cleanups run. The cleanup it registers runs
// before prepare's teardown, since cleanups run last registered first, and waits for the load to
// end: when a piece on the test's goroutine fails before the join, the load is killed with the
// context, and no docker load outlives the test or runs beside the teardown.
func (r *Rig) startWorkerImage(t *testing.T) *workerLoad {
	t.Helper()
	ctx := t.Context()
	load := &workerLoad{rig: r, done: make(chan struct{}), started: time.Now()}
	go func() {
		defer close(load.done)
		load.image, load.note, load.err = loadWorkerImage(ctx)
		load.took = time.Since(load.started)
	}()
	t.Cleanup(func() {
		<-load.done
		if load.joined {
			return
		}
		outcome := load.note
		if load.err != nil {
			outcome = load.err.Error()
		}
		t.Logf("kind rig: worker-image ended after %s, beside a piece that failed: %s", load.took.Round(100*time.Millisecond), outcome)
	})
	return load
}

// join waits for the worker image's load and records it as the piece worker-image, timed from
// its own start, with how long the test's goroutine waited for it after the pieces beside it.
// Then it fails t with the load's error, on the test's goroutine, or returns the name Docker holds
// the image under.
func (l *workerLoad) join(t *testing.T) string {
	t.Helper()
	waiting := time.Now()
	<-l.done
	l.joined = true
	if l.err == nil {
		t.Log(l.note)
	}
	l.rig.record(t, "worker-image", l.took, time.Since(waiting))
	if l.err != nil {
		t.Fatal(l.err)
	}
	return l.image
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
		lib:  filepath.Join(RepositoryRoot(t), "scripts", "e2e", "lib", "kind-sandbox.sh"),
		work: t.TempDir(), kind: "kind",
		Name: fmt.Sprintf("legion-rig-%d", os.Getpid()),
	}
	r.Kubeconfig, r.Context, r.Node = filepath.Join(r.work, "kubeconfig"), "kind-"+r.Name, r.Name+"-control-plane"
	r.Evidence = evidenceDir(t)
	t.Cleanup(func() { r.teardown(t) })
	return r
}

// Measure runs do as the rig's piece named piece, and logs how long it took.
func (r *Rig) Measure(t *testing.T, piece string, do func()) {
	t.Helper()
	started := time.Now()
	defer func() {
		took := time.Since(started)
		r.record(t, piece, took, took)
	}()
	do()
}

// record keeps and logs the measurement of piece: it took took, of which Start waited wall.
func (r *Rig) record(t *testing.T, piece string, took, wall time.Duration) {
	t.Helper()
	took, wall = took.Round(100*time.Millisecond), wall.Round(100*time.Millisecond)
	r.measured = append(r.measured, measurement{piece: piece, took: took, wall: wall})
	if wall == took {
		t.Logf("kind rig: %s took %s", piece, took)
		return
	}
	t.Logf("kind rig: %s took %s beside the pieces before it, %s of it after they ended", piece, took, wall)
}

// Report logs every piece measured so far, in order, and how long they kept the rig waiting: their
// sum, with only what was left of a piece run beside others when those ended.
func (r *Rig) Report(t *testing.T) {
	t.Helper()
	var lines strings.Builder
	var total time.Duration
	for _, m := range r.measured {
		fmt.Fprintf(&lines, "  %-16s %8.1fs", m.piece, m.took.Seconds())
		if m.wall != m.took {
			fmt.Fprintf(&lines, " (beside the pieces before it, %.1fs of it after they ended)", m.wall.Seconds())
		}
		lines.WriteByte('\n')
		total += m.wall
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

// postgresPassword is the password of the rig's PostgreSQL superuser, a server of one test's
// throwaway cluster that nothing but this host and the cluster reaches.
const postgresPassword = "kind-postgres-not-a-secret"

// Postgres is the rig's PostgreSQL server (StartPostgres): the URL of its database postgres, as its
// superuser, from each side that dials it.
type Postgres struct {
	// InCluster is the URL a pod of the cluster dials: the Service's cluster IP.
	InCluster string
	// Host is the URL this process dials: the node's address on the kind network and the
	// Service's node port.
	Host string
}

// StartPostgres starts the rig's PostgreSQL server (kind_sandbox_start_postgres) in the cluster
// and returns its URLs once it accepts connections: a session store's database, which the pods of
// the test reach, and the store of a daemon the test process runs.
func (r *Rig) StartPostgres(t *testing.T) Postgres {
	t.Helper()
	r.shell(t, "kind_sandbox_start_postgres", postgresPassword)
	address := strings.TrimSpace(r.Kubectl(t, "-n", "postgres", "get", "service", "postgres", "-o", "jsonpath={.spec.clusterIP}"))
	port := strings.TrimSpace(r.Kubectl(t, "-n", "postgres", "get", "service", "postgres", "-o", "jsonpath={.spec.ports[0].nodePort}"))
	node := strings.TrimSpace(r.docker(t, "inspect", "--format", `{{with index .NetworkSettings.Networks "kind"}}{{.IPAddress}}{{end}}`, r.Node))
	if address == "" || port == "" || node == "" {
		t.Fatalf("the rig's PostgreSQL has cluster IP %q and node port %q, and the node %s address %q on the kind network", address, port, r.Node, node)
	}
	url := func(host, port string) string {
		return "postgres://postgres:" + postgresPassword + "@" + net.JoinHostPort(host, port) + "/postgres?sslmode=disable"
	}
	return Postgres{InCluster: url(address, "5432"), Host: url(node, port)}
}

// leakCheckToken is the read token LeakCheck's Source presents, and the one its repository takes.
const leakCheckToken = "ghs_kind-leak-check-not-a-secret"

// LeakCheckRepository is the repository the rig's stand-in shared public leak check is published
// from (LeakCheckSetting), as a deployment names its own (leakguard.Setting.Repository): a neutral
// one, under an owner no other repository of the rig's has, so a whole daemon reads it with the
// implement App's read lease on that owner's installation alone (ServeLeakCheck).
var LeakCheckRepository = ghrepo.MustParse("example-org/deployment")

// standInLeakCheck is the shared public leak check a kind test's pods install: the real check is a
// script of a private repository no CI run reads (leakguardtest says so), so this stand-in, under
// the real one's name, answers `--help` as the real one does, naming the two shapes a launcher's
// install verifies (leakguard.InstallServed), and clears every write it is asked about, reading
// its standard input as the real one does.
const standInLeakCheck = `case "${1:-}" in --help) echo '# Usage: check-public-text FILE..., --commits REV..., --tree DIR, gh-guard ARGS, or REMOTE URL.'; exit 1 ;; esac
cat >/dev/null 2>&1
exit 0
`

// standInFiles are the stand-in's leak-check directory, each file under its name: the launcher
// (standInLeakCheck), executable, and the three files beside it that the check's manifest digest
// covers too (leakguard.ManifestFiles), with leakguardtest's stand-in bodies. Every pod's install
// of the stand-in holds these bytes, and so the manifest digest LeakCheckSetting names.
var standInFiles = map[string]leakguardtest.Member{
	leakguardtest.CheckName:    {Body: "#!/bin/sh\n" + standInLeakCheck, Mode: 0o755},
	leakguardtest.ProgramName:  {Body: leakguardtest.Program, Mode: 0o644},
	leakguardtest.ReadmeName:   {Body: leakguardtest.Readme, Mode: 0o644},
	leakguardtest.MustPassName: {Body: leakguardtest.MustPass, Mode: 0o644},
}

// LeakCheckSetting is the deployment setting that names the rig's stand-in (leakguard.Setting,
// legion.yaml's leak_check): LeakCheckRepository, which LeakCheckAPI and ServeLeakCheck serve it
// from, and the manifest digest of standInFiles, computed by leakguard.Manifest as the runtime and
// every role's shim compute it of an install. A runtime under it pushes the stand-in to each pod's
// launcher and tells every role the install as PUBLIC_LEAK_CHECK and this digest beside it, so each
// role's `legion gh` and push run the stand-in; a runtime under the zero Setting pushes no check,
// and its roles start without one.
func LeakCheckSetting(t testing.TB) leakguard.Setting {
	t.Helper()
	digest, err := leakguard.Manifest(func(name string) ([]byte, error) {
		member, held := standInFiles[name]
		if !held {
			return nil, fs.ErrNotExist
		}
		return []byte(member.Body), nil
	})
	if err != nil {
		t.Fatalf("the manifest digest of the stand-in leak check: %v", err)
	}
	return leakguard.Setting{Repository: LeakCheckRepository, ManifestSHA256: digest}
}

// LeakCheckAPI serves GitHub's tarball route of LeakCheckRepository on this host's loopback, over
// plain HTTP, until the test ends: at any tag, to a request carrying token as its bearer, a tarball
// shaped as GitHub's (leakguardtest.GitHubTarball) holding standInFiles. It returns the REST root a
// daemon reads the check at (leakguard.Source.API), for a runtime under LeakCheckSetting whose pods'
// launchers name the worker image's pin: a launcher installs the check its daemon read before it
// starts any role of its pod, and a runtime that reads none starts no role.
func LeakCheckAPI(t *testing.T, token string) string {
	t.Helper()
	tarball := leakguardtest.GitHubTarball(t, standInFiles)
	return githubtest.Server(t, githubtest.Spec{Token: token, Handle: func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || !strings.HasPrefix(request.URL.Path, "/repos/"+LeakCheckRepository.String()+"/tarball/") {
			githubtest.Unexpected(t, w, request)
			return
		}
		w.Header().Set("Content-Type", "application/x-gzip")
		_, _ = w.Write(tarball)
	}})
}

// LeakCheck is the reader of the shared public leak check a kind test's runtime is built with
// (sandbox.Options.LeakCheck), beside LeakCheckSetting (sandbox.Options.LeakCheckSetting): the
// daemon's own reader, leakguard.Source, of LeakCheckRepository over LeakCheckAPI's stand-in with
// its token.
func LeakCheck(t *testing.T) *leakguard.Source {
	t.Helper()
	return &leakguard.Source{Repository: LeakCheckRepository, API: LeakCheckAPI(t, leakCheckToken),
		Token: func(context.Context) (string, error) { return leakCheckToken, nil }}
}

// leakCheckTagPin is the worker image's pin of the shared public leak check's tag, as
// packages/daemon/docker/worker.Dockerfile writes it (leakguard.TagVariable's source).
var leakCheckTagPin = regexp.MustCompile(`(?m)^ARG LEAK_CHECK_TAG=(\S+)$`)

// ServeLeakCheck serves LeakCheckRepository from the rig's fake GitHub, private, with standInFiles
// as its leak-check directory, at the tag the worker image pins, and returns that tag: the check a
// whole daemon under LeakCheckSetting reads through its GitHub REST root with the implement App's
// read lease on LeakCheckRepository's owner, which that App must be installed on
// (fakegithub.Server.AddApp), and pushes to each pod's launcher.
func (r *Rig) ServeLeakCheck(t *testing.T) string {
	t.Helper()
	dockerfile, err := os.ReadFile(filepath.Join(RepositoryRoot(t), "packages", "daemon", "docker", "worker.Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	pin := leakCheckTagPin.FindSubmatch(dockerfile)
	if pin == nil {
		t.Fatal("worker.Dockerfile pins no LEAK_CHECK_TAG")
	}
	tag := string(pin[1])
	repo := r.GitHub.NewRepository(t, LeakCheckRepository.Owner(), LeakCheckRepository.Name(), false)
	git := func(stdin string, args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"--git-dir=" + repo.Bare}, args...)...)
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=kind", "GIT_AUTHOR_EMAIL=kind@example.com", "GIT_COMMITTER_NAME=kind", "GIT_COMMITTER_EMAIL=kind@example.com")
		command.Stdin = strings.NewReader(stdin)
		out, err := command.Output()
		if err != nil {
			t.Fatalf("seed %s: git %s: %v", repo.Slug(), strings.Join(args, " "), err)
		}
		return strings.TrimSpace(string(out))
	}
	var entries strings.Builder
	for _, name := range slices.Sorted(maps.Keys(standInFiles)) {
		member := standInFiles[name]
		mode := "100644"
		if member.Mode&0o111 != 0 {
			mode = "100755"
		}
		fmt.Fprintf(&entries, "%s blob %s\t%s\n", mode, git(member.Body, "hash-object", "-w", "--stdin"), name)
	}
	directory := git(entries.String(), "mktree")
	rest := git("the rest of the repository\n", "hash-object", "-w", "--stdin")
	tree := git("100644 blob "+rest+"\tCONTRIBUTING.md\n040000 tree "+directory+"\tleak-check\n", "mktree")
	commit := git("", "commit-tree", tree, "-p", "main", "-m", "The stand-in leak check")
	git("", "update-ref", "refs/heads/main", commit)
	git("", "update-ref", "refs/tags/"+tag, commit)
	return tag
}

// AppTokenCommand is a github_apps.<role>.token_command for app, installed on owners, whose every
// token the fake GitHub takes: the stand-in for a secrets broker's release of an App's
// installation tokens (appauth's token mode, tokencommand.go). The command asks a server of this
// process's own, on its loopback, for the release appauth reads, minted by the fake GitHub
// (fakegithub.Server.InstallationToken) for the owner LEGION_GITHUB_OWNER names, or for each of
// owners where it is empty, each read-only where LEGION_GITHUB_READ_ONLY is 1 and able to push
// otherwise. The server answers until the test ends.
func (r *Rig) AppTokenCommand(t *testing.T, app fakegithub.App, owners ...string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		wanted := owners
		if owner := query.Get("owner"); owner != "" {
			wanted = []string{owner}
		}
		installations := map[string]any{}
		for _, owner := range wanted {
			token, installation, expires := r.GitHub.InstallationToken(t, app, owner, query.Get("read_only") != "1")
			if token == "" {
				http.Error(w, "the App "+app.Slug+" is not installed on "+owner, http.StatusNotFound)
				return
			}
			installations[owner] = map[string]any{"installation_id": installation, "token": token, "expires_at": expires.Format(time.RFC3339)}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"installations": installations})
	}))
	t.Cleanup(server.Close)
	return fmt.Sprintf(`curl -fsS "%s/release?owner=${%s:-}&read_only=${%s:-}"`, server.URL, appauth.TokenCommandOwner, appauth.TokenCommandReadOnly)
}

// ServiceAccountToken is a token the cluster's API server issues for the ServiceAccount
// namespace/account, for audience (the API server's own where empty), living lifetime: what a pod
// of that account is projected, minted from this process (`kubectl create token`). It fails no
// test, so a goroutine that mints the next one before the last expires may call it.
func (r *Rig) ServiceAccountToken(namespace, account, audience string, lifetime time.Duration) (string, error) {
	args := []string{"-n", namespace, "create", "token", account, "--duration", lifetime.String()}
	if audience != "" {
		args = append(args, "--audience", audience)
	}
	out, err := r.kubectl(nil, args...)
	token := strings.TrimSpace(out)
	if err != nil || token == "" {
		return "", fmt.Errorf("kubectl create token %s/%s: %v: printed %d bytes", namespace, account, err, len(token))
	}
	return token, nil
}

// IssuerClient is an HTTP client of this process that trusts the cluster's own authority alone,
// the one its API server's certificate is signed by: what reads the service-account issuer
// (Issuer), its discovery document and its keys, as a verifier of the cluster's tokens does.
func (r *Rig) IssuerClient(t *testing.T) *http.Client {
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
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
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
	response, err := r.IssuerClient(t).Get(r.Issuer + "/.well-known/openid-configuration")
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
// Docker holds it under, with a line for the test's log that says so: a pull of ImageEnv's
// reference, or a load of ImageArchiveEnv's archive. It runs on a goroutine of its own beside
// other pieces of Start, so it returns its failure rather than failing a test, and what it starts
// is killed when ctx ends.
func loadWorkerImage(ctx context.Context) (string, string, error) {
	if reference := os.Getenv(ImageEnv); reference != "" {
		if !strings.Contains(reference, "@sha256:") {
			return "", "", fmt.Errorf("%s must name the worker image by digest (<image>@sha256:<64 hex>), got %s", ImageEnv, reference)
		}
		if out, err := exec.CommandContext(ctx, "docker", "pull", "--quiet", reference).CombinedOutput(); err != nil {
			return "", "", fmt.Errorf("docker pull --quiet %s: %v: %s", reference, err, out)
		}
		return reference, "pulled the worker image " + reference, nil
	}
	archive := os.Getenv(ImageArchiveEnv)
	var out string
	var err error
	if strings.HasSuffix(archive, ".zst") {
		out, err = loadZstdArchive(ctx, archive)
	} else {
		var printed []byte
		printed, err = exec.CommandContext(ctx, "docker", "load", "--input", archive).CombinedOutput()
		out = string(printed)
		if err != nil {
			err = fmt.Errorf("docker load --input %s: %v: %s", archive, err, out)
		}
	}
	if err != nil {
		return "", "", err
	}
	loaded := regexp.MustCompile(`(?m)^Loaded image(?: ID)?: (\S+)$`).FindStringSubmatch(out)
	if loaded == nil {
		return "", "", fmt.Errorf("docker load of %s named no image: %s", archive, out)
	}
	// The archive stays: each kind test of the job brings up a rig of its own, and each loads the
	// image from it again.
	return loaded[1], fmt.Sprintf("loaded the worker image %s from %s", loaded[1], archive), nil
}

// loadZstdArchive runs `zstd --decompress --stdout --long=31 archive | docker load` and returns
// what docker load printed: zstd decompresses the archive as docker reads it, so no uncompressed
// copy of the multi-gigabyte image lands on the disk. --long=31 admits the long window the
// kind-image job compresses with (.github/actions/kind/image). Each end of the pipe is closed in
// this process once its command holds it, so a docker load that stops early ends zstd's writes
// rather than leaving zstd blocked on a full pipe. Both are killed when ctx ends.
func loadZstdArchive(ctx context.Context, archive string) (string, error) {
	read, write, err := os.Pipe()
	if err != nil {
		return "", fmt.Errorf("a pipe from zstd to docker load: %w", err)
	}
	var zstdErr, loadOut bytes.Buffer
	unzstd := exec.CommandContext(ctx, "zstd", "--decompress", "--stdout", "--long=31", archive)
	unzstd.Stdout, unzstd.Stderr = write, &zstdErr
	load := exec.CommandContext(ctx, "docker", "load")
	load.Stdin, load.Stdout, load.Stderr = read, &loadOut, &loadOut
	if err := unzstd.Start(); err != nil {
		read.Close()
		write.Close()
		return "", fmt.Errorf("zstd --decompress %s: %w", archive, err)
	}
	write.Close()
	loadStarted := load.Start()
	read.Close()
	if loadStarted != nil {
		_ = unzstd.Process.Kill()
		_ = unzstd.Wait()
		return "", fmt.Errorf("docker load: %w", loadStarted)
	}
	loadErr := load.Wait()
	unzstdErr := unzstd.Wait()
	if loadErr != nil || unzstdErr != nil {
		return "", fmt.Errorf("zstd --decompress --stdout --long=31 %s | docker load: zstd: %v %s; docker load: %v %s",
			archive, unzstdErr, strings.TrimSpace(zstdErr.String()), loadErr, strings.TrimSpace(loadOut.String()))
	}
	return loadOut.String(), nil
}

// buildStandInImage builds, FROM worker, the image every pod of a kind test starts, under the user
// worker runs as, which reads the rig's stand-ins as the real services:
//
//   - the fake GitHub's authority and the TLS sites' (TLSSites.AuthorityPEM) in the system trust
//     store, which git, curl, wget, python3, aws, gcloud, redis-cli and every Go program of the
//     image read;
//   - the same two in NODE_EXTRA_CA_CERTS, which node and bun read beside their own roots;
//   - UV_SYSTEM_CERTS, so uv reads the system store in place of its own roots;
//   - the same two in Chrome for Testing's managed CACertificates policy, since Chromium reads no
//     file of the system store.
//
// Debian's update-ca-certificates is called by its full path, /usr/sbin/update-ca-certificates,
// since the worker image's PATH (its ENV, packages/daemon/docker/worker.Dockerfile) holds no
// /usr/sbin and a RUN of the bare name exits 127.
//
// The build fetches nothing: worker is in this host's Docker already, and its one RUN has no
// network (--network none), since the kind job's rule on the test's own connections does not
// reach the containers Docker starts.
func (r *Rig) buildStandInImage(t *testing.T, worker string) string {
	t.Helper()
	dir := filepath.Join(r.work, "stand-in-image")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	authorities := slices.Concat(r.GitHub.CAPEM(), r.Sites.AuthorityPEM())
	var policy struct {
		CACertificates []string `json:"CACertificates"`
	}
	for rest := authorities; ; {
		var block *pem.Block
		if block, rest = pem.Decode(rest); block == nil {
			break
		}
		policy.CACertificates = append(policy.CACertificates, base64.StdEncoding.EncodeToString(block.Bytes))
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"fake-github-ca.crt": r.GitHub.CAPEM(), "tls-sites-ca.crt": r.Sites.AuthorityPEM(),
		"authorities.pem": authorities, "chrome-policy.json": policyJSON,
	}
	dockerfile := "FROM " + worker + "\nUSER root\n" +
		"COPY fake-github-ca.crt tls-sites-ca.crt /usr/local/share/ca-certificates/\n" +
		"COPY authorities.pem /usr/local/share/legion-kind/authorities.pem\n" +
		"COPY chrome-policy.json /etc/opt/chrome_for_testing/policies/managed/legion-kind.json\n"
	dockerfile += "RUN /usr/sbin/update-ca-certificates\n" +
		"ENV NODE_EXTRA_CA_CERTS=/usr/local/share/legion-kind/authorities.pem UV_SYSTEM_CERTS=1\n"
	user := strings.TrimSpace(r.docker(t, "image", "inspect", "--format", "{{.Config.User}}", worker))
	if user != "" {
		dockerfile += "USER " + user + "\n"
	}
	files["Dockerfile"] = []byte(dockerfile)
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tag := "legion-worker-stand-ins:" + r.Name
	r.docker(t, "build", "--quiet", "--network", "none", "--tag", tag, dir)
	t.Logf("built %s FROM %s, reading the rig's stand-ins, as user %q", tag, worker, user)
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

// teardown keeps the TLS stand-ins' requests, each with its body, on every run, and logs the
// web-search stand-in's, so the job's artifact holds what the pods asked each stand-in; when the
// test failed, it keeps the fake GitHub's requests and what the cluster held too. Then it deletes
// the cluster, the route to its pods and the registry. The script's status 3 is
// kind_sandbox_teardown's verdict that CHECK kind-node-pulls failed (a node pulled an image from
// outside ghcr.io/cybermonklabs/legion/ci and the rig's registry, or its journal holds no pull),
// which fails the test; any other failure of the teardown is logged.
func (r *Rig) teardown(t *testing.T) {
	status := "0"
	if r.Sites != nil {
		requests := r.Sites.Requests()
		if err := os.WriteFile(filepath.Join(r.Evidence, "tls-sites-requests.txt"), []byte(strings.Join(requests, "\n")+"\n"), 0o644); err != nil {
			t.Logf("keep the TLS stand-ins' requests: %v", err)
		}
		var searches []string
		for _, request := range requests {
			if strings.Contains(request, " "+WebSearchHost+"/") {
				searches = append(searches, request)
			}
		}
		t.Logf("the web-search stand-in %s answered %d requests:\n%s", WebSearchHost, len(searches), strings.Join(searches, "\n"))
	}
	if t.Failed() {
		status = "1"
		if r.GitHub != nil {
			requests := strings.Join(r.GitHub.Requests(), "\n") + "\n"
			if err := os.WriteFile(filepath.Join(r.Evidence, "fake-github-requests.txt"), []byte(requests), 0o644); err != nil {
				t.Logf("keep the fake GitHub's requests: %v", err)
			}
		}
		t.Logf("the rig's state at the failure is in %s", r.Evidence)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	const script = `note() { printf '   %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
. "$1"
kind_sandbox_use "$2" "$3"
kind_sandbox_teardown "$4" "$5"`
	command := exec.CommandContext(ctx, "bash", "-c", script, "kind-rig", r.lib, r.Name, r.work, status, r.Evidence)
	command.Env = append(os.Environ(), "LEGION_E2E_KIND="+r.kind)
	out, err := command.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() == 3:
		t.Errorf("teardown of %s: CHECK kind-node-pulls failed, as its lines say:\n%s", r.Name, out)
	case err != nil || len(out) > 0:
		t.Logf("teardown of %s: %v: %s", r.Name, err, out)
	}
}

// evidenceDir is where a test's evidence goes (its stand-ins' requests and its nodes' containerd
// journals, and at a failure the cluster's state): a directory of the test's own under LogDirEnv,
// which the kind job uploads, or, where that is unset, a directory that outlives the test, named in
// the log.
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

// RepositoryRoot is the checkout this test runs in: the nearest directory above the working
// directory that holds the rig's library, which a kind test reads its repository's own files from.
func RepositoryRoot(t *testing.T) string {
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
