package natsurl

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"syscall"

	"github.com/nats-io/nats.go"
)

// ConnectionUnprinted is ConnectionClass's words for an error it has no class for.
const ConnectionUnprinted = "the connection failed (its error is not printed, since it can name the server by a piece of a credential)"

// The words ConnectionClass names a lookup, a time out and a TLS failure by: the envoy module's
// internal/redact names the same failures by the same words (ClassNameResolution, ClassTimedOut,
// ClassTLS), so one failure has one set of words wherever it prints.
const (
	classNameResolution = "name resolution failed"
	classTimedOut       = "timed out"
	classTLS            = "TLS failed"
)

// ConnectionClass is err, an error of a connection to a NATS server, as a NATS client prints it in a
// log line or an error: fixed words naming its class, never its text. The NATS client's and the
// dialers' errors name the server they failed at by the host its URL spells - a lookup's
// (net.DNSError), a dial's through a SOCKS5 proxy, which is handed that host (net.OpError's Addr), a
// TLS certificate's name check (x509.HostnameError), a port net cannot read (net.AddrError,
// `dial tcp: address <port>: invalid port`) - and where a caller split its list on commas, a
// credential holding a comma leaves its head a URL of its own, so that host can be a piece of a
// credential: the tail rule (Check) lets such a list through when the part after the comma names a
// NATS scheme, since a later URL that names one is one a person writes.
//
// The classes are the system's own words for a failed call (a syscall.Errno: "connection refused",
// "connection reset by peer"), EOF, a TLS alert's own words ("remote error: tls: certificate
// required"), the words a lookup, a time out and a TLS failure are named by, the reply a SOCKS5
// proxy answered (socksClass), and the NATS client's own words: the text of an error it defines
// (natsErrors), and of a server's -ERR it relays, which it writes as "nats: " and the server's
// fixed words, an error that wraps nothing. The NATS client's TLS error keeps its words before the
// class of what failed under it. Any other error is ConnectionUnprinted, and nil is "unknown".
func ConnectionClass(err error) string {
	if err == nil {
		return "unknown"
	}
	text := failureClass(err)
	if errors.Is(err, nats.ErrTLS) {
		if text == "" {
			return nats.ErrTLS.Error()
		}
		return nats.ErrTLS.Error() + ": " + text
	}
	if text != "" {
		return text
	}
	// A server's -ERR, as the NATS client relays it (processErr's errors.New("nats: " + …), and
	// sendConnect's natsProtoErr), or an error of its own it defines: an error that wraps nothing,
	// in the client's words. It is printed as it is, so a server's refusal reads as the server wrote
	// it ("nats: Authorization Violation"). So is a server's permissions violation, which the client
	// writes as its own error and the server's words after it, the subject among them.
	if relayed(err) {
		return err.Error()
	}
	for _, known := range natsErrors {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return ConnectionUnprinted
}

// relayed reports whether err is the NATS client's own words: an error that wraps nothing and
// whose text starts "nats: ", or the client's permissions violation and the server's -ERR after
// it (processErr's fmt.Errorf("%w: %s", ErrPermissionViolation, …)), which wraps that error alone.
func relayed(err error) bool {
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		return false
	case interface{ Unwrap() error }:
		return e.Unwrap() == nats.ErrPermissionViolation && strings.HasPrefix(err.Error(), nats.ErrPermissionViolation.Error()+": ")
	}
	return strings.HasPrefix(err.Error(), "nats: ")
}

// ServerPosition names the server a connection reached, connected (its ConnectedUrl, which the
// NATS client renders from the server's URL whole, its userinfo among it), by its position in urls,
// the servers the connection was configured with: "2 of 3". It prints no part of a URL: a URL's
// userinfo is a credential, and its host can be a piece of one a comma split (ConnectionClass). It
// compares the host and the port alone, and a URL that names no port by its host, since the client
// adds a default port to one; a blank entry, which the client drops, is no server; and a server
// urls does not hold is one the cluster advertised, which the client adds to the servers it dials.
func ServerPosition(urls []string, connected string) string {
	if reached, err := url.Parse(connected); err == nil && reached.Host != "" {
		for index, raw := range urls {
			if sameServer(strings.TrimSpace(raw), reached) {
				return fmt.Sprintf("%d of %d", index+1, len(urls))
			}
		}
	}
	return fmt.Sprintf("one the cluster advertised, beside the %d configured", len(urls))
}

// sameServer reports whether raw, a configured server URL, names the host and port of reached,
// raw read as the NATS client reads it, nats:// added where it names no scheme.
func sameServer(raw string, reached *url.URL) bool {
	if raw == "" {
		return false
	}
	if !strings.Contains(raw, "://") {
		raw = "nats://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Hostname(), reached.Hostname()) {
		return false
	}
	return parsed.Port() == "" || parsed.Port() == reached.Port()
}

// ConnectionError is err printed as its class (ConnectionClass), with err kept beneath it for
// errors.Is and errors.As; nil is nil.
func ConnectionError(err error) error {
	if err == nil {
		return nil
	}
	return connectionError{err}
}

type connectionError struct{ err error }

func (e connectionError) Error() string { return ConnectionClass(e.err) }
func (e connectionError) Unwrap() error { return e.err }

// failureClass is the class of the transport failure err holds, in ConnectionClass's words, or ""
// when it holds none.
func failureClass(err error) string {
	var dns *net.DNSError
	var address *net.AddrError
	var hostname x509.HostnameError
	var authority x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var verification *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	var timeout interface{ Timeout() bool }
	var errno syscall.Errno
	var op *net.OpError
	switch {
	case errors.As(err, &dns):
		return classNameResolution
	case errors.As(err, &address):
		return "the server's address is no host and port the system can dial"
	case errors.As(err, &hostname), errors.As(err, &authority), errors.As(err, &invalid),
		errors.As(err, &verification), errors.As(err, &record):
		return classTLS
	case errors.As(err, &op) && (op.Op == "remote error" || op.Op == "local error") && op.Err != nil &&
		strings.HasPrefix(op.Err.Error(), "tls: "):
		// crypto/tls returns an alert, the far side's or this side's, as a *net.OpError whose Err
		// is the alert, whose text is its fixed description.
		return op.Op + ": " + op.Err.Error()
	case errors.Is(err, context.Canceled):
		return context.Canceled.Error()
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded),
		errors.As(err, &timeout) && timeout.Timeout():
		return classTimedOut
	case errors.As(err, &errno):
		return errno.Error()
	case errors.Is(err, io.EOF):
		return io.EOF.Error()
	case errors.Is(err, io.ErrUnexpectedEOF):
		return io.ErrUnexpectedEOF.Error()
	case errors.As(err, &op) && strings.HasPrefix(op.Op, "socks"):
		return socksClass(op.Err)
	}
	return ""
}

// socksReplies are the replies a SOCKS5 proxy refuses a CONNECT with (RFC 1928 section 6), as
// x/net's client names them in its error, "unknown error <reply>" (internal/socks, Reply.String).
var socksReplies = []string{
	"general SOCKS server failure", "connection not allowed by ruleset", "network unreachable", "host unreachable",
	"connection refused", "TTL expired", "command not supported", "address type not supported",
}

// socksClass is the class of err, what failed under a dial through a SOCKS5 proxy that is no failed
// call of the system's: the proxy's reply where it is one of socksReplies, printed from that list,
// and otherwise that the proxy failed the connection.
func socksClass(err error) string {
	if err != nil {
		if reply, ok := strings.CutPrefix(err.Error(), "unknown error "); ok && slices.Contains(socksReplies, reply) {
			return "the SOCKS5 proxy answered " + reply
		}
	}
	return "the SOCKS5 proxy failed the connection"
}

// natsErrors are the errors the NATS client defines that a connection can fail with, each a fixed
// text that names no server (nats.go v1.55.0 nats.go:108-166). An error that wraps one under words
// of its own prints as the one it wraps alone.
var natsErrors = []error{
	nats.ErrConnectionClosed, nats.ErrConnectionDraining, nats.ErrDrainTimeout, nats.ErrConnectionReconnecting,
	nats.ErrSecureConnRequired, nats.ErrSecureConnWanted, nats.ErrTimeout, nats.ErrBadTimeout,
	nats.ErrAuthorization, nats.ErrAuthExpired, nats.ErrAuthRevoked, nats.ErrPermissionViolation,
	nats.ErrAccountAuthExpired, nats.ErrNoServers, nats.ErrJsonParse, nats.ErrMaxPayload,
	nats.ErrMultipleTLSConfigs, nats.ErrClientCertOrRootCAsRequired, nats.ErrNoInfoReceived, nats.ErrInvalidConnection,
	nats.ErrInvalidContext, nats.ErrNoDeadlineContext, nats.ErrNoEchoNotSupported, nats.ErrUserButNoSigCB,
	nats.ErrNkeyButNoSigCB, nats.ErrNoUserCB, nats.ErrNkeyAndUser, nats.ErrNkeysNotSupported,
	nats.ErrStaleConnection, nats.ErrTokenAlreadySet, nats.ErrUserInfoAlreadySet, nats.ErrDisconnected,
	nats.ErrHeadersNotSupported, nats.ErrMaxConnectionsExceeded, nats.ErrMaxAccountConnectionsExceeded,
	nats.ErrConnectionNotTLS, nats.ErrMaxSubscriptionsExceeded, nats.ErrWebSocketHeadersAlreadySet,
	nats.ErrServerNotInPool, nats.ErrMixingWebsocketSchemes, nats.ErrSlowConsumer,
}
