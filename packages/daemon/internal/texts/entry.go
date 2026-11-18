package texts

import (
	"slices"

	"github.com/sjawhar/legion/daemon/internal/claim"
)

// Kind names a kind of text on the one list.
type Kind string

// Form is the form of a kind's text.
type Form string

const (
	// Task is a text that the daemon sends a session as a prompt. The parts that a start puts
	// before a task are tasks too.
	Task Form = "task"
	// Notice is a notice to the architect that owns an issue.
	Notice Form = "notice"
	// ControllerNotice is a notice to the controller.
	ControllerNotice Form = "controller-notice"
	// RoleMessage is a message that the daemon publishes to a role.
	RoleMessage Form = "role-message"
)

// Entry is one kind on the one list.
type Entry struct {
	Kind Kind
	Form Form
	// Record is the zero value of the kind's record type, which declares the kind's fields.
	Record any
	// Urgency is the kind's urgency rule for a reader and the variant of the text. It is nil
	// for a task, which always starts a turn. A nil rule on a notice or a role message gives
	// Med.
	Urgency func(reader claim.Role, v Variant) Urgency
}

// byKind is the position of each kind on the list.
var byKind = func() map[Kind]int {
	index := make(map[Kind]int, len(list))
	for i, entry := range list {
		if _, seen := index[entry.Kind]; !seen {
			index[entry.Kind] = i
		}
	}
	return index
}()

// Lookup returns the entry of kind, and false where kind is not on the list.
func Lookup(kind Kind) (Entry, bool) {
	i, ok := byKind[kind]
	if !ok {
		return Entry{}, false
	}
	return list[i], true
}

// Entries returns every entry of the list, in the order of the list. The slice is a copy.
func Entries() []Entry {
	return slices.Clone(list)
}

// IsNotice says whether kind is on the list as a notice to an architect or to the controller.
func IsNotice(kind Kind) bool {
	entry, ok := Lookup(kind)
	return ok && (entry.Form == Notice || entry.Form == ControllerNotice)
}

// IsControllerNotice says whether kind is on the list as a notice to the controller.
func IsControllerNotice(kind Kind) bool {
	return formOf(kind) == ControllerNotice
}

// IsRoleMessage says whether kind is on the list as a role message.
func IsRoleMessage(kind Kind) bool {
	return formOf(kind) == RoleMessage
}

// IsTask says whether kind is on the list as a task.
func IsTask(kind Kind) bool {
	return formOf(kind) == Task
}

// formOf is the form of kind, or empty where kind is not on the list.
func formOf(kind Kind) Form {
	entry, _ := Lookup(kind)
	return entry.Form
}
