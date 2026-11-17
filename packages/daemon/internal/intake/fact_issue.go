package intake

// IssueOf is the issue fact names, for a fact that names one, and false for any other: a fact
// that names none or several — a pull request's by its repository and number, a push, a tree's
// adoption of its children, the controller's tick over many issues, the daemon's own sweeps — and
// a fact type this switch does not know.
func IssueOf(fact Fact) (string, bool) {
	switch fact := fact.(type) {
	case DispatchIssue:
		return fact.Key, true
	case DispatchArtifact:
		return fact.Key, true
	case GateAboveDispatch:
		return fact.Read.Key, true
	case DispatchAskOpened:
		return fact.Key, true
	case DispatchAskRestored:
		return fact.Key, true
	case DispatchAskClosed:
		return fact.Key, true
	case DispatchAskReply:
		return fact.Key, true
	case DispatchApprovalMoved:
		return fact.Key, true
	case DispatchClaim:
		return fact.Key, true
	case Handover:
		return fact.Issue, true
	case MergeQueueReadDone:
		return fact.Issue, true
	case PullRequestMissing:
		return fact.Issue, true
	case HandoffComplete:
		return fact.Issue, true
	case BackwardMove:
		return fact.Issue, true
	case RetryOrEscalate:
		return fact.Issue, true
	case RestartPhase:
		return fact.Issue, true
	case SignOff:
		return fact.Issue, true
	case GateRegistered:
		return fact.Issue, true
	case CloseRoot:
		return fact.Issue, true
	case ClaimFailed:
		return fact.Issue, true
	case SessionLost:
		return fact.Issue, true
	case SessionStoreFault:
		return fact.Issue, true
	case MemoryGuardKill:
		return fact.Issue, true
	case ClaimReady:
		return fact.Issue, true
	case TaskConfirmed:
		return fact.Issue, true
	case GateRequestFound:
		return fact.Issue, true
	case CatchUpApplied:
		return fact.Issue, true
	case LingerExpired:
		return fact.Issue, true
	case CheckReport:
		return fact.Issue, true
	case CheckWakeDue:
		return fact.Issue, true
	case CheckLookEnded:
		return fact.Issue, true
	case CheckBound:
		return fact.Issue, true
	case PodBackstopDue:
		return fact.Issue, true
	case WorkerSilent:
		return fact.Issue, true
	case WorkerBackgroundStalled:
		return fact.Issue, true
	}
	return "", false
}
