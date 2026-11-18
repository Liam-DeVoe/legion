package supervise

import (
	"strconv"
	"strings"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// plainCause is the sentence the daemon tells a death by where the runtime read its cause
// (runtime.DeathCause), and "" for every other death. It is the daemon's own words, with numbers
// it formats from the quantities the runtime parsed (plainSize), and it names what the role's own
// work can change: the memory limit of a `roles` container the kernel killed for memory; the use
// and the request of the container a node-pressure eviction named over its request, since that pod
// passed its request and not its limit; and the disk limit of an eviction for the pod's own disk
// (dispatch://INFRA-1117 R9). It reads after "because", as every cause a let-go records does
// (EndedWork.Cause), and it leads the death's cause wherever that is told: the claim's record of
// it, which the notices of a claim its deaths fail carry (deathCause), and the record of what the
// death ended, which the role's next task carries (endDied). The container a node-pressure
// eviction named is one the pod's spec holds, which the runtime checked, so no word of the
// kubelet's message reaches the sentence.
//
// The memory limit's sentence is the one statement the role's next task makes that its pod ran
// out of memory: it says what outOfMemoryTask's own statement says, that the container runs every
// role of the issue, and names the limit, and the note behind it keeps only its guidance
// (outOfMemoryNote, namesMemoryLimit).
func plainCause(cause runtime.DeathCause) string {
	switch cause.Kind {
	case runtime.DeathMemoryLimit:
		return memoryLimitLead + plainSize(cause.Limit) + " memory limit"
	case runtime.DeathMemoryRequest:
		return "the kubelet evicted the pod for node memory pressure because its " + cause.Container + " container was using " +
			plainSize(cause.Use) + ", more than its " + plainSize(cause.Request) + " memory request"
	case runtime.DeathDiskLimit:
		return "the kubelet evicted the pod because its container's own disk, outside $TMPDIR, passed its " + plainSize(cause.Limit) + " limit"
	}
	return ""
}

// memoryLimitLead opens the memory limit's plain sentence (plainCause), up to the limit.
const memoryLimitLead = "the pod's roles container, which runs every role of this issue, was killed because it used more than its "

// namesMemoryLimit is whether cause, a cause a let-go recorded (EndedWork.Cause), opens with the
// memory limit's plain sentence, which endDied puts first in a death's cause. The record is the one
// persisted place the sentence is kept, and the note in front of a task reads it there
// (outOfMemoryNote) so that the task says once that the pod ran out of memory.
func namesMemoryLimit(cause string) bool { return strings.HasPrefix(cause, memoryLimitLead) }

// keepsDeathRecord is whether the record of observation's death is kept whatever the death cut off
// (recordEnd's keep): a death the daemon tells in plain words (plainCause) while the claim holds a
// task. That death is charged (chargeDeath) and its task is the role's next one, which must say
// what killed the process (dispatch://INFRA-1117 R9), but a task the agent never confirmed, of a
// claim in no turn, is in no turn the death cut off, and without this the record would be skipped.
// A claim holding no task is left to recordEnd's own rule: its death is not charged, and a record
// kept for it would wake a parked agent with a delivery of its own (queueRelaunch).
func (m *Machine) keepsDeathRecord(observation runtime.Observation) bool {
	return plainCause(observation.Cause) != "" && HoldsTask(m.claim.Pending)
}

// diskTask follows the plain sentence of a death for the pod's own disk in the role's next task
// (endDied): the disk the kubelet held to that limit is the container's own, and $TMPDIR is on the
// issue's volume, outside it, so that is where the role's large files go (dispatch://INFRA-1117
// R9). The notices of a failed claim do not carry it, since their reader is not the role.
const diskTask = "Keep large files under $TMPDIR, on the issue's volume."

// runtimeDetail introduces the runtime's own account of a death behind the plain sentence of its
// cause (deathCause, endDied), so a reader tells the daemon's words from the runtime's.
const runtimeDetail = "The runtime's detail: "

// plainSize is a number of bytes as a person reads one: in GiB from one GiB up and in MiB from one
// MiB up, to two decimals with the zeros that end them dropped (8 GiB, 15.34 GiB, 768 MiB), and in
// bytes below a MiB. Two decimals keep a use just over a request from reading as equal to it: one
// decimal would write 8.04 GiB as 8 GiB.
func plainSize(bytes int64) string {
	const mib, gib = 1 << 20, 1 << 30
	amount := func(value float64) string {
		return strings.TrimSuffix(strings.TrimRight(strconv.FormatFloat(value, 'f', 2, 64), "0"), ".")
	}
	switch {
	case bytes >= gib:
		return amount(float64(bytes)/gib) + " GiB"
	case bytes >= mib:
		return amount(float64(bytes)/mib) + " MiB"
	}
	return strconv.FormatInt(bytes, 10) + " bytes"
}
