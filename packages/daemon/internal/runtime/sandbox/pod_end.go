package sandbox

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// podEndRecordLife is how long each of the two records a pod's end needs stands: the daemon's own
// record that it means to end a pod (intendedEnds), written before the act and read when the end
// arrives, and the reading the end was captured as (podEnds), written where the informer delivers
// the end and read again by each claim of that pod as it is evaluated.
//
// It is long enough that every claim of the pod reaches its verdict while the reading stands: the
// six roles of an issue pod share one pod and are evaluated one at a time, each marked for the
// running Observe by the same informer event that captured the reading, and the slowest of them
// waits a probe interval (30 seconds in the deployment's configuration) and the API reads one
// evaluation makes. Ten minutes is twenty such intervals.
//
// It is short enough that a pod which keeps running outlives it, which is what the record of an
// intended end needs: the daemon patches the Sandbox, the patch does not take, the pod runs on,
// and the pod's next genuine end must be read for what it is rather than taken for the end that
// patch meant to make. An issue pod runs for hours, so a record ten minutes old belongs to an act
// whose end never came, and prunePodEnds drops it rather than letting it answer for an end that
// is not the one it was written for.
const podEndRecordLife = 10 * time.Minute

// deletedByAnother is the reading of a pod the daemon found being deleted with no act of its own
// to account for it. The daemon deletes no pod itself — it patches or deletes the pod's Sandbox,
// and the Agent Sandbox controller deletes the pod — so every end the daemon makes carries a
// deletion timestamp the daemon did not write, and this reading is made only where no record of
// an intended end answers for the pod. It is made only of a pod not yet terminal: a terminal pod
// is read from its status alone, whatever deletion timestamp it carries (capturePodEnd).
const deletedByAnother = "the pod was being deleted with no act of the daemon's to account for it"

// clusterEndedReasons are the reasons a pod's own status carries when the cluster ended it, and
// the whole of that set: a pod the kubelet ended because its node is going down (Terminated,
// NodeShutdown), and one the scheduler preempted for a pod of higher priority (Preempted). A reason
// outside the set is not one of them, however it reads: UnexpectedAdmissionError is a pod a node
// refused before it ran, which the roles of the next pod would meet again, and an end the daemon
// waived would be relaunched for ever.
//
// Evicted (evictedReason) is not in the set, because the reason alone does not say who caused the
// eviction. The kubelet writes it for a node under pressure, and then it also sets the
// DisruptionTarget condition, which disruption reads first. It writes the same reason, with no
// condition, for a pod that went over a limit of its own: the eviction manager's
// podEphemeralStorageLimitEviction (the pod's ephemeral-storage limit),
// containerEphemeralStorageLimitEviction (a container's) and emptyDirLimitEviction (an emptyDir's
// sizeLimit) each call evictPod with a nil condition (kubernetes pkg/kubelet/eviction/
// eviction_manager.go). Only the node-pressure path in synchronize passes one. Those three pods
// are ended by what they do themselves: a task that fills its own disk gets a new pod, the same
// task and the same eviction, and read as an end from outside the pod that would be a relaunch
// for ever. The kubelet's admission check writes `Evicted` with no condition as well, for a pod a
// node under pressure refuses before it runs, which is the refusal UnexpectedAdmissionError is.
// So `Evicted` with no DisruptionTarget condition reads as neither reading and is charged a
// death (capturePodEnd). So does an eviction under node memory pressure whose message names a
// container of the pod using more memory than it requested (memoryOverRequest), though it carries
// the condition: that pod's own memory use is what the kubelet evicted first, and a new pod
// running the same work would use as much.
var clusterEndedReasons = []string{"Terminated", "NodeShutdown", "Preempted"}

// evictedReason is the status reason the kubelet gives every pod it evicts. It is an end from
// outside the pod only when the DisruptionTarget condition comes with it (clusterEndedReasons says
// why).
const evictedReason = "Evicted"

// evictedForItsOwnUse is the reading of a pod the kubelet evicted with no DisruptionTarget
// condition: it went over its own ephemeral-storage limit, a container's limit or an emptyDir's
// sizeLimit, or a node under pressure refused it at admission (clusterEndedReasons). Such an end
// is read neither way and charged.
const evictedForItsOwnUse = "the kubelet evicted the pod with no DisruptionTarget condition: over a limit of its own, or refused at admission"

// evictedForItsOwnMemoryUse is the reading of a pod the kubelet evicted under node memory
// pressure whose message names a container of the pod using more memory than it requested
// (memoryOverRequest). The kubelet evicts the pods using more than their request before any
// other, so the pressure the end answered was the pod's own memory use and not a neighbour's:
// such an end is read neither way and charged, as an eviction over a limit of the pod's own is,
// whatever DisruptionTarget condition it carries.
const evictedForItsOwnMemoryUse = "the kubelet evicted the pod for node memory pressure, its message naming a container of the pod over its memory request"

// containerOverRequest matches the sentence the kubelet's eviction manager adds to the message of
// a pod it evicts for node memory pressure for each container of the pod using more memory than it
// requested. It is upstream's containerMessageFmt, "Container %s was using %s, request is %s, has
// larger consumption of %v. " (kubernetes pkg/kubelet/eviction/helpers.go, evictionMessage), with
// the container's name, its use and its request, each quantity a resource.Quantity's String, and
// the resource the node was short of, here memory. The sentence ends in a space, which the match
// lets the message's end stand in for.
var containerOverRequest = regexp.MustCompile(`Container ([^ ]+) was using ([^ ,]+), request is ([^ ,]+), has larger consumption of memory\.(?: |$)`)

// memoryOverRequest is the cause of the end of pod where the kubelet evicted it for node memory
// pressure — its reason is evictedReason and its DisruptionTarget condition is true — and its
// message names, in containerOverRequest's words, a container of the pod's own spec whose use and
// request both parse and whose use is the larger: that container, its use and its request
// (runtime.DeathMemoryRequest). A message naming no such container, none over its request or only
// a name the pod's spec does not hold, is a neighbour's pressure, and it answers the zero cause, so
// the end is read as one from outside the pod (disruption). Each number is the quantity the message
// gives, parsed, and the name is one the pod's spec holds, so no word of the message's own reaches
// a sentence built from the cause.
func memoryOverRequest(pod *corev1.Pod) runtime.DeathCause {
	if c := podCondition(pod, corev1.DisruptionTarget); pod.Status.Reason != evictedReason || c == nil || c.Status != corev1.ConditionTrue {
		return runtime.DeathCause{}
	}
	for _, named := range containerOverRequest.FindAllStringSubmatch(pod.Status.Message, -1) {
		if !specContainer(pod, named[1]) {
			continue
		}
		use, useErr := resource.ParseQuantity(named[2])
		request, requestErr := resource.ParseQuantity(named[3])
		if useErr != nil || requestErr != nil || use.Cmp(request) <= 0 {
			continue
		}
		return runtime.DeathCause{Kind: runtime.DeathMemoryRequest, Container: named[1], Use: use.Value(), Request: request.Value()}
	}
	return runtime.DeathCause{}
}

// specContainer is whether pod's spec holds a container named name, an init container among them,
// since the kubelet's eviction message reads both kinds.
func specContainer(pod *corev1.Pod, name string) bool {
	named := func(c corev1.Container) bool { return c.Name == name }
	return slices.ContainsFunc(pod.Spec.Containers, named) || slices.ContainsFunc(pod.Spec.InitContainers, named)
}

// diskLimitSentences match the sentences the kubelet's eviction manager writes as the message of
// a pod it evicts for passing a disk limit of its own, each with that limit (kubernetes
// pkg/kubelet/eviction/helpers.go): podEphemeralStorageMessageFmt, "Pod ephemeral local storage
// usage exceeds the total limit of containers %s. ", the pod's own limit as a quantity's String;
// containerEphemeralStorageMessageFmt, "Container %s exceeded its local ephemeral storage limit
// %q. ", a container's, quoted; and emptyDirMessageFmt, "Usage of EmptyDir volume %q exceeds the
// limit %q. ", an emptyDir's sizeLimit, quoted. Each ends in a space, which the match lets the
// message's end stand in for.
var diskLimitSentences = []*regexp.Regexp{
	regexp.MustCompile(`Pod ephemeral local storage usage exceeds the total limit of containers ([^ ]+)\.(?: |$)`),
	regexp.MustCompile(`Container [^ ]+ exceeded its local ephemeral storage limit "([^"]+)"\.(?: |$)`),
	regexp.MustCompile(`Usage of EmptyDir volume "[^"]*" exceeds the limit "([^"]+)"\.(?: |$)`),
}

// admissionRefusal starts the message the kubelet's eviction manager writes for a pod a node under
// pressure refuses at admission, which it gives the reason `Evicted` and no condition, as it does a
// pod over a limit of its own (clusterEndedReasons): upstream's nodeConditionMessageFmt, "The node
// had condition: %v. " (kubernetes pkg/kubelet/eviction/helpers.go, managerImpl.Admit). Such a pod
// never ran, so it passed no disk limit of its own.
const admissionRefusal = "The node had condition: "

// diskEviction is the cause of the end of pod, a pod selfEvicted names, where the kubelet evicted
// it for passing a disk limit of its own (runtime.DeathDiskLimit): the limit its message gives in
// one of diskLimitSentences, parsed, or, where the message gives none that parses, the limit the
// pod's spec holds it to as a whole (specDiskLimit). A pod refused at admission (admissionRefusal),
// and one whose spec sets no limit either, answer the zero cause.
func diskEviction(pod *corev1.Pod) runtime.DeathCause {
	if strings.HasPrefix(pod.Status.Message, admissionRefusal) {
		return runtime.DeathCause{}
	}
	for _, sentence := range diskLimitSentences {
		if named := sentence.FindStringSubmatch(pod.Status.Message); named != nil {
			if limit, err := resource.ParseQuantity(named[1]); err == nil && limit.Sign() > 0 {
				return runtime.DeathCause{Kind: runtime.DeathDiskLimit, Limit: limit.Value()}
			}
		}
	}
	if limit := specDiskLimit(pod); limit > 0 {
		return runtime.DeathCause{Kind: runtime.DeathDiskLimit, Limit: limit}
	}
	return runtime.DeathCause{}
}

// specDiskLimit is the ephemeral-storage limit pod's spec holds the pod to as a whole, which is the
// limit the kubelet evicts it for passing: the sum of its containers' limits, or one init
// container's where that is larger, since the init containers run one at a time before the others
// start (the kubelet's PodLimits, for a pod with no sidecar). It is 0 where no container sets one.
func specDiskLimit(pod *corev1.Pod) int64 {
	var total int64
	for _, c := range pod.Spec.Containers {
		total += c.Resources.Limits.StorageEphemeral().Value()
	}
	for _, c := range pod.Spec.InitContainers {
		total = max(total, c.Resources.Limits.StorageEphemeral().Value())
	}
	return total
}

// terminalWithNoClusterEnd is the reading of a pod already terminal whose status names neither an
// end the cluster made (disruption) nor an eviction (selfEvicted): a reason outside
// clusterEndedReasons, UnexpectedAdmissionError among them, or no reason at all. Such an end is
// read neither way and charged, whatever deletion timestamp the pod carries (capturePodEnd).
const terminalWithNoClusterEnd = "the pod was terminal and its status names no end the cluster made"

// disruption is whether the cluster says it ended pod, with the words the daemon read that from
// and the reason the cluster gave: the DisruptionTarget condition the control plane and the
// kubelet set on a pod they are ending — an eviction, a preemption, a node shutting down or
// draining — and otherwise one of clusterEndedReasons on the pod's own status. Both readings live
// here, read in this one place. A pod whose status reason is evictedReason with no condition is no
// disruption (clusterEndedReasons); selfEvicted names that end for capturePodEnd. A disruption the
// kubelet made for node memory pressure whose message names a container of the pod over its
// memory request is still one here, and capturePodEnd asks memoryOverRequest before it asks this.
//
// kubeletFailure asks a different question of the same two facts, so neither reads the other: it
// answers any non-empty reason, which this excludes, and it reads the image probe's pod, which
// runs under `restartPolicy: Never` where an issue pod is RestartPolicyAlways.
func disruption(pod *corev1.Pod) (cause, reason string, ok bool) {
	if c := podCondition(pod, corev1.DisruptionTarget); c != nil && c.Status == corev1.ConditionTrue {
		return "the cluster marked the pod a disruption target", c.Reason, true
	}
	if slices.Contains(clusterEndedReasons, pod.Status.Reason) {
		return "the cluster ended the pod", pod.Status.Reason, true
	}
	return "", "", false
}

// selfEvicted is whether the kubelet evicted pod with no DisruptionTarget condition, the end
// evictedForItsOwnUse names: the reason is evictedReason and the condition is not set true. It
// reads the condition itself rather than leaning on disruption having answered no first, so no
// order of capturePodEnd's cases, and no other caller, can read an eviction under node pressure,
// which carries the condition, as the pod's own and have it charged.
func selfEvicted(pod *corev1.Pod) bool {
	if pod.Status.Reason != evictedReason {
		return false
	}
	c := podCondition(pod, corev1.DisruptionTarget)
	return c == nil || c.Status != corev1.ConditionTrue
}

// intendedEnd is one act of the daemon's own that is about to end a pod: the act, in the words the
// pod's end is logged and detailed by, and the moment it was recorded, which its expiry is read
// from (podEndRecordLife).
type intendedEnd struct {
	act string
	at  time.Time
}

// podEnd is what the daemon read about the cause of one pod's end: the reading the supervisor
// decides on (runtime.PodEndReading), the words that reading was made from, the reason the cluster
// gave where it gave one, the cause the daemon tells in plain words where the end is one of the
// pod's own it names (runtime.DeathCause: memoryOverRequest, diskEviction), and the moment it was
// read. The cause is kept here because the kubelet's message it is read from leaves with the pod.
type podEnd struct {
	reading runtime.PodEndReading
	cause   string
	reason  string
	death   runtime.DeathCause
	at      time.Time
}

// detail is what a claim's observation of this end says of it, after the mapping's own detail: the
// words the reading was made from, with the cluster's reason where there was one.
func (e podEnd) detail() string {
	if e.reason == "" {
		return e.cause
	}
	return e.cause + " (" + e.reason + ")"
}

// meanToEndPod records, before an act of the daemon's own that will end the pod uid, that the
// daemon means to end it: the end is then read as the daemon's own (runtime.PodEndOurs) and
// charged as it always was, rather than as something outside the pod ending it. Every such act
// writes the pod's Sandbox rather than the pod — the idle release's teardown and an issue's close,
// a tree's cleanup and the orphan sweep, an update round's replacement and a relaunch's — so every
// one of the daemon's own ends reaches the pod as a deletion timestamp the daemon did not write,
// and this record is the whole of what tells those ends from an eviction's.
//
// It is written before the act and read after it, so it is dropped by its consumer the moment the
// end it was written for is read (takeIntendedEnd) and by its expiry when no end comes
// (podEndRecordLife): a record left standing would have the pod's next genuine end read as an end
// of the daemon's own. Every write prunes what has expired, so the map cannot grow without bound.
//
// The record is held in memory alone. A daemon that restarts between the act and the pod's end
// has lost it, so an end the daemon itself made is then read as an end from outside the pod and
// its roles are relaunched uncharged, as for every end from outside a pod (supervise's
// endedFromOutside).
func (r *Runtime) meanToEndPod(uid, act string) {
	if uid == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prunePodEnds()
	r.intendedEnds[uid] = intendedEnd{act: act, at: r.now()}
}

// meanToEndPodOf is meanToEndPod for the pod the Sandbox s owns in the store now, which is the pod
// the caller's act is about to end. A Sandbox the store holds no pod of records nothing: there is
// no pod for the act to end, and a same-named pod it does not own is no process of its claims'.
func (r *Runtime) meanToEndPodOf(s *sandbox, act string) {
	if s == nil {
		return
	}
	if pod := r.viewOf(s.Name, s).pod; pod != nil {
		r.meanToEndPod(string(pod.UID), act)
	}
}

// takeIntendedEnd is the consumer of the records meanToEndPod writes: it answers the act the
// daemon recorded for the pod uid and drops that record, the end it was written for having now
// been read and nothing being left that could read it. A record older than podEndRecordLife is
// dropped and answers nothing, so an act whose end never came waives no later death. Called with
// r.mu held.
func (r *Runtime) takeIntendedEnd(uid string) (string, bool) {
	intended, ok := r.intendedEnds[uid]
	delete(r.intendedEnds, uid)
	if !ok || r.now().Sub(intended.at) >= podEndRecordLife {
		return "", false
	}
	return intended.act, true
}

// prunePodEnds drops every record of an intended end and every captured reading that has outlived
// podEndRecordLife. It runs wherever one of them is written, so neither map grows without bound on
// a daemon that has watched pods come and go for weeks. Called with r.mu held.
func (r *Runtime) prunePodEnds() {
	now := r.now()
	maps.DeleteFunc(r.intendedEnds, func(_ string, e intendedEnd) bool { return now.Sub(e.at) >= podEndRecordLife })
	maps.DeleteFunc(r.podEnds, func(_ string, e podEnd) bool { return now.Sub(e.at) >= podEndRecordLife })
}

// capturePodEnd reads what ended pod and keeps that reading by the pod's uid, once, the first time
// the pod shows an end; gone says the informer delivered the pod as deleted, which is an end
// whatever the copy that event carries says.
//
// The reading is made here, where the informer delivers the object, because neither fact it rests
// on can be read once the end is final: a pod being deleted carries its deletion timestamp while
// it still runs, which the mapping reads as a live pod (verdict), and by the time a claim of that
// pod is evaluated for the end the object has left the store. One reading answers every claim of
// the pod — the six roles of an issue pod share one — so it is kept by the pod's uid, which no
// other pod ever holds, beside the Sandbox-name record that stands in for an annotation
// (stranded).
//
// An end the daemon's own record names is the daemon's own (takeIntendedEnd), whatever the cluster
// made of it, so a pod the daemon is ending that the kubelet then evicts continues no role. An end
// the cluster says it made (disruption) is an end from outside the pod, except an eviction under
// node memory pressure whose message names a container of the pod over its memory request
// (memoryOverRequest): that is the pod's own memory use, read neither way, with its cause kept for
// the claims of the pod (podEnd.death). Past those, a pod already terminal — in the phase the
// kubelet sets on a pod it has ended (terminal) — is read from its status alone, and a deletion
// timestamp counts only on a pod not yet terminal. A terminal pod is read neither way, whatever
// its reason and whatever deletion timestamp it carries: `Evicted` with no DisruptionTarget
// condition (selfEvicted), whose disk limit is kept as its cause where the kubelet evicted it for
// passing one (diskEviction), a reason outside the cluster's own such as UnexpectedAdmissionError,
// or no reason at all. The words of an end read neither way (evictedForItsOwnMemoryUse,
// evictedForItsOwnUse, terminalWithNoClusterEnd) and its reason are kept for the line
// (logPodEnd), and no caller is answered with them (podEndOf). A pod not yet terminal that
// carries a deletion timestamp no act of the daemon's accounts for is an end from outside it
// (deletedByAnother). A pod the informer delivers as deleted is read by the same rule from the
// last state it carries.
//
// The rule reads a pod's failure the same however the informer delivers its facts. The controller
// deletes a pod the kubelet has failed, and the informer can deliver the terminal status and the
// deletion timestamp in one event or in two. Read from the deletion timestamp wherever there is
// one, the same failure would be continued uncharged when both came in one event and charged when
// the status came first, and a pod a node keeps refusing, or a task that fills its own disk, would
// be relaunched for ever whenever the two came together.
//
// The rule has one cost: an outside delete the daemon first sees when the pod is already terminal
// is charged a death. That is rare. The informer hands notify each watch event in order, and a
// graceful delete sets the deletion timestamp on the running pod before the kubelet makes it
// terminal, so the daemon sees the running pod's deletion first and reads it as an end from
// outside. It sees the terminal pod first only when its watch missed that update and a relist
// delivered only the later state: that is inferred from how client-go's watch and relist behave,
// and no test here proves the order the informer delivers in.
//
// Every reading is kept, an end of neither reading too, which is kept with an empty reading and
// charged as it always was. The record is what makes the first event that shows a pod's end the
// one that decides: a deletion read from outside stays so when the kubelet's terminal status
// follows it, and no later event of the same end — the controller's delete, pod garbage
// collection, an operator's own `kubectl delete pod`, the informer's delete — is read again or
// gets a line of its own. The empty reading is answered to no caller as a reading (podEndOf), so
// it decides nothing it did not read; the cause it carries, where it carries one, is answered.
// Called with r.mu held.
func (r *Runtime) capturePodEnd(pod *corev1.Pod, gone bool) {
	uid := string(pod.UID)
	if uid == "" || !podEnding(pod, gone) {
		return
	}
	if _, read := r.podEnds[uid]; read {
		return
	}
	end := podEnd{at: r.now()}
	act, intended := r.takeIntendedEnd(uid)
	cause, reason, disrupted := disruption(pod)
	ownMemory := memoryOverRequest(pod)
	switch {
	case intended:
		end.reading, end.cause, end.reason = runtime.PodEndOurs, act, pod.Status.Reason
	case ownMemory.Kind != "":
		end.cause, end.reason, end.death = evictedForItsOwnMemoryUse, reason, ownMemory
	case disrupted:
		end.reading, end.cause, end.reason = runtime.PodEndNotOurs, cause, reason
	case terminal(pod) && selfEvicted(pod):
		end.cause, end.reason, end.death = evictedForItsOwnUse, pod.Status.Reason, diskEviction(pod)
	case terminal(pod):
		end.cause, end.reason = terminalWithNoClusterEnd, pod.Status.Reason
	case pod.DeletionTimestamp != nil:
		end.reading, end.cause = runtime.PodEndNotOurs, deletedByAnother
	}
	r.prunePodEnds()
	r.podEnds[uid] = end
	r.logPodEnd(pod, end)
}

// podEnding is whether pod's event is one an end can be read from: the informer delivered it as
// deleted (gone), it carries a deletion timestamp, or the kubelet has it in a terminal phase. A
// pod that is merely running is no end, and nothing is read or consumed for it.
func podEnding(pod *corev1.Pod, gone bool) bool {
	return gone || pod.DeletionTimestamp != nil || terminal(pod)
}

// podEndLine is the one line a pod's end gets, written where the end is first read and naming the
// pod, which reading was made, the words it was made from, the reason the cluster gave where it
// gave one, and, as ended, the roles whose processes the end ended: every role of that pod the
// runtime watches. The line names what ended and nothing of what any claim got for it. It is
// written before any claim is decided, and the supervisor decides each claim on its own — a
// pod_end_not_ours end continued uncharged however often it comes, an end read neither way charged
// as the claim's own death wherever that claim had work outstanding — so each claim's own
// supervise line says what that claim got (supervise's endedFromOutside, died). One line
// per pod end and not one per role: a pod's six roles end together, and the runtime knows every
// watched claim whose locator names the pod. A reader of the line tells a restart an eviction
// caused from one an out-of-memory kill caused.
const podEndLine = "sandbox runtime: read what ended a watched pod"

// logPodEnd writes that line for end, the reading just made of pod's end: at warning level for an
// end from outside the pod, which cost every role in it its turn through nothing of its own, and
// at info level for one the daemon itself made, which is the daemon reporting its own act, or for
// an end read neither way, which is charged as it always was.
//
// A pod the runtime watches no claim of gets no line. The line is the record of the watched roles
// an end ended, and a pod no claim of this runtime runs in ends none: the pods informer carries
// the image probe's pod and each repository cache's pod beside the issue and controller pods, and
// every one of those ends — a probe pod ends at every boot — would otherwise write, at warning
// level, a line for an end from outside the pod that names ended=[]. The reading itself is kept
// whether or not the line is written: a claim of the pod may be watched by the time the end is
// evaluated. Called with r.mu held.
func (r *Runtime) logPodEnd(pod *corev1.Pod, end podEnd) {
	ended := make([]string, 0, len(r.watch))
	for token, loc := range r.watch {
		if loc.Sandbox == nil || loc.Sandbox.PodUID != string(pod.UID) {
			continue
		}
		if role, ok := token.Role(); ok {
			ended = append(ended, string(role))
		}
	}
	if len(ended) == 0 {
		return
	}
	slices.Sort(ended)
	write := r.log.Info
	if end.reading == runtime.PodEndNotOurs {
		write = r.log.Warn
	}
	write(podEndLine, "pod", pod.Name, "uid", string(pod.UID), "reading", string(end.reading),
		"cause", end.cause, "reason", end.reason, "ended", ended)
}

// podEndOf is what the daemon read about the end of the pod uid, and whether it read anything at
// all: the reading capturePodEnd kept, unless it has outlived podEndRecordLife, which is answered
// to no one.
//
// An end read neither way is answered to no one either, unless it carries a cause the daemon tells
// in plain words (podEnd.death), which is then answered with its empty reading. Its record exists so
// that the first end event of a pod is the one that decides (capturePodEnd), and it holds no
// reading, so the claims of that pod are evaluated as they were before the reading existed: no
// reading on the observation and nothing added to its detail, and only the cause where there is one.
func (r *Runtime) podEndOf(uid string) (podEnd, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	end, ok := r.podEnds[uid]
	if !ok || (end.reading == "" && end.death.Kind == "") || r.now().Sub(end.at) >= podEndRecordLife {
		return podEnd{}, false
	}
	return end, true
}
