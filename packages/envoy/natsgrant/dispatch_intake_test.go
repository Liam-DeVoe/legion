package natsgrant_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/natsgrant"
)

// allows reports whether p's publish allow list admits subject, as nats-server matches a literal
// subject against an entry: token for token, where * is any one token and a last > is one or more.
func allows(p natsgrant.Permissions, subject string) bool {
	tokens := strings.Split(subject, ".")
	return slices.ContainsFunc(p.Publish, func(entry string) bool {
		pattern := strings.Split(entry, ".")
		for i, token := range pattern {
			switch {
			case token == ">":
				return i < len(tokens)
			case i >= len(tokens):
				return false
			case token != "*" && token != tokens[i]:
				return false
			}
		}
		return len(pattern) == len(tokens)
	})
}

const (
	exampleMachine = "example-dispatch"
	ownGen         = "GenNowA1b2C3d4E5f6G7h8"
	oldGen         = "GenOldA1b2C3d4E5f6G7h8"
	otherGen       = "GenElseA1b2C3d4E5f6G7h"
)

func consumerCall(call, name string) string {
	return "$JS.API.CONSUMER." + call + "." + bus.Stream + "." + name
}

func consumerCreate(name string) string {
	return consumerCall("CREATE", name) + ".notifications.github.example"
}

func consumerAck(name string) string {
	return "$JS.ACK." + bus.Stream + "." + name + ".1.7.7.1700000000000000000.0"
}

// A Dispatch connection's name is read as the intake it asks for only in the three shapes Dispatch
// names its own connections, for one of Dispatch's two durables and generation tokens of letters
// and digits alone. Any other name, Dispatch's fixed-mode intakes' and its publisher's included,
// asks for nothing, so a name can never widen the grant past Dispatch's own two durables.
func TestADispatchConnectionNameIsReadAsTheIntakeItNames(t *testing.T) {
	for name, want := range map[string]natsgrant.DispatchIntake{
		natsgrant.DispatchIntakeConnection(bus.DispatchDeliveryDurable, ownGen, ""):      {Logical: bus.DispatchDeliveryDurable, Generation: ownGen},
		natsgrant.DispatchIntakeConnection(bus.DispatchJobDurable, ownGen, oldGen):       {Logical: bus.DispatchJobDurable, Generation: ownGen, Old: oldGen},
		natsgrant.DispatchCleanupConnection(bus.DispatchDeliveryDurable, oldGen):         {Logical: bus.DispatchDeliveryDurable, Generation: oldGen, Cleanup: true},
		"dispatch-intake/delivery-events/" + ownGen:                                      {Logical: bus.DispatchDeliveryDurable, Generation: ownGen},
		"dispatch-intake/delivery-jobs/" + ownGen + "/" + oldGen:                         {Logical: bus.DispatchJobDurable, Generation: ownGen, Old: oldGen},
		"dispatch-cleanup/delivery-jobs/" + oldGen:                                       {Logical: bus.DispatchJobDurable, Generation: oldGen, Cleanup: true},
		"dispatch-intake":                                                                {},
		"dispatch-job-intake":                                                            {},
		"dispatch":                                                                       {},
		"":                                                                               {},
		"dispatch-intake/delivery-events":                                                {},
		"dispatch-intake/delivery-events/":                                               {},
		"dispatch-intake/delivery-events/" + ownGen + "/":                                {},
		"dispatch-intake/delivery-events/" + ownGen + "/" + oldGen + "/" + otherGen:      {},
		"dispatch-cleanup/delivery-events/" + ownGen + "/" + oldGen:                      {},
		"dispatch-intake/listener-m2/" + ownGen:                                          {},
		"dispatch-intake/legion-go-p-github/" + ownGen:                                   {},
		"dispatch-intake/s-x/" + ownGen:                                                  {},
		"dispatch-intake/delivery-events/*":                                              {},
		"dispatch-intake/delivery-events/>":                                              {},
		"dispatch-intake/delivery-events/a.b":                                            {},
		"dispatch-intake/delivery-events/a b":                                            {},
		"dispatch-intake/delivery-events/a~b":                                            {},
		"dispatch-intake/delivery-events/" + ownGen + "/" + ownGen:                       {},
		"dispatch-intake/delivery-events/" + strings.Repeat("a", 65):                     {},
		"other-intake/delivery-events/" + ownGen:                                         {},
		"dispatch-intake/delivery-events/" + ownGen + " dispatch-cleanup/delivery-jobs/": {},
	} {
		require.Equal(t, want, natsgrant.ParseDispatchConnection(name), "connection name %q", name)
	}
}

// The grant rows of an intake connection named for one generation and the one it replaces: it
// creates, reads and acknowledges its own generation and reads and deletes the old one, and
// nothing else of any consumer the fixed grant does not already reach. Its own listener durable is
// still its own, and the fixed names stay until their rights are revoked separately.
func TestAnIntakeConnectionIsGrantedItsOwnGenerationsExactly(t *testing.T) {
	logical := bus.DispatchDeliveryDurable
	own, old, other := logical+"~"+ownGen, logical+"~"+oldGen, logical+"~"+otherGen
	grant := natsgrant.Dispatch(exampleMachine, natsgrant.ParseDispatchConnection(natsgrant.DispatchIntakeConnection(logical, ownGen, oldGen)))
	for subject, want := range map[string]bool{
		consumerCreate(own):                       true,
		consumerCall("INFO", own):                 true,
		consumerAck(own):                          true,
		consumerCall("INFO", old):                 true,
		consumerCall("DELETE", old):               true,
		consumerCall("DELETE", own):               false,
		consumerCreate(old):                       false,
		consumerAck(old):                          false,
		consumerCreate(other):                     false,
		consumerCall("INFO", other):               false,
		consumerCall("DELETE", other):             false,
		consumerAck(other):                        false,
		consumerCreate("s-x"):                     false,
		consumerCall("INFO", "s-x"):               false,
		consumerCall("DELETE", "s-x"):             false,
		consumerCreate("listener-m2"):             false,
		consumerCall("INFO", "listener-m2"):       false,
		consumerCreate("legion-go-p-github"):      false,
		consumerCall("INFO", "legion-go-p-github"): false,
		consumerAck("legion-go-p-github"):         false,
		consumerCall("INFO", bus.ListenerDurable(exampleMachine)):                       true,
		consumerCall("CREATE", bus.ListenerDurable(exampleMachine)) + ".notifications.>": false,
		consumerCall("CREATE", bus.ListenerDurable(exampleMachine)):                     true,
		consumerCreate(bus.DispatchJobDurable + "~" + ownGen):                           false,
		consumerCreate(logical):                   true,
		consumerCall("DELETE", logical):           true,
	} {
		require.Equal(t, want, allows(grant, subject), "%s for an intake of %s over %s", subject, ownGen, oldGen)
	}
	// Control: the same rows under Dispatch's fixed grant, with no intake, refuse every fresh name.
	fixed := natsgrant.Dispatch(exampleMachine, natsgrant.DispatchIntake{})
	for _, subject := range []string{consumerCreate(own), consumerCall("INFO", own), consumerAck(own), consumerCall("DELETE", old)} {
		require.False(t, allows(fixed, subject), "the fixed grant admits %s", subject)
	}
	require.Equal(t, fixed.Publish, grant.Publish[:len(fixed.Publish)], "an intake's grant is the fixed grant followed by its own entries")
	require.Equal(t, fixed.Subscribe, grant.Subscribe)
}

// A cleanup connection reads and deletes its one generation and nothing more of the fresh names:
// it creates nothing and acknowledges nothing.
func TestACleanupConnectionReadsAndDeletesItsOneGeneration(t *testing.T) {
	logical := bus.DispatchJobDurable
	gone, other := logical+"~"+oldGen, logical+"~"+otherGen
	grant := natsgrant.Dispatch(exampleMachine, natsgrant.ParseDispatchConnection(natsgrant.DispatchCleanupConnection(logical, oldGen)))
	for subject, want := range map[string]bool{
		consumerCall("INFO", gone):    true,
		consumerCall("DELETE", gone):  true,
		consumerCreate(gone):          false,
		consumerAck(gone):             false,
		consumerCall("INFO", other):   false,
		consumerCall("DELETE", other): false,
		consumerCall("DELETE", bus.DispatchDeliveryDurable+"~"+oldGen): false,
	} {
		require.Equal(t, want, allows(grant, subject), "%s for a cleanup of %s", subject, oldGen)
	}
}

// The fixed grant, with no intake, is the set the hubs render today, entry for entry: an intake
// adds entries after it and changes none of it.
func TestTheFixedDispatchGrantIsTheOneWithNoIntake(t *testing.T) {
	var base mergeBase
	readJSON(t, "testdata/merge-base-grants.json", &base)
	requireSet(t, base.Dispatch[exampleMachine], natsgrant.Dispatch(exampleMachine, natsgrant.DispatchIntake{}), "Dispatch with no intake")
	requireSet(t, base.Dispatch[exampleMachine], natsgrant.Dispatch(exampleMachine, natsgrant.ParseDispatchConnection("dispatch-intake")), "Dispatch for the fixed intake's name")
}
