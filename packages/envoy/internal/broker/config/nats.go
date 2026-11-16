package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/nats-io/nkeys"
)

// loadNATSCallout reads the NATS auth service's settings (Config.NATS): none set is no service;
// any set needs BROKER_NATS_URLS and the three seed files, each holding a seed of its own kind;
// BROKER_NATS_K8S_AUDIENCE needs k8sIssuer, BROKER_K8S_OIDC_ISSUER. No refusal carries a seed.
func loadNATSCallout(getenv func(string) string, k8sIssuer string) (NATSCallout, error) {
	settings := NATSCallout{
		SeedFile:       strings.TrimSpace(getenv("BROKER_NATS_SEED_FILE")),
		IssuerSeedFile: strings.TrimSpace(getenv("BROKER_NATS_ISSUER_SEED_FILE")),
		XKeySeedFile:   strings.TrimSpace(getenv("BROKER_NATS_XKEY_SEED_FILE")),
		Services:       strings.TrimSpace(getenv("BROKER_NATS_SERVICES")),
		K8sAudience:    strings.TrimSpace(getenv("BROKER_NATS_K8S_AUDIENCE")),
		GoogleAudience: strings.TrimSpace(getenv("BROKER_NATS_GOOGLE_AUDIENCE")),
	}
	for url := range strings.SplitSeq(getenv("BROKER_NATS_URLS"), ",") {
		if url = strings.TrimSpace(url); url != "" {
			settings.URLs = append(settings.URLs, url)
		}
	}
	if settings.URLs == nil && settings.SeedFile == "" && settings.IssuerSeedFile == "" && settings.XKeySeedFile == "" &&
		settings.Services == "" && settings.K8sAudience == "" && settings.GoogleAudience == "" {
		return NATSCallout{}, nil
	}
	if settings.URLs == nil {
		return NATSCallout{}, errors.New("a BROKER_NATS_* setting is set, and BROKER_NATS_URLS is not: the NATS auth service needs the hub it answers")
	}
	for _, file := range []struct {
		setting, path string
		kind          nkeys.PrefixByte
	}{
		{"BROKER_NATS_SEED_FILE", settings.SeedFile, nkeys.PrefixByteUser},
		{"BROKER_NATS_ISSUER_SEED_FILE", settings.IssuerSeedFile, nkeys.PrefixByteAccount},
		{"BROKER_NATS_XKEY_SEED_FILE", settings.XKeySeedFile, nkeys.PrefixByteCurve},
	} {
		if err := checkSeedFile(file.setting, file.path, file.kind); err != nil {
			return NATSCallout{}, err
		}
	}
	if settings.K8sAudience != "" && k8sIssuer == "" {
		return NATSCallout{}, errors.New("BROKER_NATS_K8S_AUDIENCE needs BROKER_K8S_OIDC_ISSUER, whose projected tokens a service presents to the NATS auth service")
	}
	return settings, nil
}

// checkSeedFile refuses, naming setting, a path that is unset or unreadable, or whose trimmed
// contents are not an nkey seed of kind.
func checkSeedFile(setting, path string, kind nkeys.PrefixByte) error {
	if path == "" {
		return fmt.Errorf("the NATS auth service needs %s", setting)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s names %s, which could not be read: %w", setting, path, err)
	}
	prefix, _, err := nkeys.DecodeSeed([]byte(strings.TrimSpace(string(raw))))
	if err != nil {
		return fmt.Errorf("%s names %s, which holds no nkey seed", setting, path)
	}
	if prefix != kind {
		return fmt.Errorf("%s names %s, which holds a %s seed where it needs a %s seed", setting, path, prefix, kind)
	}
	return nil
}
