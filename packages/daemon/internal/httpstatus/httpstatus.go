// Package httpstatus holds the rule the daemon reads another service's HTTP status by when it
// decides whether the same request may be answered if it is asked again. It is RFC 9110's: a 5xx
// is the server failing to answer (section 15.6), a 408 Request Timeout is a server that did not
// receive the whole request in time, which the client may repeat (section 15.5.9), and a 429 Too
// Many Requests tells the client to try later (RFC 6585 section 4). The readers that tell an
// outage from a final answer by status read it here: the sandbox runtime's reading of the
// Kubernetes API server (serverBusy), the role lookup at the Envoy listener (notify.RoleHolder)
// and the GitHub App calls (appauth). The Envoy module states the same rule as
// oauthrefusal.Outage, an internal package of that module, which this one cannot import.
package httpstatus

import "net/http"

// Outage reports whether status is a server that could not answer the request now: any 5xx, a 408
// and a 429. Every other status is the server's answer, whatever its body says, which asking again
// repeats. A reader whose own contract says otherwise for one of them says so where it reads it.
func Outage(status int) bool {
	return status >= http.StatusInternalServerError || status == http.StatusRequestTimeout ||
		status == http.StatusTooManyRequests
}
