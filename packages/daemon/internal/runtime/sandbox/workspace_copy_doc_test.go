package sandbox

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/testdoc"
)

// What an operator does when an issue pod goes away is decided by three facts this release
// changed, and docs/kubernetes.md is where that operator reads them: which pods carry the
// extended-run-time annotation, what the daemon keeps of a workspace outside its pod, and which
// pod ends are continued uncharged. A document that still states the annotation as unconditional,
// or that names the copy without its period, sends the operator to look for a node of its own
// that no longer exists, or to wait for work that no copy holds.
//
// These checks do not read the guide. They read workspace_copy_doc_extract.txt, this package's
// own copy of the seven regions in the guide's bytes, which internal/textcheck's
// TestTheWorkspaceCopyDocExtractsAreTheGuidesOwnLines holds to the guide's lines between the same
// markers. The split is what keeps docs/kubernetes.md out of this job's filter: a declared read
// applies to every compiled source file of a run, so a test that reads the guide starts the whole
// daemon Go lane on a documentation-only edit. The copy names each region by the two markers
// below, and internal/testdoc fails a check whose markers name no region of it, so a reworded
// paragraph is its markers changed here, in the copy and in the guide in one commit. The AWS
// paragraph's start marker is built from doNotDisruptAnnotation, so a changed annotation key is
// such a change too.
//
// Each check reads one region, bounded by its first and last words, and fails by naming the
// marker it could not find. That bound is the whole structure. The reclaim paragraph's own checks
// (reclaim_doc_test.go) were written after an earlier version of them passed over a paragraph that
// was wrong, because a word it searched for happened to appear elsewhere in a document of
// thousands of lines; a search of the whole guide for "no death charged" would pass here the same
// way, with the annotation bullet saying the opposite of what it says now.
//
// Where a region is a list, it is read as a list: the two readings that earn an uncharged
// continue are two bullets, each held to the facts of its own bullet. A fact proved from the
// region's flat text alone would let the deletion-timestamp reading borrow the cluster's three
// reasons.
//
// The annotation keys, the three cluster reasons and the reason that needs the condition come
// from the code they document (safeToEvictAnnotation, doNotDisruptAnnotation, podShapeAnnotation,
// clusterEndedReasons, evictedReason), never from a list written beside the check: a reason added
// to the set is then a reason the document must name, and nobody has to remember to edit this
// file. The paragraphs that state internal/daemon's constants — the copy's cadence, with the
// running copy's 5-minute period and the line a copy that was not stored writes, and what each
// kind of pod end loses and keeps, whose bullet for an end the cluster made states that period —
// are checked in that package (its workspace_copy_doc_test.go), which reads those two facts from
// the constants themselves.
//
// What these checks cannot do: they read what the document says, never whether it is true. A
// reader is still the only check on that.
const (
	// workspaceCopyDocExtract is read from this package's own directory, where the file sits
	// beside the code it is about and needs no declared read of its own.
	workspaceCopyDocExtract = "workspace_copy_doc_extract.txt"

	microVMAnnotationsStart = "A microVM issue pod carries the annotations a gVisor issue pod carries"
	microVMAnnotationsEnd   = "as a `Burstable` gVisor pod carries none"

	annotationRuleStart = "the project controller's pod additionally carries"
	annotationRuleEnd   = "with no death charged"

	burstAnnotationStart = "An issue pod carries `cluster-autoscaler.kubernetes.io/safe-to-evict: \"false\"` only while"
	burstAnnotationEnd   = "rather than into a node of its own."

	copyKindsStart = "An issue therefore holds up to two kinds of copy"
	copyKindsEnd   = "and both stay through a linger and a park."

	podEndReadingsStart = "The daemon reads what ended each watched pod once"
	podEndReadingsEnd   = "would be relaunched for ever."

	karpenterStart = "`" + doNotDisruptAnnotation + "` is the AWS scheduler's annotation"
	karpenterEnd   = "and no repacking of its pods."

	controllerAnnotationsStart = "Legion sets no affinity on it, so under Karpenter"
	controllerAnnotationsEnd   = "where an issue pod carries the second only while its reservation is `Guaranteed`"
)

// The issue pod carries cluster-autoscaler.kubernetes.io/safe-to-evict only while its reservation
// is Guaranteed (podKind.asksExtendedRunTime), and the "Anatomy of a Sandbox pod" bullet is where
// an operator reads that rule and what a Burstable issue pod gives up for it.
func TestKubernetesDocStatesWhichPodsCarryTheExtendedRunTimeAnnotation(t *testing.T) {
	const what = `docs/kubernetes.md's annotation bullet of "Anatomy of a Sandbox pod"`
	_, region := testdoc.Region(t, workspaceCopyDocExtract, what, annotationRuleStart, annotationRuleEnd)
	testdoc.RequireDocFacts(t, what, region, []testdoc.Fact{
		{Fact: "the annotation the rule is about", Phrase: safeToEvictAnnotation},
		{Fact: "that the controller's pod carries it whatever its reservation", Phrase: "whatever its reservation"},
		{Fact: "the rule itself, which is the reservation", Phrase: "an issue pod carries it only while its cpu request equals its cpu limit and its memory request equals its memory limit"},
		{Fact: "the reservation that rule names", Phrase: "`Guaranteed`"},
		{Fact: "that a Burstable issue pod carries it not at all", Phrase: "is `Burstable` and carries the annotation not at all"},
		{Fact: "what such a pod asks GKE Autopilot for", Phrase: "no extended run time"},
		{Fact: "that such a pod shares a node", Phrase: "shares a node"},
		{Fact: "what such a pod gives up", Phrase: "the seven-day protection"},
		{Fact: "what such a pod keeps, and why", Phrase: "copy of each issue workspace outside the pod"},
	})
}

// The guide says when an issue pod carries cluster-autoscaler.kubernetes.io/safe-to-evict in three
// places besides the "Anatomy of a Sandbox pod" bullet: the stop-eviction annotations of a microVM
// issue pod, the burst paragraph of "Issue sizing: one reservation per pod", and the controller
// pod's paragraph, which sets the issue pod's rule beside the controller's. Each must state the
// rule the code applies (podKind.asksExtendedRunTime): only while the pod's reservation is
// Guaranteed, whatever its runtime. A microVM paragraph that says the pod carries the annotation
// unconditionally sends an operator who lowered an issue request to look for a node of its own
// that the pod no longer asks for.
//
// "Rolling back the running copy" also names the annotation on every issue pod whatever its
// reservation, and is no region here: that sentence says what an earlier daemon release does, so
// this release's code is not what it answers to.
func TestKubernetesDocStatesTheSafeToEvictRuleWhereverElseItNamesIt(t *testing.T) {
	const rule = "only while its reservation is `Guaranteed`"
	safeToEvict := "`" + safeToEvictAnnotation + `: "false"` + "`"
	for _, tc := range []struct {
		name, what string
		start, end string
		facts      []testdoc.Fact
	}{
		{
			name: "the microVM issue pod", what: `docs/kubernetes.md's stop-eviction annotations of "Issue pods in microVMs"`,
			start: microVMAnnotationsStart, end: microVMAnnotationsEnd,
			facts: []testdoc.Fact{
				{Fact: "that a microVM issue pod takes a gVisor issue pod's rule", Phrase: "by the same rule"},
				{Fact: "the annotations every such pod carries",
					Phrase: "`" + doNotDisruptAnnotation + `: "true"` + "` and `" + podShapeAnnotation + "` always"},
				{Fact: "the rule for the annotation the paragraph is about", Phrase: safeToEvict + " " + rule},
				{Fact: "that the rule reads the reservation and not the runtime", Phrase: "reads the pod's reservation and never its runtime"},
				{Fact: "that a Burstable microVM pod carries none", Phrase: "a `Burstable` microVM pod carries no safe-to-evict annotation"},
			},
		},
		{
			name: "the burst paragraph", what: `docs/kubernetes.md's burst paragraph of "Issue sizing: one reservation per pod"`,
			start: burstAnnotationStart, end: burstAnnotationEnd,
			facts: []testdoc.Fact{
				{Fact: "the rule for the annotation the paragraph is about", Phrase: safeToEvict + " " + rule},
				{Fact: "that the controller's pod carries it whatever its reservation", Phrase: "the controller's pod carries it whatever its reservation"},
				{Fact: "that a Burstable issue pod carries it not at all", Phrase: "A `Burstable` issue pod carries it not at all"},
				{Fact: "what such a pod asks GKE Autopilot for", Phrase: "asks for no extended run time"},
			},
		},
		{
			name: "the controller's pod", what: `docs/kubernetes.md's paragraph of the controller's pod in "Daemon-launched controller"`,
			start: controllerAnnotationsStart, end: controllerAnnotationsEnd,
			facts: []testdoc.Fact{
				{Fact: "the two annotations the controller's pod carries whatever its reservation",
					Phrase: "`" + doNotDisruptAnnotation + `: "true"` + "` and " + safeToEvict + ", both whatever its reservation"},
				{Fact: "that a Burstable controller's pod keeps both", Phrase: "takes neither annotation away"},
				{Fact: "the issue pod's rule beside it", Phrase: "an issue pod carries the second " + rule},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, region := testdoc.Region(t, workspaceCopyDocExtract, tc.what, tc.start, tc.end)
			testdoc.RequireDocFacts(t, tc.what, region, tc.facts)
		})
	}
}

// The two kinds of copy: which a new pod restores, what becomes of the older, which of the two
// holds a pod, and how the operator's psql recipe reads the running copy's own table.
func TestKubernetesDocStatesTheTwoKindsOfWorkspaceCopy(t *testing.T) {
	const what = `docs/kubernetes.md's two-kinds paragraph of "The copy of a running pod's workspace"`
	_, region := testdoc.Region(t, workspaceCopyDocExtract, what, copyKindsStart, copyKindsEnd)
	testdoc.RequireDocFacts(t, what, region, []testdoc.Fact{
		{Fact: "the release bundle's table", Phrase: "workspace_bundles"},
		{Fact: "the running copy's table", Phrase: "workspace_running_copies"},
		{Fact: "which copy a new pod restores", Phrase: "is sent the newer of the two"},
		{Fact: "that the restore happens before any role starts", Phrase: "before any role starts"},
		{Fact: "what becomes of the older copy", Phrase: "The older is closed unsent"},
		{Fact: "that an unrestored release bundle holds the issue's pod", Phrase: "a release bundle no pod has restored holds the issue's pod"},
		{Fact: "that a running copy holds no pod", Phrase: "a running copy holds no pod at all"},
		{Fact: "that an issue holding a running copy is still released when it is idle", Phrase: "released when it is idle"},
		{Fact: "the psql recipe for the running copy's table", Phrase: "select encode(bundle, 'base64') from workspace_running_copies"},
		{Fact: "that the column is bundle there too", Phrase: "the column is `bundle` there too"},
	})
}

// The two readings that earn an uncharged continue, the end reasons the second covers, the reason
// that needs the condition, that no count of such ends is kept however often they come, and the
// ends that are still charged as a death, the pod's own memory use under node pressure among them
// though it carries the condition, and the daemon's own end whatever the kubelet wrote.
func TestKubernetesDocStatesWhichPodEndsAreContinuedUnchargedAndWhichIsCharged(t *testing.T) {
	const what = `docs/kubernetes.md's "An end from outside the pod" paragraph`
	lines, region := testdoc.Region(t, workspaceCopyDocExtract, what, podEndReadingsStart, podEndReadingsEnd)
	testdoc.RequireDocFacts(t, what, region, []testdoc.Fact{
		{Fact: "the line one pod end writes", Phrase: podEndLine},
		{Fact: "that each role is continued in its own session", Phrase: "in that role's own session"},
		{Fact: "that neither a death nor a launch failure is charged", Phrase: "charges no death and no launch failure"},
		{Fact: "that each role is sent the task it owed again", Phrase: "sends each role the task it owed again"},
		{Fact: "that no run of such ends is ever charged", Phrase: "costs its roles nothing, however often it comes"},
		{Fact: "that no count of such ends is kept", Phrase: "The daemon keeps no count of these ends"},
		{Fact: "that the ends of a start are uncharged too", Phrase: "whether its agent was ready or still starting"},
		{Fact: "the end that is still charged", Phrase: "An end of neither reading is charged as a death"},
		{Fact: "that the kubelet's eviction with no condition is charged", Phrase: "`" + evictedReason + "` with no `DisruptionTarget` condition is such an end"},
		{Fact: "the three limits that eviction is for", Phrase: "its own ephemeral-storage limit, a container's ephemeral-storage limit or an `emptyDir`'s `sizeLimit`"},
		{Fact: "a reason outside the three, which is charged", Phrase: "UnexpectedAdmissionError"},
		{Fact: "that an eviction naming a container of the pod over its memory request is charged",
			Phrase: "An eviction for node memory pressure whose kubelet message names a container of the pod using more memory than it requested is such an end too"},
		{Fact: "the sentence of the kubelet's message that names such a container", Phrase: "has larger consumption of memory."},
		{Fact: "that a message naming no container of the pod is a neighbour's, which is continued",
			Phrase: "names no container of the pod's own spec is a neighbour's"},
		{Fact: "that an end the daemon itself started stays its own", Phrase: "An end the daemon itself started is its own whatever the pod's status says"},
	})

	bullets := testdoc.RegionBullets(lines)
	if len(bullets) != 2 {
		t.Fatalf("%s lists %d readings, want the 2 that earn an uncharged continue: %q", what, len(bullets), bullets)
	}
	if deletion, ok := testdoc.BulletNaming(t, what, bullets, "deletion timestamp"); ok {
		testdoc.RequireDocFacts(t, what+", its deletion-timestamp bullet", deletion, []testdoc.Fact{
			{Fact: "that no act of the daemon's own accounts for the deletion", Phrase: "no act of the daemon's own accounts for"},
		})
	}
	cluster, ok := testdoc.BulletNaming(t, what, bullets, "DisruptionTarget")
	if !ok {
		return
	}
	// The reasons come from the code this bullet documents, so a reason added to the set is a
	// reason the document must name from that moment, with nobody reminded to edit either file.
	for _, reason := range clusterEndedReasons {
		if !strings.Contains(cluster, "`"+reason+"`") {
			t.Errorf("%s's bullet for what the cluster says ended a pod does not name the reason %q, which is one of the "+
				"reasons that earn an uncharged continue (clusterEndedReasons): an operator reading that reason in a pod's "+
				"status is left to guess whether the roles were charged for it. The bullet is: %q", what, reason, cluster)
		}
	}
	testdoc.RequireDocFacts(t, what+", its DisruptionTarget bullet", cluster, []testdoc.Fact{
		{Fact: "that the kubelet's eviction reason earns the continue only with the condition",
			Phrase: "`" + evictedReason + "` is such an end only with the condition"},
		{Fact: "that the pod's own memory use earns no continue, with the condition or without",
			Phrase: "is not such an end, though it carries the condition"},
	})
}

// Karpenter's annotation is unchanged by this release, which is what tells an AWS deployment that
// it gains the copy and the uncharged continue and pays no repacking for them.
func TestKubernetesDocStatesKarpentersAnnotationStaysOnEveryPod(t *testing.T) {
	const what = `docs/kubernetes.md's AWS paragraph of "The copy of a running pod's workspace"`
	_, region := testdoc.Region(t, workspaceCopyDocExtract, what, karpenterStart, karpenterEnd)
	testdoc.RequireDocFacts(t, what, region, []testdoc.Fact{
		{Fact: "the annotation that stays", Phrase: doNotDisruptAnnotation},
		{Fact: "that every pod carries it whatever its reservation", Phrase: "every Legion pod carries it whatever its reservation"},
		{Fact: "that Karpenter disrupts no node a Legion pod runs on", Phrase: "no node a Legion pod runs on"},
		{Fact: "what an AWS deployment gains", Phrase: "gains the copy and the uncharged resume"},
		{Fact: "what it does not pay for", Phrase: "no repacking"},
	})
}
