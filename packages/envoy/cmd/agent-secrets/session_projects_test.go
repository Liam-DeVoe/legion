package main

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sjawhar/envoy/internal/broker/helper"
	"github.com/sjawhar/envoy/internal/brokerclient"
)

// recordingHelper listens at a fresh socket, answers every op as enrolled, and records each
// request it got.
func recordingHelper(t *testing.T) (string, func() []helper.Request) {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), "h.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var got []helper.Request
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadBytes('\n')
			var req helper.Request
			_ = json.Unmarshal(line, &req)
			mu.Lock()
			got = append(got, req)
			mu.Unlock()
			data, _ := json.Marshal(helper.Response{OK: true, State: "enrolled", RuntimeID: "rt", EnrollmentID: "e"})
			_, _ = conn.Write(append(data, '\n'))
			_ = conn.Close()
		}
	}()
	return sock, func() []helper.Request {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

// TestRegisterReadsAgentSecretsProjects: register and enroll --helper name the session's projects
// to the helper from AGENT_SECRETS_PROJECTS, sorted, and --projects overrides the variable; a list
// that is no list is refused before the helper is asked.
func TestRegisterReadsAgentSecretsProjects(t *testing.T) {
	sock, requests := recordingHelper(t)
	t.Setenv("AGENT_SECRETS_HELPER_SOCK", sock)
	t.Setenv(brokerclient.ProjectsVar, "PUB PRIV")
	t.Setenv("AGENT_SECRETS_KEY_DIR", t.TempDir())
	var out, errOut strings.Builder
	if code := cmdRegister(nil, &out, &errOut); code != 0 {
		t.Fatalf("register: exit %d stderr %q", code, errOut.String())
	}
	if code := cmdRegister([]string{"--projects", "CORE"}, &out, &errOut); code != 0 {
		t.Fatalf("register --projects CORE: exit %d stderr %q", code, errOut.String())
	}
	if code := cmdEnroll([]string{"--helper", "--runtime-id", "box:1", "--thumbprint", "tp"}, &out, &errOut); code != 0 {
		t.Fatalf("enroll --helper: exit %d stderr %q", code, errOut.String())
	}
	got := requests()
	if len(got) != 3 {
		t.Fatalf("the helper got %d requests, want 3: %+v", len(got), got)
	}
	for i, want := range [][]string{{"PRIV", "PUB"}, {"CORE"}, {"PRIV", "PUB"}} {
		if !slices.Equal(got[i].Projects, want) {
			t.Errorf("request %d (%s) names projects %v, want %v", i, got[i].Op, got[i].Projects, want)
		}
	}
	errOut.Reset()
	if code := cmdRegister([]string{"--projects", "core"}, &out, &errOut); code != exitUsageError || !strings.Contains(errOut.String(), "--projects") {
		t.Fatalf("register --projects core: exit %d stderr %q; want a usage error naming --projects", code, errOut.String())
	}
	t.Setenv(brokerclient.ProjectsVar, "PRIV PRIV")
	errOut.Reset()
	if code := cmdRegister(nil, &out, &errOut); code != exitUsageError || !strings.Contains(errOut.String(), brokerclient.ProjectsVar) {
		t.Fatalf("register under %s=PRIV PRIV: exit %d stderr %q; want a usage error naming the variable", brokerclient.ProjectsVar, code, errOut.String())
	}
	if n := len(requests()); n != 3 {
		t.Fatalf("a refused list reached the helper: it got %d requests, want 3", n)
	}
}

// TestTheRegisterAndEnrollSynopsesNameProjects: the usage line of register, and of enroll's
// --helper half, names --projects, the option that names a session's Dispatch projects; enroll's
// --pod half refuses the option, so its half names none.
func TestTheRegisterAndEnrollSynopsesNameProjects(t *testing.T) {
	if synopsis := lookupCommand("register").synopsis; !strings.Contains(synopsis, "[--projects ") {
		t.Errorf("register's synopsis %q names no --projects", synopsis)
	}
	synopsis := lookupCommand("enroll").synopsis
	helperHalf, podHalf, found := strings.Cut(synopsis, " | ")
	if !found || !strings.Contains(helperHalf, "[--projects ") || strings.Contains(podHalf, "--projects") {
		t.Errorf("enroll's synopsis %q: want --projects in its --helper half and not in its --pod half", synopsis)
	}
}

// TestADenialForAnUnnamedProjectSaysHowToNameIt: a request the broker denies project-not-named
// ends with the line that says to start the session with AGENT_SECRETS_PROJECTS naming the
// project, in the request form and the exec form; the line names no project, and a denial for
// any other reason adds nothing.
func TestADenialForAnUnnamedProjectSaysHowToNameIt(t *testing.T) {
	binary := buildAgentSecrets(t)
	broker, _ := fakeBroker(t)
	defer broker.Close()
	keyDir := newKeyDir(t)
	for _, args := range [][]string{
		{"request", "PROJECT_DENIED"},
		{"PROJECT_DENIED", "--", "sh", "-c", "echo ran-the-child"},
	} {
		stdout, stderr, exit := runAgentSecrets(t, binary, broker.URL, keyDir, nil, args...)
		if exit != exitDenied || !strings.Contains(stderr, projectNotNamedHint+"\n") || strings.Contains(stdout, "ran-the-child") {
			t.Errorf("%q = exit %d stdout %q stderr %q; want exit %d and the line %q", args, exit, stdout, stderr, exitDenied, projectNotNamedHint)
		}
	}
	for _, args := range [][]string{{"request", "DETAIL_DENIED"}, {"request", "DENY_ME"}} {
		_, stderr, _ := runAgentSecrets(t, binary, broker.URL, keyDir, nil, args...)
		if strings.Contains(stderr, brokerclient.ProjectsVar) {
			t.Errorf("%q printed the project line for a denial of another reason: %q", args, stderr)
		}
	}
}
