package daemon

// The running copy's half of a project's pause (dispatch://INFRA-3759 beside R1 of
// dispatch://INFRA-1117): while the daemon's project is paused no copy starts
// (runningCopyProjectPaused), read as a copy starts and again just before its shim is asked
// (takeRunningCopy, storeRunningCopy).

// projectPaused is the open pause's id and true while the daemon's project is paused, as the
// pause service last read or made it (pause.Service.Paused), and false for a runtime that holds no
// pause: run builds the project's pause before supervision and before any copy can start
// (workflowRuntime.pauses), and a runtime a test builds by hand holds none.
func (w *workflowRuntime) projectPaused() (int64, bool) {
	if w.pauses == nil {
		return 0, false
	}
	return w.pauses.service.Paused()
}

// pausedCopy logs a copy of issue that the open pause of the daemon's project kept from its shim,
// at Info under runningCopyProjectPaused with the pause's id, since nothing failed
// (runningCopyUnsavedLine). args are what the line names between the issue and the reason: the
// role the copy was asked of, where one was chosen.
func (w *workflowRuntime) pausedCopy(issue string, pause int64, args ...any) {
	args = append(append([]any{"issue", issue}, args...), "reason", runningCopyProjectPaused, "pause", pause)
	w.log.Info(runningCopyUnsavedLine, args...)
}
