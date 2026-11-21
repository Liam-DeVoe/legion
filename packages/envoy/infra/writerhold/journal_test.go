package writerhold

import "github.com/sjawhar/envoy/internal/holdrecord"

// latestState is the state of command's latest entry in d, or "" when it has none (holdrecord's
// Done.Latest).
func latestState(d holdrecord.Done, command string) string {
	e, _ := d.Latest(command)
	return e.State
}
