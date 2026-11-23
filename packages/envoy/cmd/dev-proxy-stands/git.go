// packages/envoy/cmd/dev-proxy-stands/git.go
// The `git` subcommand: a smart-HTTP git server over TLS, the stand-in for github.com that check
// k9's shallow clone reaches through the airlock (scripts/airlock-check.sh), so the check reads
// from no host outside the machine. It serves each bare repository under --root, read only, as
// git's own http-backend serves it - /<owner>/<name> for <root>/<owner>/<name>.git - behind one
// certificate for every --host, signed by the stack's authority (--ca-cert and --ca-key, the
// files the stands wrote), which the clone's CA file holds. A clone of
// https://github.com/<owner>/<name> that reaches this address verifies; one of any other host
// does not.
//
// It writes one JSON object to standard output naming its address, logs "git listening
// addr=<address>" and one line for each request, and serves until it is interrupted.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/proxyrules/proxyrulestest"
)

// gitReport is the JSON the git stand-in writes to standard output once it is bound.
type gitReport struct {
	// Address is the host:port it listens on, with the port the system picked resolved.
	Address string `json:"address"`
}

// nameFlags is a repeated name flag: the git stand-in's --host.
type nameFlags []string

func (f *nameFlags) String() string { return "" }

func (f *nameFlags) Set(value string) error {
	if value == "" {
		return errors.New("want a host name")
	}
	*f = append(*f, value)
	return nil
}

// runGit is `dev-airlock-stands git`.
func runGit(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dev-airlock-stands git", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "127.0.0.1:0", "bind `<host:port>`, port 0 for one the system picks")
	root := fs.String("root", "", "serve the bare repositories under `<path>`, each <owner>/<name>.git at /<owner>/<name>")
	caCert := fs.String("ca-cert", "", "the stack authority's certificate, `<path>`")
	caKey := fs.String("ca-key", "", "the stack authority's key, `<path>`")
	var hosts nameFlags
	fs.Var(&hosts, "host", "a `<name>` the certificate carries (repeatable)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitUsageError
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "dev-airlock-stands git: unexpected argument %q\n", fs.Arg(0))
		return exitUsageError
	}
	if *root == "" || *caCert == "" || *caKey == "" || len(hosts) == 0 {
		fmt.Fprintln(stderr, "dev-airlock-stands git: name --root, --ca-cert, --ca-key and at least one --host")
		return exitUsageError
	}
	projectRoot, err := filepath.Abs(*root)
	if err == nil {
		_, err = os.Stat(projectRoot)
	}
	if err != nil {
		fmt.Fprintf(stderr, "dev-airlock-stands git: --root: %v\n", err)
		return 1
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		fmt.Fprintf(stderr, "dev-airlock-stands git: %v\n", err)
		return 1
	}
	authority, err := proxyrulestest.ReadAuthority(*caCert, *caKey)
	if err != nil {
		fmt.Fprintf(stderr, "dev-airlock-stands git: %v\n", err)
		return 1
	}
	leaf, _, err := authority.Leaf(hosts)
	if err != nil {
		fmt.Fprintf(stderr, "dev-airlock-stands git: %v\n", err)
		return 1
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "dev-airlock-stands git: %v\n", err)
		return 1
	}
	addr := listener.Addr().String()
	if err := json.NewEncoder(stdout).Encode(gitReport{Address: addr}); err != nil {
		_ = listener.Close()
		fmt.Fprintf(stderr, "dev-airlock-stands git: write the report: %v\n", err)
		return 1
	}
	// Each request is served on a goroutine of its own, and http-backend's own errors are copied
	// in from another, so every line reaches stderr through one lock.
	logs := &lockedWriter{w: stderr}
	backend := &cgi.Handler{
		Path: gitPath, Args: []string{"http-backend"}, Stderr: logs,
		Env: []string{"GIT_PROJECT_ROOT=" + projectRoot, "GIT_HTTP_EXPORT_ALL=1"},
	}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(logs, "dev-airlock-stands git: %s %s %s\n", r.Host, r.Method, r.URL.RequestURI())
			backend.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{*leaf}, MinVersion: tls.VersionTLS12},
		ErrorLog:          log.New(logs, "dev-airlock-stands git: ", 0),
	}
	fmt.Fprintf(logs, "dev-airlock-stands: git listening addr=%s root=%s hosts=%v\n", addr, projectRoot, []string(hosts))
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	if err := server.ServeTLS(listener, "", ""); err != nil && ctx.Err() == nil {
		fmt.Fprintf(logs, "dev-airlock-stands git: %v\n", err)
		return 1
	}
	return 0
}

// lockedWriter is a writer several goroutines write whole lines to.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
