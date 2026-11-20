package pmdoc

import (
	"errors"
	"log/slog"
)

// refusalClasses are the particular refusals RefusalClass names a refusal of a document by, the
// most particular first. Each is a sentinel whose message is this package's own words, never a
// document's.
var refusalClasses = [...]error{
	ErrTablePadding,
	ErrBlockHTML,
	ErrMarkNotAllowed,
	ErrUnknownElement,
	ErrBlockPointer,
	ErrMisreadRendering,
	ErrRenderingRefused,
}

// LoggedError is err as a log line carries it. A refusal of a document - this package's (ErrSchema,
// ErrUnknownElement, ErrBlockPointer) or one a caller wraps in a sentinel of its own, which it
// names in wrappers - is logged as its class alone (RefusalClass). The refusal's own message is
// left out, and so is whatever a caller wrapped it in, because it quotes the document - a block's
// text, an attribute's value - and a document can hold a person's address and other private text,
// while a log line is read by whoever reads the server's logs. Any other error is logged as it is.
func LoggedError(err error, wrappers ...error) slog.Value {
	if class := RefusalClass(err, wrappers...); class != "" {
		return slog.StringValue(class)
	}
	return slog.AnyValue(err)
}

// RefusalClass is the class a refusal of a document is logged by: the message of the first
// sentinel err is, this package's particular refusals first (refusalClasses), then wrappers in
// order, then ErrSchema, and "" where err refuses no document. It answers a sentinel's own words
// and never err's, so a line that only ever logs a refusal logs it by this, and nothing of the
// refusal's message - the document's text it quotes - can reach that line whatever err holds.
func RefusalClass(err error, wrappers ...error) string {
	for _, class := range refusalClasses {
		if errors.Is(err, class) {
			return class.Error()
		}
	}
	for _, class := range wrappers {
		if errors.Is(err, class) {
			return class.Error()
		}
	}
	if errors.Is(err, ErrSchema) {
		return ErrSchema.Error()
	}
	return ""
}
