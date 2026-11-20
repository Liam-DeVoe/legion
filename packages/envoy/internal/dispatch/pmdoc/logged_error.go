package pmdoc

import (
	"errors"
	"log/slog"
)

// refusalClasses are the particular refusals LoggedError names a refusal of a document by, the
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
// names in wrappers - is logged as its class alone: the message of the first sentinel it is, this
// package's particular refusals first (refusalClasses), then wrappers in order, then ErrSchema. The
// refusal's own message is left out, and so is whatever a caller wrapped it in, because it quotes
// the document - a block's text, an attribute's value - and a document can hold a person's address
// and other private text, while a log line is read by whoever reads the server's logs. Any other
// error is logged as it is.
func LoggedError(err error, wrappers ...error) slog.Value {
	if class := refusalClass(err, wrappers); class != nil {
		return slog.StringValue(class.Error())
	}
	return slog.AnyValue(err)
}

// refusalClass is the sentinel LoggedError names err by, or nil where err refuses no document.
func refusalClass(err error, wrappers []error) error {
	for _, class := range refusalClasses {
		if errors.Is(err, class) {
			return class
		}
	}
	for _, class := range wrappers {
		if errors.Is(err, class) {
			return class
		}
	}
	if errors.Is(err, ErrSchema) {
		return ErrSchema
	}
	return nil
}
