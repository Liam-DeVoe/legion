package buckets

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
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
	// via is the expansion of a variable the pod's env sets whose value alone names the writer, and
	// composed says the writer is named only once the shell joins the words with the values they
	// expand (readShell). Neither is set when a word names it itself.
	via      string
	composed bool
	// unread is the first expansion in the launcher's words, or in the command's first word, of a
	// variable the pod's env sets from a Secret, a ConfigMap or an envFrom source, whose value the
	// pod does not show: the program it starts may be any writer.
	unread string
}

// launch reads what c's command and args start, as the kubelet and a shell run them. Each word is
// first expanded as the kubelet expands it (kubeExpand): a $(VAR) reference becomes the value of VAR
// where c's env sets it (environment), and $$ becomes $. A command whose first word is no writer
// binary - a shell, env, tini, dumb-init or any other program, or the image's entrypoint when c
// sets args alone, which Kubernetes appends to it - is a launcher, and its later words and its
// args are what it runs, such as the script of [/bin/sh, -c, 'ENVOY_MACHINE_ID=x exec
// envoy-listener'], the program of [env, ENVOY_MACHINE_ID=x, envoy-listener], or args
// [envoy-listener] under an entrypoint of [tini, --]. The words name a writer when they hold its
// name (launchMarkers): as written, in the value of a variable of c's env they expand, or as the
// shell reads them (readShell), which joins each word with the values it expands - so
// [/bin/sh, -c, 'exec "envoy-${ROLE}"'] with ROLE=dispatch names envoy-dispatch. Any word or value
// holding a writer's name counts, so a launcher that only mentions it, such as a probe that reads
// the listener's URL, blocks too: the proof fails closed.
//
// An expansion of a variable c's env sets but whose value the pod does not show - one from a
// Secret or a ConfigMap, or one an envFrom source may set - is unread, and may start any writer,
// such as [$(PROGRAM)] or [tini, --] with args [$(PROGRAM)] where PROGRAM comes from a Secret:
// the proof fails closed on it. A variable neither c's env nor the script's own literal assignments
// set - one the image's environment sets, such as TMPDIR, or one the script sets from a command's
// output or a loop - and a glob or bash's ${!REF} are not read, as the image's entrypoint and a
// script file are not: a program a launcher picks through one of those gets past the proof
// (bestEffortWarning).
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
	read := readShell(words, lookup)
	if read.unread != "" {
		l.unread = read.unread
		return l
	}
	// Each text that may name a writer, in the order a blocker names the first that does: the words
	// as written, each value of the pod's env they expand, then the words as the shell reads them.
	type text struct {
		value, via string
		composed   bool
	}
	texts := []text{{value: strings.Join(words, "\n")}}
	for _, value := range read.values {
		texts = append(texts, text{value: value.value, via: value.expansion})
	}
	for _, reading := range read.readings {
		texts = append(texts, text{value: reading, composed: true})
	}
	for _, t := range texts {
		for _, marker := range launchMarkers {
			if strings.Contains(t.value, marker.marker) && (l.names == "" || l.names == dispatchBinary && marker.binary == listenerBinary) {
				l.names, l.marker, l.via, l.composed = marker.binary, marker.marker, t.via, t.composed
			}
		}
	}
	return l
}

// shellReading is a launcher's words as the shell reads them (readShell).
type shellReading struct {
	// values is each variable of the pod's env the words expand, with its value, in their order.
	values []expandedValue
	// readings is the words read once, then that reading read again, until a reading changes
	// nothing.
	readings []string
	// unread is the first expansion of a variable the pod's env sets but does not show.
	unread string
}

// expandedValue is an expansion, as written ($NAME or ${NAME}), and the value it reads.
type expandedValue struct{ expansion, value string }

// The bounds of readShell: at most maxShellReadings readings, and none past a reading of
// maxShellReadingBytes, a value that expands itself.
const (
	maxShellReadings     = 8
	maxShellReadingBytes = 1 << 20
)

// readShell reads words, a launcher's words joined by line breaks, as a POSIX shell reads them
// before it runs a program (shellReader): each $NAME, ${NAME} and ${NAME...} becomes the value the
// pod's env gives NAME (lookup: a literal, or a field of the pod) or, after a literal assignment of
// NAME in the words (NAME=value, export NAME=value, a quoted value, a value its own expansions
// make), that value; $1 to $9, $@ and $* become the words after a -c script; an unset name's
// ${NAME:-word} becomes its word; and quotes and backslashes go. The reading is read again until it
// changes nothing, as an eval would read it, so a value that expands another variable is read too.
// A variable the words give no literal value (from a command's output, $?, a loop), bash's ${!REF}
// and ${#NAME}, and a glob stay as they are written.
func readShell(words []string, lookup func(name string) variable) shellReading {
	var read shellReading
	positional := positionalWords(words)
	text := strings.Join(words, "\n")
	for range maxShellReadings {
		reader := shellReader{lookup: lookup, positional: positional, assigned: map[string]*string{}}
		next := reader.read(text)
		if reader.unread != "" {
			read.unread = reader.unread
			return read
		}
		if read.readings == nil {
			read.values = reader.values
		}
		if next == text {
			break
		}
		read.readings = append(read.readings, next)
		if len(next) > maxShellReadingBytes {
			break
		}
		text = next
	}
	return read
}

// commandOption is a shell's option word that holds -c, such as -c, -ec or -lc: the word after it
// is the script, and those after that its $0, $1 and on.
var commandOption = regexp.MustCompile(`^-[a-zA-Z]*c[a-zA-Z]*$`)

// positionalWords is the shell's $0, $1 and on: the words after the script that follows the first
// word holding -c (commandOption), or none.
func positionalWords(words []string) []string {
	for i, word := range words {
		if commandOption.MatchString(word) && i+1 < len(words) {
			return words[i+2:]
		}
	}
	return nil
}

// commandWords are the shell's words after which a command still starts, where NAME=value assigns.
var commandWords = map[string]bool{"export": true, "readonly": true, "local": true, "declare": true, "typeset": true, "then": true, "do": true, "else": true, "elif": true, "if": true, "while": true, "until": true, "!": true, "time": true}

// shellReader reads one pass of a launcher's words (readShell).
type shellReader struct {
	lookup     func(name string) variable
	positional []string
	// assigned is the value each literal assignment so far gave its name, nil for one the words give
	// a value that is not literal.
	assigned map[string]*string
	values   []expandedValue
	unread   string
	// open says the value of the assignment being read holds something the reader leaves as it is.
	open bool
}

// read is text as the shell reads it, from the start to the end: each expansion replaced (expand),
// quotes and backslashes taken out, and each literal assignment recorded where it comes, so an
// expansion after it reads its value.
func (r *shellReader) read(text string) string {
	var out strings.Builder
	var single, double bool
	start := true
	assigning, from := "", 0
	end := func() {
		if assigning == "" {
			return
		}
		if r.open {
			r.assigned[assigning] = nil
		} else {
			value := out.String()[from:]
			r.assigned[assigning] = &value
		}
		assigning, start = "", true
	}
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case single:
			if c == '\'' {
				single = false
			} else {
				out.WriteByte(c)
			}
			i++
		case c == '\\':
			if i+1 < len(text) {
				out.WriteByte(text[i+1])
			}
			i += 2
		case c == '\'' && !double:
			single = true
			i++
		case c == '"':
			double = !double
			i++
		case c == '$':
			i += r.expand(text[i:], &out)
		case double:
			out.WriteByte(c)
			i++
		case c == ' ' || c == '\t':
			end()
			out.WriteByte(c)
			i++
			continue
		case strings.IndexByte(";&|\n(){}`", c) >= 0:
			if c == '`' {
				r.open = true
			}
			end()
			out.WriteByte(c)
			start = true
			i++
			continue
		case start && assigning == "" && isNameStart(c):
			j := i + 1
			for j < len(text) && isNameChar(text[j]) {
				j++
			}
			word := text[i:j]
			out.WriteString(word)
			if j < len(text) && text[j] == '=' {
				out.WriteByte('=')
				assigning, from, r.open = word, out.Len(), false
				i = j + 1
				continue
			}
			i = j
			start = commandWords[word]
			continue
		default:
			out.WriteByte(c)
			i++
		}
		if assigning == "" {
			start = false
		}
	}
	end()
	return out.String()
}

// expand writes what the expansion text starts with reads as, and returns how many bytes of text it
// took: $NAME, ${NAME...}, $0 to $9, $@ and $*. A command substitution, $(...) or $((...)), and $?,
// $$, $#, $! and $- are written as they are.
func (r *shellReader) expand(text string, out *strings.Builder) int {
	if len(text) == 1 {
		out.WriteByte('$')
		return 1
	}
	switch c := text[1]; {
	case c == '{':
		end := closingBrace(text, 1)
		if end < 0 {
			out.WriteString("${")
			r.open = true
			return 2
		}
		r.braced(text[2:end], text[:end+1], out)
		return end + 1
	case isNameStart(c):
		j := 2
		for j < len(text) && isNameChar(text[j]) {
			j++
		}
		r.substitute(text[1:j], text[:j], "", "", text[:j], out)
		return j
	case c >= '0' && c <= '9':
		r.substitute(text[1:2], text[:2], "", "", text[:2], out)
		return 2
	case c == '@' || c == '*':
		if len(r.positional) > 1 {
			out.WriteString(strings.Join(r.positional[1:], " "))
		} else {
			out.WriteString(text[:2])
			r.open = true
		}
		return 2
	default:
		out.WriteString(text[:2])
		r.open = true
		return 2
	}
}

// parameterOperators are the operators of ${NAME<operator>word} that choose between NAME's value
// and word, longest first.
var parameterOperators = []string{":-", ":=", ":?", ":+", "-", "=", "?", "+"}

// braced writes what ${inner} reads as; whole is the expansion as written. ${!REF} and ${#NAME} are
// written as they are. Any other operator than parameterOperators (a trimmed prefix or suffix, a
// substring) reads as the whole value, which holds whatever it would leave.
func (r *shellReader) braced(inner, whole string, out *strings.Builder) {
	if strings.HasPrefix(inner, "!") || strings.HasPrefix(inner, "#") && len(inner) > 1 {
		out.WriteString(whole)
		r.open = true
		return
	}
	name := inner
	if len(inner) > 0 && inner[0] >= '0' && inner[0] <= '9' {
		name = inner[:len(inner)-len(strings.TrimLeft(inner, "0123456789"))]
	} else if len(inner) > 0 && isNameStart(inner[0]) {
		j := 1
		for j < len(inner) && isNameChar(inner[j]) {
			j++
		}
		name = inner[:j]
	}
	operator, word := "", ""
	for _, op := range parameterOperators {
		if rest, ok := strings.CutPrefix(inner[len(name):], op); ok {
			operator, word = op, rest
			break
		}
	}
	r.substitute(name, "${"+name+"}", operator, word, whole, out)
}

// substitute writes the value of the variable name, expanded as display (for a blocker) and written
// as whole, under operator and its word: a positional parameter's word, the value of a literal
// assignment of name before it, or the value the pod's env gives it. Every expansion of a variable
// the pod's env sets but does not show is unread. An unset name's ${NAME:-word} and ${NAME-word}
// read as word, which the image's environment may override, and any other expansion of an unset
// name is written as it is.
func (r *shellReader) substitute(name, display, operator, word, whole string, out *strings.Builder) {
	value, known, fromPod := "", false, false
	if index, err := strconv.Atoi(name); err == nil {
		if index < len(r.positional) {
			value, known = r.positional[index], true
		}
	} else if assigned, ok := r.assigned[name]; ok {
		if assigned != nil {
			value, known = *assigned, true
		}
	} else if v := r.lookup(name); v.set && !v.read {
		if r.unread == "" {
			r.unread = display
		}
		out.WriteString(whole)
		r.open = true
		return
	} else if v.set {
		value, known, fromPod = v.value, true, true
	}
	writeWord := func() {
		out.WriteString(word)
		if strings.ContainsAny(word, "$`") {
			r.open = true
		}
	}
	switch {
	case known && (operator == ":-" || operator == ":=") && value == "":
		writeWord()
	case known && (operator == ":+" && value != "" || operator == "+"):
		writeWord()
	case known && (operator == ":+" || operator == "+"):
	case known:
		out.WriteString(value)
		if fromPod {
			r.values = append(r.values, expandedValue{display, value})
		}
	case operator == ":-" || operator == "-" || operator == ":=" || operator == "=":
		writeWord()
		r.open = true
	case operator == ":+" || operator == "+":
		r.open = true
	default:
		out.WriteString(whole)
		r.open = true
	}
}

// closingBrace is the index of the brace that closes the one at open in text, or -1.
func closingBrace(text string, open int) int {
	depth := 0
	for k := open; k < len(text); k++ {
		switch text[k] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return k
			}
		}
	}
	return -1
}

func isNameStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isNameChar(c byte) bool { return isNameStart(c) || c >= '0' && c <= '9' }

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
// what makes c a writer of them (launch): the expansion whose value the pod does not show, or the
// writer the launcher's words name, where they name it, and the launcher, quoted since a pod's
// words can hold any bytes, and for a listener that the launcher may set the variables the writer
// map reads, which the pod does not show. envoy-dispatch is named only on a blocker of the CI state
// bucket, the one bucket it adds. It is "" for any other container.
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
	case started.names == listenerBinary:
		return fmt.Sprintf(" (its command or args name %s, run through %s, which may set ENVOY_MACHINE_ID and ENVOY_WEBHOOKS where the pod does not show them)", named, through)
	case started.names == dispatchBinary && slices.Contains(written, bus.CIStateBucket):
		return fmt.Sprintf(" (its command or args name %s, run through %s)", named, through)
	}
	return ""
}
