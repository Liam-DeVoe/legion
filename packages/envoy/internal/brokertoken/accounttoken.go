package brokertoken

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// AccountTokenCacheTTL is how long an AccountToken reuses the bytes it last read. Opening the file
// on every call would cost a read per request for a credential that changes a few times a day: the
// kubelet rewrites a projected token in place well before it expires, and the tokens read here are
// projected for an hour, so bytes this fresh are never near expiry and a rewrite reaches the
// callee within this window of it.
const AccountTokenCacheTTL = 30 * time.Second

// Account is the caller's own platform identity, read fresh at call time: the bearer a call
// presents to a callee that verifies the caller's own platform token, for the audience that
// callee verifies, and resolves the calling service from it. It is an interface because callers
// hold two kinds of identity: a caller in a pod holds a projected Kubernetes ServiceAccount token
// in a file (AccountToken), and one on a Compute Engine VM holds no Kubernetes token at all and
// asks its metadata server for the machine's own Google identity token instead (GoogleAccount).
//
// Read is called on the path of one call, so an implementation caches what it read: the
// credentials behind both implementations rotate, which is why neither is read once at boot.
type Account interface {
	// Read is the bearer to present, or an error that never reached the callee: a deployment's
	// own credential it could not read is never an empty bearer for the callee to refuse.
	Read() (string, error)
}

// AccountToken is the Account of a caller holding a projected Kubernetes ServiceAccount token in
// a file, for a callee that verifies the caller's own projected token. One reader serves every call
// a program makes with that file, so a projected token is cached once rather than once per caller.
//
// A projected token rotates, so the path is read at call time and never once at boot.
type AccountToken struct {
	// Path is the file the token is read from.
	Path string
	// TTL is how long Read reuses what it read, AccountTokenCacheTTL for every reader
	// NewAccountToken builds. A test that proves a rewritten file is picked up lapses the cache
	// by zeroing it.
	TTL time.Duration

	mu     sync.Mutex
	cached string
	readAt time.Time
}

// NewAccountToken is a reader of the projected ServiceAccount token at path, which Read opens at
// call time, caching what it reads for AccountTokenCacheTTL.
func NewAccountToken(path string) *AccountToken {
	return &AccountToken{Path: path, TTL: AccountTokenCacheTTL}
}

// AccountTokenError is a call that never reached its callee because the caller could not read its
// own ServiceAccount token from Path: the file is missing or unreadable, or holds nothing. It is
// its own type because what is wrong is the caller's deployment, not the callee, and a route must
// say so rather than send the operator to a callee that was never asked.
type AccountTokenError struct {
	// Path is the token file the caller tried to read.
	Path string
	// Err is why it could not: the read's own error, or errEmptyAccountToken.
	Err error
}

// Error names the caller's own token file and no package, since it reaches the text of whatever
// call it stopped (a Dispatch delivery to its listener among them), whose reader is sent to the
// caller's deployment, not to the secrets broker this package is named for.
func (e *AccountTokenError) Error() string {
	return fmt.Sprintf("read the caller's own ServiceAccount token %s: %v", e.Path, e.Err)
}

func (e *AccountTokenError) Unwrap() error { return e.Err }

// errEmptyAccountToken is an AccountTokenError's reason when the file reads but holds nothing.
var errEmptyAccountToken = errors.New("the file is empty")

// Read is the bearer a call presents: Path's trimmed contents, read again once the copy it cached
// is older than TTL. A file that cannot be read, or that holds nothing, is an *AccountTokenError
// naming the path and no request at all, never an empty bearer for the callee to refuse: a
// projected token the caller cannot read is the caller's own misconfiguration.
func (a *AccountToken) Read() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cached != "" && time.Since(a.readAt) < a.TTL {
		return a.cached, nil
	}
	data, err := os.ReadFile(a.Path)
	if err != nil {
		return "", &AccountTokenError{Path: a.Path, Err: err}
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", &AccountTokenError{Path: a.Path, Err: errEmptyAccountToken}
	}
	a.cached, a.readAt = token, time.Now()
	return token, nil
}
