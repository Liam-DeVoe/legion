package buckets

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/sjawhar/envoy/internal/bus"
)

// The writer binaries a container's command runs or a launcher's words name, by the name the writer
// map reads.
const (
	listenerBinary = "envoy-listener"
	dispatchBinary = "envoy-dispatch"
)

// launchMarkers are what a launcher's words hold when they name a writer, and the writer each
// names: envoy-listener, or ENVOY_MACHINE_ID or ENVOY_WEBHOOKS, which a launcher sets for a
// listener it starts, and envoy-dispatch.
var launchMarkers = []struct{ marker, binary string }{
	{listenerBinary, listenerBinary}, {"ENVOY_MACHINE_ID", listenerBinary}, {"ENVOY_WEBHOOKS", listenerBinary}, {dispatchBinary, dispatchBinary},
}

// launch is what a container's command and args start, worked out once for the writer map (writes)
// and for its blockers (launchNote).
type launch struct {
	// binary is the writer binary the command's first word runs, by its base name: listenerBinary
	// for envoy-listener and for the held-port wrapper envoy-listener-heldport, which runs it;
	// dispatchBinary for envoy-dispatch. Its later words and its args are its own flags, so such a
	// command has no launcher.
	binary string
	// launcher is the program that runs the launcher's words: the command's first word, by its base
	// name, or "" for the image's entrypoint when the container sets args alone.
	launcher string
	// names is the writer the launcher's words name (launchMarkers), listenerBinary or
	// dispatchBinary, or "" for neither; marker is what in them names it.
	names, marker string
	// via is the expansion of a variable the pod's env sets whose value alone names the writer;
	// composed says the writer is named in a text the shell makes of the words (readShell).
	via      string
	composed bool
	// unread is the first expansion of a variable the pod's env sets from a Secret, a ConfigMap or an
	// envFrom source, whose value the pod does not show; undecided the first bash-only operator the
	// proof does not evaluate; bounded the bound that stopped the reading. Each may start any writer.
	unread, undecided, bounded string
}

// writesAny reports whether the launch may start any writer: its words expand what the proof cannot
// read, or name a listener, which may write every bucket.
func (l launch) writesAny() bool {
	return l.unread != "" || l.undecided != "" || l.bounded != "" || l.names == listenerBinary
}

// launch reads what c's command and args start, as the kubelet and a shell run them. Each word is
// first expanded as the kubelet expands it (kubeExpand): a $(VAR) reference becomes the value of VAR
// where c's env sets it (environment), and $$ becomes $. A command whose first word is no writer
// binary - a shell, env, tini, dumb-init or any other program, or the image's entrypoint when c
// sets args alone, which Kubernetes appends to it - is a launcher, and its later words and its
// args are what it runs, such as the script of [/bin/sh, -c, 'ENVOY_MACHINE_ID=x exec
// envoy-listener'], the program of [env, ENVOY_MACHINE_ID=x, envoy-listener], or args
// [envoy-listener] under an entrypoint of [tini, --].
//
// The words name a writer (launchMarkers) when a text they can take holds its name: the words as
// written, the value of a variable of c's env they expand, or a text the shell can make of them
// (readShell) - with every value each expansion can take there, so [/bin/sh, -c, 'exec
// "envoy-${ROLE}"'] with ROLE=dispatch names envoy-dispatch, and so does a script whose branch can
// assign it - or a hole in such a text, which stands for what the proof cannot know (a command's
// output, a loop's variable, a value the image's environment may set), beside the text it joins:
// exec envoy-$ROLE names a listener, ROLE unset by the pod. Any word or value holding a writer's
// name counts, so a launcher that only mentions it, such as a probe that reads the listener's URL,
// blocks too: the proof fails closed. A launcher whose program is a hole alone, such as exec
// "$(cat /etc/program)" or exec "$PROGRAM" with PROGRAM the image's, names none: that is the program
// a launcher picks whole from what the pod does not show, which gets past the proof
// (bestEffortWarning).
//
// An expansion of a variable c's env sets but whose value the pod does not show - one from a Secret
// or a ConfigMap, or one an envFrom source may set - is unread, a bash-only operator the reader does
// not evaluate is undecided, and a reading a bound stopped is bounded: each may start any writer, and
// the proof fails closed on it.
func (c podContainer) launch() launch {
	lookup := c.environment()
	var l launch
	var unread []string
	expand := func(words []string) []string {
		expanded := make([]string, len(words))
		for i, word := range words {
			var refs []string
			expanded[i], refs = kubeExpand(word, lookup)
			unread = append(unread, refs...)
		}
		return expanded
	}
	command, words := expand(c.Command), expand(c.Args)
	if len(command) > 0 {
		l.launcher = path.Base(command[0])
		switch l.launcher {
		case listenerBinary, listenerBinary + "-heldport":
			l.binary = listenerBinary
			return l
		case dispatchBinary:
			l.binary = dispatchBinary
			return l
		}
		words = slices.Concat(command[1:], words)
	}
	if len(unread) > 0 {
		l.unread = unread[0]
		return l
	}
	read := readShell(words, lookup, false)
	switch {
	case read.unread != "":
		l.unread = read.unread
		return l
	case read.undecided != "":
		l.undecided = read.undecided
		return l
	case read.bounded != "":
		l.bounded = read.bounded
		return l
	}
	// Each text that may name a writer, in the order a blocker names the first that names the best
	// one (markerRank): the words as written, each value of the pod's env they reference, then each
	// text the shell can make of them.
	type text struct {
		value, via string
		composed   bool
	}
	raw := strings.Join(words, "\n")
	texts := []text{{value: raw}}
	for _, match := range referencePattern.FindAllStringSubmatch(raw, -1) {
		if v := lookup(match[1]); v.set && v.read {
			via := match[0]
			if strings.HasPrefix(via, "${") {
				via += "}"
			}
			texts = append(texts, text{value: v.value, via: via})
		}
	}
	for _, candidate := range read.candidates {
		texts = append(texts, text{value: candidate, composed: true})
	}
	best := 4
	for _, t := range texts {
		rank, binary := markerRank(t.value)
		if rank >= best {
			continue
		}
		best = rank
		l.names, l.via, l.composed = binary, t.via, t.composed
		l.marker = binary
		for _, marker := range launchMarkers {
			if marker.binary == binary && strings.Contains(t.value, marker.marker) {
				l.marker = marker.marker
				break
			}
		}
	}
	return l
}

// variable is a variable of a container as the kubelet sets it (environment): set when c's env or
// envFrom sets it, and read when the pod shows its value, value.
type variable struct {
	value     string
	set, read bool
}

// envFromKey is the key an envFrom source's ConfigMap or Secret may turn into a variable after the
// source's prefix: Kubernetes makes a variable of a key of [-._a-zA-Z0-9]+ alone.
var envFromKey = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)

// envFromNames reports whether an envFrom source of prefix ("" for none) may set the variable name:
// name is prefix and then a key the source may hold (envFromKey). So $(date -u +%s), a command the
// kubelet leaves as it is, names no variable a source sets.
func envFromNames(prefix, name string) bool {
	key, ok := strings.CutPrefix(name, prefix)
	return ok && envFromKey.MatchString(key)
}

// environment is how the kubelet sets c's variables, as far as the pod shows them
// (k8s.io/kubernetes pkg/kubelet/kubelet_pods.go, makeEnvironmentVariables): each envFrom source's
// keys, then each env entry in order, a later one of a name taking its place. An entry's value is
// expanded against the variables set before it (kubeExpand), and read only when every reference it
// holds is; one from a field of the pod is read from the pod (field), and one from a resource is a
// quantity, which names no program. One from a Secret or a ConfigMap, and any name an envFrom
// source may set that no entry sets (envFromNames), is set but not read. Any other name is not set
// by the pod.
func (c podContainer) environment() func(name string) variable {
	vars := map[string]variable{}
	lookup := func(name string) variable {
		if v, ok := vars[name]; ok {
			return v
		}
		for _, source := range c.EnvFrom {
			if envFromNames(source.Prefix, name) {
				return variable{set: true}
			}
		}
		return variable{}
	}
	for _, env := range c.Env {
		v := variable{set: true}
		switch {
		case env.ValueFrom == nil:
			var unread []string
			v.value, unread = kubeExpand(env.Value, lookup)
			v.read = len(unread) == 0
		case env.ValueFrom.FieldRef != nil:
			v.value, v.read = c.field(env.ValueFrom.FieldRef.FieldPath)
		case len(env.ValueFrom.ResourceFieldRef) > 0:
			v.read = true
		}
		vars[env.Name] = v
	}
	return lookup
}

// field is the value the kubelet gives a variable from fieldPath, a field of c's pod (the downward
// API's fields a variable may take): its name, namespace, uid, a label or an annotation, its
// node, its service account, or an address, which names no program. read is false for any other
// path, and for a container read with no pod.
func (c podContainer) field(fieldPath string) (value string, read bool) {
	if c.pod == nil {
		return "", false
	}
	metadata := c.pod.Metadata
	switch fieldPath {
	case "metadata.name":
		return metadata.Name, true
	case "metadata.namespace":
		return metadata.Namespace, true
	case "metadata.uid":
		return metadata.UID, true
	case "spec.nodeName":
		return c.pod.Spec.NodeName, true
	case "spec.serviceAccountName":
		return c.pod.Spec.ServiceAccountName, true
	case "status.hostIP", "status.hostIPs", "status.podIP", "status.podIPs":
		return "", true
	}
	for prefix, values := range map[string]map[string]string{"metadata.labels": metadata.Labels, "metadata.annotations": metadata.Annotations} {
		if key, ok := strings.CutPrefix(fieldPath, prefix+"['"); ok {
			if key, ok = strings.CutSuffix(key, "']"); ok {
				return values[key], true
			}
		}
	}
	return "", false
}

// kubeExpand is word as the kubelet expands a container's command, args and env values
// (k8s.io/kubernetes third_party/forked/golang/expansion, Expand): $$ becomes $, and $(NAME)
// becomes NAME's value where lookup says the pod sets it. A reference to a name the pod does not
// set, and one incomplete, stays as it is. unread is each reference to a variable the pod sets but
// whose value it does not show, which stays as it is too. The loop steps over what each $ starts,
// so it moves its own index.
func kubeExpand(word string, lookup func(name string) variable) (expanded string, unread []string) {
	var out strings.Builder
	for i := 0; i < len(word); i++ {
		if word[i] != '$' || i+1 == len(word) {
			out.WriteByte(word[i])
			continue
		}
		switch word[i+1] {
		case '$':
			out.WriteByte('$')
		case '(':
			end := strings.IndexByte(word[i+2:], ')')
			if end < 0 {
				out.WriteString("$(")
				break
			}
			reference := word[i : i+3+end]
			switch v := lookup(word[i+2 : i+2+end]); {
			case v.set && v.read:
				out.WriteString(v.value)
			case v.set:
				unread = append(unread, reference)
				out.WriteString(reference)
			default:
				out.WriteString(reference)
			}
			i += 2 + end
			continue
		default:
			out.WriteByte('$')
			out.WriteByte(word[i+1])
		}
		i++
	}
	return out.String(), unread
}

// launchNote is what a blocker on c, which writes written of the buckets, adds when its launch is
// what makes c a writer of them (launch): the expansion whose value the pod does not show, the
// operator the proof does not evaluate, a reading a bound stopped, or the writer the launcher's
// words name, where they name it, and the launcher, quoted since a pod's words can hold any bytes,
// and for a listener that the launcher may set the variables the writer map reads, which the pod
// does not show. envoy-dispatch is named only on a blocker of the CI state bucket, the one bucket it
// adds. It is "" for any other container.
func (c podContainer) launchNote(written []string) string {
	started := c.launch()
	through := "the image's entrypoint"
	if started.launcher != "" {
		through = fmt.Sprintf("%q", started.launcher)
	}
	named := started.marker
	switch {
	case started.via != "":
		named = fmt.Sprintf("%s through %q, which the pod's env sets", started.marker, started.via)
	case started.composed:
		named = started.marker + " as the shell reads them"
	}
	switch {
	case started.unread != "":
		return fmt.Sprintf(" (its command or args expand %q, whose value the pod does not show, so it may start any writer)", started.unread)
	case started.undecided != "":
		return fmt.Sprintf(" (its command or args use %q, a bash operator the proof does not evaluate, so it may start any writer)", started.undecided)
	case started.bounded != "":
		return fmt.Sprintf(" (its command or args are more than the proof reads to its end, past its bound on %s, so it may start any writer)", started.bounded)
	case started.names == listenerBinary:
		return fmt.Sprintf(" (its command or args name %s, run through %s, which may set ENVOY_MACHINE_ID and ENVOY_WEBHOOKS where the pod does not show them)", named, through)
	case started.names == dispatchBinary && slices.Contains(written, bus.CIStateBucket):
		return fmt.Sprintf(" (its command or args name %s, run through %s)", named, through)
	}
	return ""
}
