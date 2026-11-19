package daemon

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/sjawhar/legion/daemon/internal/leakguard"
)

// privateNameRulesNone is the boot's line for a deployment that names no private-name rule file
// (config.Config.PrivateNameRulesFile): the two places one is named, and what running without one
// means. It is a plain Info line, since a deployment with no private names of its own is one the
// daemon runs as designed; a deployment that meant to name a file reads, at the boot that lost it,
// that no role refuses a name of its own. Boot logs it before anything that can stop the boot
// (prepare), so a boot that then fails has said it all the same.
const privateNameRulesNone = "daemon: the deployment names no private-name rule file (private_name_rules_file in legion.yaml, or " +
	leakguard.RulesFileVar + " in the daemon's environment): every role's guard runs no private-name rule"

// privateNameRulesBound is the most of a private-name rule file boot reads: the mebibyte
// leakguard.LoadRules holds a rule file's content to. Boot reads one byte past it, so that a longer
// file is refused for its length here, whatever bound LoadRules holds, and never cut at the bound
// and compiled short.
const privateNameRulesBound = 1 << 20

// readPrivateNameRulesFile is the private-name rule file at path, its bytes verbatim: the launch
// secret leakguard.RulesSecret (launchSecrets), which readBoot compiles (compilePrivateNameRules).
// namedBy is the setting that named the file as the operator wrote it, `private_name_rules_file`
// or LEGION_PRIVATE_NAME_RULES_FILE (config.Config.PrivateNameRulesNamedBy), and a file that
// cannot be opened or read, or that is longer than privateNameRulesBound, is refused naming it and
// the file. Nothing is trimmed, as a token's file is: the rules a role runs are the bytes the
// operator wrote.
func readPrivateNameRulesFile(namedBy, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%s names %s, which could not be read: %w", namedBy, path, err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, privateNameRulesBound+1))
	if err != nil {
		return "", fmt.Errorf("%s names %s, which could not be read: %w", namedBy, path, err)
	}
	if len(content) > privateNameRulesBound {
		return "", fmt.Errorf("%s names %s, which is longer than %d bytes, the most a private-name rule file may hold", namedBy, path, privateNameRulesBound)
	}
	return string(content), nil
}

// compilePrivateNameRules compiles content, the rule file at path as boot read it
// (readPrivateNameRulesFile), with the guard's own loader (leakguard.LoadRules), refused naming
// namedBy and the file as a file that cannot be read is. A file the guard cannot run would refuse
// every write of every role and every branch create of the daemon; it refuses the boot instead,
// and `legion config check` with it, where the operator reads why once.
func compilePrivateNameRules(namedBy, path, content string) (*leakguard.RuleSet, error) {
	rules, err := leakguard.LoadRules([]byte(content))
	if err != nil {
		return nil, fmt.Errorf("%s names %s, which is no private-name rule file the guard can run: %w", namedBy, path, err)
	}
	return rules, nil
}

// logPrivateNameRules is the boot's line for a deployment that names a rule file, logged once boot
// has compiled it (prepare): the file, the sha256 of the bytes every launch carries and the number
// of its rules, so that an operator reads which rules every role runs and tells one version of the
// file from another.
func logPrivateNameRules(log *slog.Logger, path string, rules *leakguard.RuleSet) {
	log.Info(fmt.Sprintf("daemon: the deployment's private-name rules are %s (sha256 %s, %d rules), handed to every launch",
		path, rules.SHA256(), rules.Count()))
}
