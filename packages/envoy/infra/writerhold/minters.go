package writerhold

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The accepted list (§3a, "Who can mint the operator's token"): the ConfigMap
// envoy-operator-accepted-minters in namespace envoy-operator, which the deployment's
// cluster-grants/ renders (t-11, part 3), under one data key, AcceptedMintersKey, whose value is a
// JSON array of AcceptedMinter, [] until Sami answers plan F's items 8(d) and 8(f).
const (
	AcceptedMintersNamespace = "envoy-operator"
	AcceptedMintersConfigMap = "envoy-operator-accepted-minters"
	AcceptedMintersKey       = "accepted.json"
)

// minterReportAge bounds the report's age: it says who could mint the operator's token or write the
// hold's tables when it ran, so the preflight takes one run less than an hour before (§3a).
const minterReportAge = time.Hour

// MinterFinding is one holder the minter script reports (#4679), as it writes it: a principal,
// Right, the script's sentence naming the permissions and where they come from, Role, the role it
// holds on Resource, that role's Permissions that mint the operator's token or write the hold's
// tables, and GrantedAt, the resource the binding is made on (a project, a cluster, a namespace).
// A person never appears in the report (#4679), so no finding names one, and no accepted entry
// does either.
type MinterFinding struct {
	Principal   string   `json:"principal"`
	Right       string   `json:"right"`
	Role        string   `json:"role"`
	Resource    string   `json:"resource"`
	Permissions []string `json:"permissions"`
	GrantedAt   string   `json:"granted_at"`
}

// MinterUnknown is one step the minter script could not read, and why.
type MinterUnknown struct {
	Step   string `json:"step"`
	Reason string `json:"reason"`
}

// minterReport is the report operator-token-minters.sh --report writes (plan E line 2148,
// its findings structured by #4679; its own test harness writes
// scripts/testdata/operator-token-minters-report.json): {"run_at": "<RFC 3339>", "findings":
// [{"principal", "right", "role", "resource", "permissions", "granted_at"}], "unknown": [{"step",
// "reason"}], "allowances": [{"flag", "value"}]}. The lists are pointers so that a report missing
// one is told from a report whose list is empty.
type minterReport struct {
	RunAt      *time.Time         `json:"run_at"`
	Findings   *[]MinterFinding   `json:"findings"`
	Unknown    *[]MinterUnknown   `json:"unknown"`
	Allowances *[]MinterAllowance `json:"allowances"`
}

// MinterAllowance is one allowance the minter run was given, as the script writes it: the flag
// (--service-agent, --rbac-only-member or --fleet) and its value. Each lets a principal or a
// reading pass, so the hold prints each one and `hold set` records it; none refuses the hold, since
// an allowance is the operator's deliberate choice.
type MinterAllowance struct {
	Flag  string `json:"flag"`
	Value string `json:"value"`
}

// String is the allowance as the run was given it: "<flag> <value>".
func (a MinterAllowance) String() string { return a.Flag + " " + a.Value }

// AcceptedMinter is one entry of the accepted list, as t-11's cluster-grants/ renders it: a
// principal, its Right (the role a finding names for it), the clause of plan
// F's go-live items it falls under (8(d) for a minter, 8(f) for a hold writer), and the link to
// Sami's answer that accepts it. Every field is required. No entry names a person, because the
// report never does.
type AcceptedMinter struct {
	Principal string `json:"principal"`
	Right     string `json:"right"`
	Clause    string `json:"clause"`
	Answer    string `json:"answer"`
}

// PreflightMinters is the preflight's minter check (§3a, "How the preflight reads it"): it reads
// reportPath, the --minters-report of `hold prepare` or `hold set`, and the accepted list through
// client (t-11's get on that one ConfigMap). It stops the hold unless the report ran under one hour
// before now, its unknown list is empty, and every finding, the hold writers included, has an
// accepted entry whose principal is its principal and whose right is its role; a finding with none
// is named by its principal, role and resource. A person never appears in the report, so no
// accepted entry names one. No report is a refusal naming it; a report or a list that does not read
// whole is UNKNOWN, and so is a report whose unknown list names a step. It returns the allowances
// the run was given, which its caller prints and records, and which refuse nothing.
func PreflightMinters(ctx context.Context, client kubernetes.Interface, reportPath string, now func() time.Time) ([]MinterAllowance, error) {
	if reportPath == "" {
		return nil, refusef(StepPreflight, "no minter report: the hold takes --minters-report <file>, the report operator-token-minters.sh --report writes")
	}
	runAt, findings, unknown, allowances, err := readMinterReport(reportPath)
	if err != nil {
		return nil, err
	}
	at := now()
	if age := at.Sub(runAt); age < 0 || age >= minterReportAge {
		return nil, refusef(StepPreflight, "the minter report %s ran at %s, %s before now (%s); the hold takes a report under %s old", reportPath, runAt.Format(time.RFC3339), age.Round(time.Second), at.UTC().Format(time.RFC3339), minterReportAge)
	}
	if len(unknown) > 0 {
		steps := make([]string, len(unknown))
		for i, u := range unknown {
			steps[i] = fmt.Sprintf("%s (%s)", u.Step, u.Reason)
		}
		return nil, unknownf(StepPreflight, nil, "the minter report %s could not read: %s", reportPath, strings.Join(steps, "; "))
	}
	accepted, err := readAcceptedMinters(ctx, client)
	if err != nil {
		return nil, err
	}
	// ok holds each accepted principal with its right, the role a finding must name.
	ok := map[[2]string]bool{}
	for _, a := range accepted {
		ok[[2]string{a.Principal, a.Right}] = true
	}
	var refused []string
	for _, f := range findings {
		if !ok[[2]string{f.Principal, f.Role}] {
			refused = append(refused, f.Principal+" (role "+f.Role+" on "+f.Resource+")")
		}
	}
	if len(refused) > 0 {
		return nil, refusef(StepPreflight, "the minter report %s names holders that %s/%s does not accept: %s", reportPath, AcceptedMintersNamespace, AcceptedMintersConfigMap, strings.Join(refused, ", "))
	}
	return allowances, nil
}

// printAllowances prints each allowance the minter run was given, for the operator.
func printAllowances(stdout io.Writer, allowances []MinterAllowance) {
	for _, a := range allowances {
		fmt.Fprintf(stdout, "the minter report's run was given %s\n", a)
	}
}

// allowanceOutputs are the allowances as `hold set`'s done entry records them:
// minters_allowance.<i>, "<flag> <value>".
func allowanceOutputs(allowances []MinterAllowance) map[string]string {
	out := map[string]string{}
	for i, a := range allowances {
		out[fmt.Sprintf("minters_allowance.%d", i)] = a.String()
	}
	return out
}

// readMinterReport reads the report at path, strictly: one JSON object with run_at, findings,
// unknown and allowances, no other field, each finding the fields the script writes (principal,
// right, role, resource, permissions and granted_at) and no other, and each allowance its flag and
// value. A finding that leaves out or empties any of the structured five (a permissions list with
// no entry, or an empty one) is UNKNOWN, naming its index and each field it lacks, so a report in
// the sentence form alone, a principal and a right, is UNKNOWN; so is an allowance that lacks its
// flag or its value, and anything else that does not read, naming the file.
func readMinterReport(path string) (time.Time, []MinterFinding, []MinterUnknown, []MinterAllowance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, nil, nil, nil, unknownf(StepPreflight, err, "read the minter report %s", path)
	}
	var report minterReport
	if err := decodeStrict(data, &report); err != nil {
		return time.Time{}, nil, nil, nil, unknownf(StepPreflight, err, "decode the minter report %s", path)
	}
	switch {
	case report.RunAt == nil:
		return time.Time{}, nil, nil, nil, unknownf(StepPreflight, nil, "the minter report %s has no run_at", path)
	case report.Findings == nil || report.Unknown == nil || report.Allowances == nil:
		return time.Time{}, nil, nil, nil, unknownf(StepPreflight, nil, "the minter report %s lacks its findings, its unknown list or its allowances", path)
	}
	for i, a := range *report.Allowances {
		var lacks []string
		if a.Flag == "" {
			lacks = append(lacks, "flag")
		}
		if a.Value == "" {
			lacks = append(lacks, "value")
		}
		if len(lacks) > 0 {
			return time.Time{}, nil, nil, nil, unknownf(StepPreflight, nil, "the minter report %s's allowances[%d] lacks its %s", path, i, joinLacks(lacks))
		}
	}
	findings := make([]MinterFinding, len(*report.Findings))
	for i, f := range *report.Findings {
		var lacks []string
		for _, field := range []struct {
			name  string
			empty bool
		}{
			{"principal", f.Principal == ""}, {"role", f.Role == ""}, {"resource", f.Resource == ""},
			{"permissions", len(f.Permissions) == 0 || slices.Contains(f.Permissions, "")},
			{"granted_at", f.GrantedAt == ""},
		} {
			if field.empty {
				lacks = append(lacks, field.name)
			}
		}
		if len(lacks) > 0 {
			return time.Time{}, nil, nil, nil, unknownf(StepPreflight, nil, "the minter report %s's findings[%d] lacks its %s", path, i, joinLacks(lacks))
		}
		findings[i] = MinterFinding(f)
	}
	return *report.RunAt, findings, *report.Unknown, *report.Allowances, nil
}

// readAcceptedMinters reads the accepted list: the ConfigMap's AcceptedMintersKey, a JSON array of
// entries each with a principal, a right, a clause and an answer. A ConfigMap that cannot be read, a
// missing key, or an entry that leaves out or empties a field is UNKNOWN, naming the entry's place
// in the list and each field it lacks (entryLacks).
func readAcceptedMinters(ctx context.Context, client kubernetes.Interface) ([]AcceptedMinter, error) {
	where := AcceptedMintersNamespace + "/" + AcceptedMintersConfigMap
	cm, err := client.CoreV1().ConfigMaps(AcceptedMintersNamespace).Get(ctx, AcceptedMintersConfigMap, metav1.GetOptions{})
	if err != nil {
		return nil, unknownf(StepPreflight, err, "read ConfigMap %s", where)
	}
	text, ok := cm.Data[AcceptedMintersKey]
	if !ok {
		return nil, unknownf(StepPreflight, nil, "ConfigMap %s holds no %s", where, AcceptedMintersKey)
	}
	var accepted []AcceptedMinter
	if err := decodeStrict([]byte(text), &accepted); err != nil {
		return nil, unknownf(StepPreflight, err, "decode ConfigMap %s's %s", where, AcceptedMintersKey)
	}
	if accepted == nil {
		return nil, unknownf(StepPreflight, nil, "ConfigMap %s's %s is not a list", where, AcceptedMintersKey)
	}
	for i, a := range accepted {
		if lacks := entryLacks(a); lacks != "" {
			return nil, unknownf(StepPreflight, nil, "ConfigMap %s's %s entry %d lacks its %s", where, AcceptedMintersKey, i, lacks)
		}
	}
	return accepted, nil
}

// entryLacks names the fields of a that are empty, in the entry's order ("clause", "principal,
// right, clause and answer"), or "" for a whole entry. A field left out of the JSON decodes empty,
// so it is named the same way.
func entryLacks(a AcceptedMinter) string {
	var lacks []string
	for _, f := range []struct{ name, value string }{
		{"principal", a.Principal}, {"right", a.Right}, {"clause", a.Clause}, {"answer", a.Answer},
	} {
		if f.value == "" {
			lacks = append(lacks, f.name)
		}
	}
	return joinLacks(lacks)
}

// joinLacks is fields as a finding names what an entry lacks: "clause", or "principal, right,
// clause and answer"; "" for none.
func joinLacks(fields []string) string {
	if len(fields) < 2 {
		return strings.Join(fields, "")
	}
	return strings.Join(fields[:len(fields)-1], ", ") + " and " + fields[len(fields)-1]
}

// decodeStrict decodes data, one JSON value, into into, refusing an unknown field and anything
// after the value.
func decodeStrict(data []byte, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("more than one JSON value")
	}
	return nil
}
