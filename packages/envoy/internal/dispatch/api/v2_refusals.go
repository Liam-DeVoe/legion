package api

// v2_refusals.go is the refusals that the /api/v2 rows of more than one group declare, each a
// condition and its one code (dispatch://INFRA-4071/spec R7), and the families of them that one
// shared write answers, which every row whose handler runs that write declares through one helper.

import "github.com/sjawhar/envoy/internal/dispatch/api/apicode"

var (
	issueClosedRefusal          = apicode.Refusal{Condition: apicode.OwnerIssueClosed, Code: apicode.IssueClosed}
	postDeletedRefusal          = apicode.Refusal{Condition: apicode.PostWithdrawn, Code: apicode.PostDeleted}
	valuePastBoundRefusal       = apicode.Refusal{Condition: apicode.ValuePastBound, Code: apicode.CapExceeded}
	artifactNotDocumentRefusal  = apicode.Refusal{Condition: apicode.ArtifactNotDocument, Code: apicode.NotDocument}
	sessionIDMalformedRefusal   = apicode.Refusal{Condition: apicode.SessionIDMalformed, Code: apicode.InvalidSessionID}
	anchorInvalidRefusal        = apicode.Refusal{Condition: apicode.AnchorInvalid, Code: apicode.InvalidAnchor}
	documentPreconditionRefusal = apicode.Refusal{Condition: apicode.DocumentPreconditionUnmet, Code: apicode.PreconditionFailed}
	envoyUnreachableRefusal     = apicode.Refusal{Condition: apicode.EnvoyUnreachable, Code: apicode.EnvoyUnavailable}
	envoyRefillingRefusal       = apicode.Refusal{Condition: apicode.EnvoyListRefilling, Code: apicode.EnvoyRefilling}
)

// unsubscribeRefusals are the refusals of the unsubscription of a session from an owner's topics
// (unsubscribeSession): the listener's, a topic only a broader one matches, and a session that
// holds none.
func unsubscribeRefusals() []apicode.Refusal {
	return []apicode.Refusal{
		envoyUnreachableRefusal, envoyRefillingRefusal,
		{Condition: apicode.EnvoyUnsubscribeRefused, Code: apicode.EnvoyUnsubscribeFailed},
		{Condition: apicode.TopicNotRemovable, Code: apicode.NotRemovable},
		{Condition: apicode.SessionNotSubscribed, Code: apicode.SubscriberNotFound},
	}
}
