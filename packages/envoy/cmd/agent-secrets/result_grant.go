package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/brokerclient"
	"github.com/sjawhar/envoy/internal/redact"
)

type resultGrantRelease struct {
	GrantID   string          `json:"grant_id"`
	ExpiresAt *time.Time      `json:"expires_at"`
	Airlock   *AirlockRelease `json:"airlock"`
}

// cmdResultGrant proves the job in Go, then gives the child only the broker's placeholder.
// The job's result access token and the DPoP key never pass through this process.
func cmdResultGrant(args []string, stdout, stderr io.Writer) int {
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	before := args
	if sep >= 0 {
		before = args[:sep]
	}
	flags := newFlagSet("result-grant", stderr)
	if err := flags.Parse(before); err != nil {
		return exitUsage(err)
	}
	if flags.NArg() != 0 || sep < 0 || sep+1 >= len(args) {
		fmt.Fprintln(stderr, "agent-secrets result-grant: use result-grant -- COMMAND [ARGS...]")
		return exitUsageError
	}
	command := args[sep+1:]
	path, err := exec.LookPath(command[0])
	if err != nil {
		fmt.Fprintln(stderr, "agent-secrets result-grant: command not found")
		return exitNotFound
	}
	base := brokerBaseURL()
	if base.Empty() {
		fmt.Fprintln(stderr, "agent-secrets result-grant: AGENT_SECRETS_URL is required")
		return exitUsageError
	}
	source, err := brokerclient.MachineIdentity(os.Getenv)
	if err != nil || source == nil {
		fmt.Fprintln(stderr, "agent-secrets result-grant: a GitHub workflow identity is required")
		return exitUsageError
	}
	switch source.(type) {
	case *brokerclient.GitHubActionsIdentity, *brokerclient.WorkflowRunTokenFile:
	default:
		fmt.Fprintln(stderr, "agent-secrets result-grant: only a GitHub workflow identity can request results")
		return exitUsageError
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	subject, kind, err := source.Token(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "agent-secrets result-grant: %s\n", redact.OneLine(err.Error()))
		return 1
	}
	if kind != oauthwire.SubjectTokenTypeGitHub {
		fmt.Fprintln(stderr, "agent-secrets result-grant: only a GitHub workflow identity can request results")
		return exitUsageError
	}
	body, err := json.Marshal(struct {
		SubjectToken string `json:"subject_token"`
	}{subject})
	if err != nil {
		return 1
	}
	c := newClient(base)
	data, err := c.do(ctx, http.MethodPost, c.route("/v1/oauth/result-grants"), body, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		fmt.Fprintf(stderr, "agent-secrets result-grant: %s\n", redact.OneLine(strings.ReplaceAll(err.Error(), subject, "[redacted]")))
		return 1
	}
	var release resultGrantRelease
	if json.Unmarshal(data, &release) != nil || release.Airlock == nil || release.GrantID == "" || release.ExpiresAt == nil || !time.Now().Before(*release.ExpiresAt) || release.Airlock.Names["DISPATCH_RESULTS_TOKEN"].Value == "" {
		fmt.Fprintln(stderr, "agent-secrets result-grant: the broker returned no live results airlock; nothing was run")
		return 1
	}
	files, err := prepareAirlock(os.Environ(), release.Airlock, release.GrantID, release.ExpiresAt)
	if err != nil {
		fmt.Fprintf(stderr, "agent-secrets result-grant: %s\n", redact.OneLine(err.Error()))
		return 1
	}
	environment, err := buildChildEnv(os.Environ(), nil, release.Airlock, files)
	if err != nil {
		fmt.Fprintf(stderr, "agent-secrets result-grant: %s\n", redact.OneLine(err.Error()))
		return 1
	}
	if err := syscall.Exec(path, command, environment); err != nil {
		fmt.Fprintln(stderr, "agent-secrets result-grant: could not execute the command")
		return exitCannotRun
	}
	return 0
}
