// packages/envoy/internal/cipher/proxy/enrollment.go
// Every live grant of one enrollment on one connection: a command holds the placeholders of each
// grant its session has, and its proxy password names one of them, so a host any of them binds is
// read (sessionBinds) and a request is handled for the grant whose placeholder it carries
// (handledFor). The enrollment's other grants are read only where that can change the answer, so a
// tunnel to a host no entry binds, and a request of the connection's own grant, read nothing more.
package proxy

import (
	"context"

	"github.com/sjawhar/envoy/internal/cipher/proxyrules"
	"github.com/sjawhar/envoy/internal/cipher/requests"
)

// handling is what one request on a decrypted connection is handled for: the grant, the (name,
// slot) pair of it (choosePair), and whether the request carries that pair's placeholder.
type handling struct {
	grant            requests.AirlockConnection
	chosen           pair
	holdsPlaceholder bool
}

// handlingFor is what a request would be handled for under grant alone: false when no name of
// grant binds host on port (candidates), and otherwise the pair choosePair chooses and whether the
// request carries its placeholder.
func (s *Server) handlingFor(grant requests.AirlockConnection, host string, port uint16, request *proxyrules.Request) (handling, bool) {
	candidates := s.candidates(grant, host, port)
	if len(candidates) == 0 {
		return handling{}, false
	}
	chosen, holdsPlaceholder := choosePair(candidates, grant, request)
	return handling{grant: grant, chosen: chosen, holdsPlaceholder: holdsPlaceholder}, true
}

// handledFor is the grant and the (name, slot) pair one request on a decrypted connection is
// handled for. own is the grant the connection's password proves, as this request read it. A
// command holds the placeholders of every grant its session has - a role's own GitHub grant
// beside the one an agent-secrets run inside it asked for, a mail client's write grant beside the
// read grant whose run it is - and its password names one of them, so the proxy swaps a
// placeholder of any live grant of the enrollment on one connection:
//
//   - a request that carries a placeholder of own is handled for own, as choosePair chooses, and
//     reads nothing more, which is the common path;
//   - a request own binds the host for and whose headers hold no whole placeholder is handled for
//     own's first candidate, and reads nothing more either: another grant is chosen only by a
//     header that carries its placeholder (choosePair), so the read could change nothing;
//   - otherwise the other live grants of own's enrollment that hold a name bound to the host are
//     read (otherGrantsBinding), and the request is handled for the first of them, in grant id
//     order, that binds the host and whose placeholder it carries;
//   - a request that carries none is decided for own's first candidate, or, where own binds
//     nothing here, for the first candidate of the first other grant that binds the host, and
//     forwarded with nothing swapped, so the entry's rules bound it as they bound any request.
//
// One request is still handled for one (name, slot) pair of one grant: a request that carries
// placeholders of two grants is handled for own when one of them is own's, and otherwise for the
// first of them in grant id order. The second answer is false when no live grant of the
// enrollment binds the host.
func (s *Server) handledFor(ctx context.Context, own requests.AirlockConnection, request *proxyrules.Request) (handling, bool, error) {
	host, port := request.ConnectHost, request.ConnectPort
	fallback, found := s.handlingFor(own, host, port, request)
	if found && (fallback.holdsPlaceholder || !headersHoldPlaceholder(request.Headers)) {
		return fallback, true, nil
	}
	others, err := s.otherGrantsBinding(ctx, own, host, port)
	if err != nil {
		return handling{}, false, err
	}
	for _, other := range others {
		handled, binds := s.handlingFor(other, host, port, request)
		if !binds {
			continue
		}
		if handled.holdsPlaceholder {
			return handled, true, nil
		}
		if !found {
			fallback, found = handled, true
		}
	}
	return fallback, found, nil
}

// headersHoldPlaceholder is whether a header of the request holds a whole placeholder
// (headerPlaceholder), which is the one thing that can hand a request to another grant.
func headersHoldPlaceholder(headers [][2]string) bool {
	for _, header := range headers {
		if headerPlaceholder(header[1]) != "" {
			return true
		}
	}
	return false
}

// sessionBinds is whether a proxy request to host is to a bound host: whether a name of grant, the
// grant the proxy password proves, or of another live grant of its enrollment binds host on port
// (candidates). It is the one answer serveConnect reads to decrypt a CONNECT rather than tunnel
// it, and servePlain to refuse plain HTTP: a host only another grant of the session binds is read,
// and a request on it is handled for the grant whose placeholder it carries (handledFor). The
// enrollment's other grants are read only where grant binds nothing, and then only those that
// hold a name an entry binds to host (otherGrantsBinding).
func (s *Server) sessionBinds(ctx context.Context, grant requests.AirlockConnection, host string, port uint16) (bool, error) {
	if len(s.candidates(grant, host, port)) > 0 {
		return true, nil
	}
	others, err := s.otherGrantsBinding(ctx, grant, host, port)
	if err != nil {
		return false, err
	}
	for _, other := range others {
		if len(s.candidates(other, host, port)) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// otherGrantsBinding is the live grants of own's enrollment, but own, that hold a placeholder of a
// name an entry binds to host on port (ProxyGrantsOfEnrollment with boundEntries' names). The
// broker reads and checks only those, so a grant that holds no such placeholder - whose project
// check may need Dispatch, which may not answer - is not read for a call to this host, and a host
// no entry binds reads nothing at all: a tunnel to a package registry, or to a git host the session
// holds no secret for, cannot fail for a read of other grants. A grant that holds such a
// placeholder is read and checked whole, a name of it its operator withheld included, so its
// failed check can still answer such a call 503, failing closed.
func (s *Server) otherGrantsBinding(ctx context.Context, own requests.AirlockConnection, host string, port uint16) ([]requests.AirlockConnection, error) {
	names := s.boundEntries(host, port)
	if len(names) == 0 {
		return nil, nil
	}
	return s.grants.ProxyGrantsOfEnrollment(ctx, own.EnrollmentID, own.GrantID, names)
}

// boundEntries is the name of every entry of the rules that binds host on port (entryBinds), in
// the rules' order. The rules are read once at New, and an entry binds a host for a grant only
// through them (candidates), so a grant that holds none of these names binds host for no request.
func (s *Server) boundEntries(host string, port uint16) []string {
	var names []string
	for i := range s.rules.Secrets {
		if entryBinds(&s.rules.Secrets[i], host, port) {
			names = append(names, s.rules.Secrets[i].Name)
		}
	}
	return names
}
