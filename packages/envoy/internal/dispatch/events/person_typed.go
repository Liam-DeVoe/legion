package events

import "context"

// personTypedKey marks the context of a write a person typed in a session: the write carried
// Dispatch-Person-Typed: 1 (Decision 26 of INFRA-3231's plan), which the server's routes read.
type personTypedKey struct{}

// WithPersonTyped is ctx marked as a person's typed write's: every text event
// Broker.QueueAppendWithLogins queues on a batch built under it carries person_typed: true
// (model.PersonTypedPayload), the mark read through store.WriteBatch.Context.
func WithPersonTyped(ctx context.Context) context.Context {
	return context.WithValue(ctx, personTypedKey{}, true)
}

// PersonTyped reports whether ctx is a person's typed write's (WithPersonTyped).
func PersonTyped(ctx context.Context) bool {
	typed, _ := ctx.Value(personTypedKey{}).(bool)
	return typed
}
