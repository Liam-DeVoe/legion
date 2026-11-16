package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/lookpath"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// ProviderEnvDir is the daemon-held directory of provider keys, under the state directory's
// secrets: one directory per set of keys a write made (MaterializeProviderKeys), each holding
// one 0600 file per key, named for the key. Every pane's shim is pointed at one set
// (`--provider-env-dir`) and exports each of its files into OMP's environment alone.
func ProviderEnvDir(stateDir string) string {
	return filepath.Join(runtime.SecretsDir(stateDir), "provider-env")
}

// MaterializeProviderKeys resolves each provider key and writes its value to a file named for the
// variable OMP reads, in a new directory under ProviderEnvDir, and returns that directory — or ""
// when there are none, writing nothing. Boot calls it before any pane can launch, and a build of
// the tmux runtime again whenever the keys or their files have moved since (internal/daemon's
// providerKeyFiles, which also decides which sets stay: RemoveProviderKeysBut).
// A pane never reaches the secret store: under tmux, identity reaches panes as daemon-held files
// (LEGION-208 spec, Identity), because the store's age identity would decrypt every agent-tier
// secret on the box.
//
// A key whose source is a file (`file:/abs/path`, ProviderKey.File) is that file's contents,
// trimmed, read as the daemon: a host with no secretsd hands the daemon its keys as files only it
// can read. Each secretsd key is resolved in two steps: `secrets get <KEY> --no-request` first,
// which reports the key's tier without costing a tap, then `secrets get <KEY> --value`. A human-tier
// key is announced before its value is requested, since a YubiKey tap may be needed. The daemon runs
// `secrets` for itself — it is never a pane — under its own environment (environ) less
// SECRETSD_SESSION_TOKEN_FILE, with its own stdin: secretsd scopes a tokenless caller by its
// terminal, so the request is the launcher's and never lands on an agent
// session the daemon was started from. There is no daemon-imposed timeout; secretsd's own approval
// window is the failure.
//
// Every key is resolved before any file is written, so a write that refuses writes nothing. The
// directory is this write's own and holds exactly the configured variables: it is whole before it
// is answered, so nothing can be told it earlier, and nothing writes it afterwards, so a shim that
// reads it reads the whole set — never part of one, nor a mix of two. ProviderEnvDir and each set
// are 0700 and each file 0600. Every refusal names the entry — the variable and its secretsd key
// or file — and never the value.
func MaterializeProviderKeys(keys []ProviderKey, stateDir string, environ []string, log *slog.Logger) (string, error) {
	if len(keys) == 0 {
		return "", nil
	}
	env := make([]string, 0, len(environ))
	for _, entry := range environ {
		if !strings.HasPrefix(entry, "SECRETSD_SESSION_TOKEN_FILE=") {
			env = append(env, entry)
		}
	}
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		resolve := resolveProviderKey
		if key.File != "" {
			resolve = readProviderKeyFile
		}
		value, err := resolve(key, env, log)
		if err != nil {
			return "", err
		}
		values[key.Env] = value
	}

	parent := ProviderEnvDir(stateDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("provider_keys: create %s: %w", parent, err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return "", fmt.Errorf("provider_keys: %w", err)
	}
	dir, err := os.MkdirTemp(parent, "keys-")
	if err != nil {
		return "", fmt.Errorf("provider_keys: create a directory under %s: %w", parent, err)
	}
	for _, key := range keys {
		if err := writeKeyFile(filepath.Join(dir, key.Env), values[key.Env]); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("provider_keys.%s: write its file: %w", key.Env, err)
		}
	}
	return dir, nil
}

// RemoveProviderKeysBut removes every set of keys under ProviderEnvDir that keep does not name,
// with anything else the directory holds (the files a daemon before sets wrote straight into it),
// and the directory itself when keep names none of its entries. Each is moved out of its place
// before it is removed, so a shim reading a set as it goes reads all of it or is refused its read
// (shim.ReadProviderEnv), never a part of it.
func RemoveProviderKeysBut(stateDir string, keep []string) error {
	parent := ProviderEnvDir(stateDir)
	entries, err := os.ReadDir(parent)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("provider_keys: list %s: %w", parent, err)
	}
	kept := false
	for _, entry := range entries {
		path := filepath.Join(parent, entry.Name())
		if slices.Contains(keep, path) {
			kept = true
			continue
		}
		gone := filepath.Join(parent, ".removed-"+entry.Name())
		if err := os.RemoveAll(gone); err != nil {
			return fmt.Errorf("provider_keys: remove %s: %w", gone, err)
		}
		if err := os.Rename(path, gone); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("provider_keys: move %s out of its place: %w", path, err)
		}
		if err := os.RemoveAll(gone); err != nil {
			return fmt.Errorf("provider_keys: remove %s: %w", gone, err)
		}
	}
	if kept {
		return nil
	}
	if err := os.Remove(parent); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("provider_keys: remove %s, which holds no key: %w", parent, err)
	}
	return nil
}

// secretsStatus is what `secrets get <KEY> --no-request` prints: the key and its tier.
type secretsStatus struct {
	Key  string `json:"key"`
	Tier string `json:"tier"`
}

// readProviderKeyFile is one entry's value from the file it names, read by
// ReadPrivateSecretPointer's rules, as every other secret file the daemon reads: refused, naming the
// file and never its contents, when it cannot be read, is not a regular file, holds nothing, or is
// readable by its group or others.
func readProviderKeyFile(key ProviderKey, _ []string, _ *slog.Logger) (string, error) {
	return ReadPrivateSecretPointer("provider_keys."+key.Env, key.File)
}

// resolveProviderKey is one entry's value: its secretsd key's tier checked, then the value read.
func resolveProviderKey(key ProviderKey, env []string, log *slog.Logger) (string, error) {
	statusText, err := runSecretsGet(key, "--no-request", env)
	if err != nil {
		return "", err
	}
	unparsable := fmt.Errorf(`provider_keys.%s: secrets get %s --no-request printed an unparsable status (expected {"key","tier"})`, key.Env, key.Secret)
	var status secretsStatus
	if err := json.Unmarshal(bytes.TrimSpace(statusText), &status); err != nil || status.Key == "" || status.Tier == "" {
		return "", unparsable
	}
	// A status for some other key is not a status for this one.
	if status.Key != key.Secret {
		return "", unparsable
	}
	switch status.Tier {
	case "agent":
	case "human":
		log.Warn(fmt.Sprintf("requesting %s from secretsd (human tier; a YubiKey tap may be needed)", key.Secret))
	default:
		return "", fmt.Errorf("provider_keys.%s: secrets get %s --no-request reported tier %q, which is neither agent nor human", key.Env, key.Secret, status.Tier)
	}
	value, err := runSecretsGet(key, "--value", env)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" {
		return "", fmt.Errorf("provider_keys.%s: secrets get %s --value printed no value", key.Env, key.Secret)
	}
	return trimmed, nil
}

// runSecretsGet runs `secrets get <key.Secret> <flag>` — found on env's PATH — with the daemon's
// stdin and env, returning its stdout. A failure carries the exit status and the child's stderr,
// never its stdout.
func runSecretsGet(key ProviderKey, flag string, env []string) ([]byte, error) {
	args := []string{"get", key.Secret, flag}
	binary, ok := lookpath.On(pathValue(env), "secrets")
	if !ok {
		return nil, fmt.Errorf("provider_keys.%s: the secrets command is not on PATH, so %s cannot be read", key.Env, key.Secret)
	}
	cmd := exec.Command(binary, args...)
	cmd.Env = runtime.WithoutNATSSeeds(env)
	cmd.Stdin = os.Stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return nil, fmt.Errorf("provider_keys.%s: could not run secrets %s: %w", key.Env, strings.Join(args, " "), err)
		}
		detail := ""
		if text := strings.TrimSpace(stderr.String()); text != "" {
			detail = ": " + text
		}
		return nil, fmt.Errorf("provider_keys.%s: secrets %s failed (exit %d)%s", key.Env, strings.Join(args, " "), exit.ExitCode(), detail)
	}
	return stdout.Bytes(), nil
}

// pathValue is PATH's value in env, the last assignment winning, and "" when env holds none.
func pathValue(env []string) string {
	path := ""
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path = value
		}
	}
	return path
}

// writeKeyFile writes value to path, a new 0600 file, synced before it is closed. Its directory
// is a set no shim is told until every file of it is written (MaterializeProviderKeys), so the
// file needs no rename into place.
func writeKeyFile(path, value string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(value); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
