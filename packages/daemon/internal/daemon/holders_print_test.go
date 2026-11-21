package daemon

import (
	"testing"

	"github.com/sjawhar/envoy/oauth2call/oauth2calltest"
)

// The box print test of this package's holders (oauth2calltest.PrintTest): the types the read of a
// base's rules decodes GitHub's answers into, each holding the probe in every box it reaches, and
// each twin the same shape with the probe as a string there.
func TestEachHolderPrintsNoSecretByAnyPrinterAtAnyPlacement(t *testing.T) {
	type contextTwin struct {
		Name string
		Said string
	}
	type checkTwin struct {
		Context contextTwin `json:"context"`
	}
	type checksTwin struct {
		RequiredStatusChecks []checkTwin `json:"required_status_checks"`
		Strict               bool        `json:"strict_required_status_checks_policy"`
	}
	type ruleTwin struct {
		Type       string     `json:"type"`
		Parameters checksTwin `json:"parameters"`
	}
	type protectionChecksTwin struct {
		EnforcementLevel string        `json:"enforcement_level"`
		Contexts         []contextTwin `json:"contexts"`
		Checks           []checkTwin   `json:"checks"`
	}
	type protectionTwin struct {
		Enabled              bool                 `json:"enabled"`
		RequiredStatusChecks protectionChecksTwin `json:"required_status_checks"`
	}
	type branchTwin struct {
		Protection protectionTwin `json:"protection"`
	}
	said := oauth2calltest.Said(oauth2calltest.Words)
	context := checkContext{Name: "lint", Said: said}
	contextT := contextTwin{Name: "lint", Said: oauth2calltest.Words}
	check, checkT := requiredCheck{Context: context}, checkTwin{Context: contextT}
	checks := requiredStatusChecks{RequiredStatusChecks: []requiredCheck{check}, Strict: true}
	checksT := checksTwin{RequiredStatusChecks: []checkTwin{checkT}, Strict: true}
	protectionChecksValue := protectionChecks{EnforcementLevel: "non_admins", Contexts: []checkContext{context}, Checks: []requiredCheck{check}}
	protectionChecksT := protectionChecksTwin{EnforcementLevel: "non_admins", Contexts: []contextTwin{contextT}, Checks: []checkTwin{checkT}}
	protection := branchProtection{Enabled: true, RequiredStatusChecks: protectionChecksValue}
	protectionT := protectionTwin{Enabled: true, RequiredStatusChecks: protectionChecksT}
	for _, h := range []oauth2calltest.Holder{
		{Name: "daemon.checkContext", Kind: "fields", Boxed: context, Same: contextT, Exported: contextT},
		{Name: "daemon.requiredCheck", Kind: "fields", Boxed: check, Same: checkT, Exported: checkT},
		{Name: "daemon.requiredStatusChecks", Kind: "fields", Boxed: checks, Same: checksT, Exported: checksT},
		{Name: "daemon.githubBranchRule", Kind: "fields",
			Boxed: githubBranchRule{Type: "required_status_checks", Parameters: checks},
			Same:  ruleTwin{Type: "required_status_checks", Parameters: checksT}, Exported: ruleTwin{Type: "required_status_checks", Parameters: checksT}},
		{Name: "daemon.protectionChecks", Kind: "fields", Boxed: protectionChecksValue, Same: protectionChecksT, Exported: protectionChecksT},
		{Name: "daemon.branchProtection", Kind: "fields", Boxed: protection, Same: protectionT, Exported: protectionT},
		{Name: "daemon.githubBranch", Kind: "fields", Boxed: githubBranch{Protection: protection},
			Same: branchTwin{Protection: protectionT}, Exported: branchTwin{Protection: protectionT}},
	} {
		t.Run(h.Name, func(t *testing.T) { oauth2calltest.PrintTest(t, h) })
	}
}
