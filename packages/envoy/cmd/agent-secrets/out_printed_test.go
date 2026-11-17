package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnrollPodNeverPrintsTheOutDirectory: the directory --out (or AGENT_SECRETS_KEY_DIR) names
// holds key.pem, a file that holds a credential, so -h keeps its value out (enroll.go) and
// outKeyFile names key.pem by the flag. A refusal about the directory itself names it the same
// way: a directory that cannot be made and a lock that cannot be opened in it printed its path.
// Control: an --out holding the secret that is made fine, with the refusal elsewhere (the token
// file is missing), prints no part of it.
func TestEnrollPodNeverPrintsTheOutDirectory(t *testing.T) {
	binary := buildAgentSecrets(t)
	const secret = "hunter2outdir5e"
	dir := t.TempDir()
	readOnly := filepath.Join(dir, "ro-"+secret)
	if err := os.MkdirAll(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(dir, "missing-token")
	for _, row := range []struct {
		name, setting string
		env, args     []string
	}{
		{"--out under a directory that cannot be made", "--out", nil, []string{"--out", "/proc/" + secret + "/out"}},
		{"AGENT_SECRETS_KEY_DIR under a directory that cannot be made", "AGENT_SECRETS_KEY_DIR", []string{"AGENT_SECRETS_KEY_DIR=/proc/" + secret + "/out"}, nil},
		{"--out a directory this user cannot write", "--out", nil, []string{"--out", readOnly}},
		{"control: --out made fine, token missing", "--pod-token-file", nil, []string{"--out", filepath.Join(dir, "ok-"+secret)}},
	} {
		t.Run(row.name, func(t *testing.T) {
			args := append([]string{"enroll", "--pod", "--service", "svc", "--pod-token-file", token}, row.args...)
			_, stderr, exit := runAgentSecrets(t, binary, "http://127.0.0.1:1", "", row.env, args...)
			t.Logf("exit %d, stderr %q", exit, stderr)
			if exit == 0 || stderr == "" {
				t.Fatalf("exit %d with stderr %q; want a refusal", exit, stderr)
			}
			if strings.Contains(stderr, secret) {
				t.Errorf("stderr prints the directory: %q", stderr)
			}
			if !strings.Contains(stderr, row.setting) {
				t.Errorf("stderr %q does not name %s", stderr, row.setting)
			}
		})
	}
}

// TestAnArgumentAFormDoesNotTakeIsNotEchoed: an argument a form refuses is where a credential
// pasted in the wrong place shows up, so enroll, unenroll and the machine forms refuse it without
// printing it. Control: the refusal still says what was wrong, and exits as a usage error.
func TestAnArgumentAFormDoesNotTakeIsNotEchoed(t *testing.T) {
	binary := buildAgentSecrets(t)
	const secret = "hunter2pw-posarg-61c2"
	sock := filepath.Join(t.TempDir(), "none.sock")
	for _, args := range [][]string{
		{"enroll", "--helper", secret},
		{"unenroll", secret},
		{"machine", "login", secret},
		{"machine", "login-status", secret},
		{"machine", "list", secret},
	} {
		_, stderr, exit := runAgentSecrets(t, binary, "http://unused", t.TempDir(), []string{"AGENT_SECRETS_HELPER_SOCK=" + sock}, args...)
		if exit != exitUsageError || strings.Contains(stderr, secret) || !strings.Contains(stderr, "unexpected argument") {
			t.Errorf("agent-secrets %s: exit %d, stderr %q; want a usage error naming the unexpected argument and not printing it", strings.Join(args[:len(args)-1], " "), exit, stderr)
		}
	}
}
