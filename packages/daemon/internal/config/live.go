package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// The two sources a settings change comes from.
const (
	// SourceFile is the file changing under the daemon: the file is read again and the stored
	// route overlay is kept, so a route change a file edit does not mention still stands.
	SourceFile = "file"
	// SourceRoute is an operator's or Dispatch's call: the requested paths are merged into the
	// stored overlay, which from then on wins over the file's own value for those paths.
	SourceRoute = "route"
)

// The two values a Change renders in place of one it may not print.
const (
	// withheldValue stands for a credential, the source of one, the path of a file holding one,
	// or an address whose userinfo or query may carry one (Row.Secret).
	withheldValue = "(withheld)"
	// UnsetValue stands for a path the configuration does not carry: a block the file names under
	// one runtime and not the other, or an optional block it drops. A Change reads the reading the
	// loader resolved, before the pin of the fixed paths (Apply), so a dropped block's members read
	// as unset whether or not the snapshot carries the block for a fixed member of it (pinFixed).
	// A keyed map is the one value read after the pin instead (Apply), so a member of an entry the
	// pin carries reads as the zero value the daemon holds there, never as unset.
	// A drift reason says the same word of the same situation (runtime/sandbox's
	// launchSettingsDrift), so the settings route's answer and a pod's drift reason never
	// disagree on it.
	UnsetValue = "(unset)"
)

// Change is one setting the daemon applied, as a log line and an answer to the caller read it.
type Change struct {
	// Path is the dotted path as legion.yaml writes it.
	Path string
	// Old and New are the values as a log line shows them, "(withheld)" for a secret and
	// "(unset)" for a path the configuration did not carry.
	Old string
	New string
	// Class is what the daemon can do with the new value, and Outcome what that means for the
	// caller: at-once, rebuilt, or next-launch.
	Class   Class
	Outcome Outcome
	// Holder is what the daemon must build again, "" when nothing holds the value.
	Holder string
	// Source is SourceFile or SourceRoute, and By whoever asked for it ("" for the file).
	Source string
	By     string
}

// Refusal is one requested path the daemon would not change, and why.
type Refusal struct {
	Path   string
	Reason string
}

// OtherRuntime is the one refusal of a whole reading: the configuration file under the daemon
// names a runtime other than the one it booted on, so nothing of that reading settles — no
// snapshot, no overlay, no observer — whichever arm asked (Live.Apply). Apply answers it as its
// error, so every caller's `err != nil` arm, which already means nothing settled, is the arm
// that meets it, and the refusals it answers beside it are the paths that were refused on their
// own account before the reading was read at all.
//
// Error is the reason a person meets, and carries the whole of what one needs: the key that
// named the runtime, both runtime names — a reader of one key's verdict cannot otherwise tell
// which deployment the file has become — and the two ways out, the file naming the booted
// runtime again or a restart onto the one the file names.
type OtherRuntime struct {
	// Path is the `legion.yaml` key the reading turned on, which is `runtime`. A caller that
	// named no such key is answered for it all the same, since it is the key that refused
	// every other (api's settingResults).
	Path string
	// File is the runtime the file names, Booted the one this daemon is running.
	File   string
	Booted string
}

func (e *OtherRuntime) Error() string {
	return fmt.Sprintf("the configuration file under the daemon names %s %q, not the %q it booted on: no setting changes until the file names %q again or the daemon restarts",
		e.Path, e.File, e.Booted, e.Booted)
}

// NotRecorded is a change the daemon applied and could not store: it is running on the new
// value, and nothing keeps it. Its next restart reads what the store still holds for those
// paths — the file's own value for a path the change laid over it, since nothing recorded the
// change, and the kept row for a path the change withdrew, since the store would not drop it
// (internal/settings' Keeper.Route, and Keeper.File for the rows a file settle took back). The
// caller is answered what happened — the change took effect, and nothing keeps it — rather than
// a refusal of a change the daemon made.
//
// It is declared here, beside Change and Refusal, because it is a third thing an applied change
// can be and both sides of the route read it: internal/settings returns it from the keeper's
// route and file arms, and internal/api answers for it (api's settingsChange), which otherwise
// would import internal/settings for this error alone.
type NotRecorded struct {
	Paths []string
	Err   error
}

func (e *NotRecorded) Error() string {
	return fmt.Sprintf("the daemon applied the change of %s and could not record it: %v", strings.Join(e.Paths, ", "), e.Err)
}

func (e *NotRecorded) Unwrap() error { return e.Err }

// Live is the daemon's one settings holder: the configuration file, the route overlay laid over
// it, and the resolved Config every reader takes a snapshot of.
//
// Every change goes through Apply, whatever asked for it, and every Apply resolves the whole
// document through the loader, so a route change meets exactly the refusals a file change meets
// and no second resolution path exists. The resolved Config sits behind an atomic pointer: a
// reader never blocks, and a reader that took a snapshot keeps reading one configuration for the
// whole of its decision.
type Live struct {
	path string
	env  func(string) string
	// settled is the resolved configuration, never nil once the holder is built.
	settled atomic.Pointer[Config]
	// reading is the settings of the reading settled was pinned from, as the loader resolved it
	// and before pinFixed put the boot's fixed values back. Apply diffs the next reading against
	// it, so a change is what moved between two readings and never a value the pin wrote: a
	// block the pin carries for a fixed member of it holds every other member at a zero value no
	// reading wrote, which a diff of settled would read as configured. A keyed map and its members
	// are the exception, read as settled holds them (Apply): the pin writes a fixed member back
	// into a map that is not itself fixed, so the map the reading resolved is not the one the
	// daemon runs on.
	reading map[string]setting
	// normalize is the caller's own fix of what the file says, run over every resolution before
	// it settles (NewLive); nil fixes nothing.
	normalize func(*Config) error
	// mu orders the applies against each other and guards overlay, reading and observers. An
	// observer runs under it, in registration order, so no observer may call Apply, Snapshot
	// excepted.
	mu sync.Mutex
	// overlay is the stored route overlay, dotted path to the value the route asked for. The
	// caller seeds it from its durable store; Apply keeps it current.
	overlay map[string]any
	// pinned is the paths this daemon refuses beyond the class table's fixed rows, each to the
	// reason it gives: a rebuild key whose holder this daemon built nothing for at boot, so a
	// change of it reaches nothing until a restart (RefusePath). It is the per-daemon twin of a
	// ClassFixed row, which the class table — one table for every daemon — cannot carry. Guarded
	// by mu, like overlay; the daemon seeds it at boot, before the API serves.
	pinned    map[string]string
	observers []func(cfg *Config, applied []Change)
}

// NewLive resolves path once and holds the result.
//
// env is the daemon's environment lookup, as Load takes it. normalize is the caller's own fix of
// what the file says — `legion start`'s absolute state_dir and the plugin pair its binary links
// — which every resolution runs over the loaded configuration before it settles, so a file read
// again under a running daemon settles exactly as this read did; nil fixes nothing. It must
// decide from the configuration alone, since every later resolution runs it again, and a
// resolution whose normalize fails changes nothing.
//
// The holder is built with no route overlay over it: the route changes a restart keeps are laid
// back over the file by the boot's own restore, through the route arm every other change takes
// (internal/settings' Keeper.Restore), so the file is read once here and the daemon resolves it
// a second time only when its store holds a change.
func NewLive(path string, env func(string) string, normalize func(*Config) error) (*Live, error) {
	live := &Live{path: path, env: env, normalize: normalize, overlay: map[string]any{}}
	cfg, err := live.resolve(live.overlay)
	if err != nil {
		return nil, err
	}
	live.settled.Store(&cfg)
	live.reading = settingsOf(&cfg)
	return live, nil
}

// Settled is the holder of one configuration the caller resolved itself, with no file under it:
// the daemon's own tests, which build a Config in memory rather than reading legion.yaml.
// Snapshot answers it, no observer is ever called, and Apply refuses by name — nothing here reads
// a file, so it is no second way to resolve one.
func Settled(cfg Config) *Live {
	live := &Live{overlay: map[string]any{}}
	live.settled.Store(&cfg)
	live.reading = settingsOf(&cfg)
	return live
}

// Snapshot is the configuration the daemon is running on, never nil.
func (l *Live) Snapshot() *Config { return l.settled.Load() }

// WhileSettled runs fn under the holder's lock, with the configuration the daemon is running
// on: work of the caller's own that must not run beside an Apply, because it touches the very
// things an apply's observers rebuild. The daemon's reread of its credential files is that work
// (internal/daemon's rereadCredentials): a file rotated in place names no changed setting, so
// nothing of it reaches an observer, and a reread that ran outside this lock would rebuild a
// holder a route change was rebuilding at the same moment.
//
// fn reads the configuration it is handed and must not call Apply, OnChange or WhileSettled.
func (l *Live) WhileSettled(fn func(cfg *Config)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fn(l.settled.Load())
}

// Path is the configuration file this holder reads, "" for a holder built from a configuration
// value (Settled). The file watcher watches the directory holding it (internal/settings).
func (l *Live) Path() string { return l.path }

// FileValues is what the file itself says about each of paths, with no overlay over it: the
// value as written, encoded canonically, and "" for a path the file does not carry. It parses
// the document and reads nothing else — no credential file, no App private key command — so a
// caller may ask it as often as a file changes.
//
// It is how the daemon tells a file that has changed a key from one that has not, which no
// resolved configuration can say once a route change of that key is laid over it: the store of
// route changes keeps the digest of the file's own value at the moment each change was applied,
// and a path whose file value has moved since is the file taking its key back
// (internal/settings).
func (l *Live) FileValues(paths []string) (map[string]string, error) {
	if l.path == "" {
		return nil, errors.New("this settings holder was built from a configuration value (config.Settled) and has no file to read")
	}
	document, err := readDocument(l.path)
	if err != nil {
		return nil, err
	}
	root, err := rootMapping(document)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(paths))
	for _, path := range paths {
		node := root
		for _, name := range strings.Split(path, ".") {
			if node == nil || node.Kind != yaml.MappingNode {
				node = nil
				break
			}
			node = mappingMember(node, name)
		}
		if node == nil || node.Tag == "!!null" {
			values[path] = ""
			continue
		}
		var value any
		if err := node.Decode(&value); err != nil {
			return nil, fmt.Errorf("read the file's own %s: %w", path, err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("read the file's own %s: %w", path, err)
		}
		values[path] = string(encoded)
	}
	return values, nil
}

// OnChange registers an observer of every applied change. Apply calls each one after it has
// swapped the pointer, in registration order, so an observer reads the new configuration from its
// argument or from Snapshot and rebuilds what the changes name. An observer runs under the
// holder's lock: it must not call Apply or OnChange.
func (l *Live) OnChange(fn func(cfg *Config, applied []Change)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.observers = append(l.observers, fn)
}

// RefusePath records that this daemon refuses every change of path, by name, with reason,
// whatever the path's class in the table: a rebuild key whose holder this daemon built nothing
// for at boot, so a change of it reaches nothing until a restart. It is the per-daemon twin of
// a ClassFixed row, which the class table cannot carry because the table is one table for every
// daemon and this refusal holds only for the daemon that booted without the holder. The route
// refuses the path by name before it resolves, and the file arm pins it back to the value boot
// settled, as the fixed pins are (Apply, pinDaemon). The daemon seeds it at boot, before the API
// serves, so no apply runs beside this.
func (l *Live) RefusePath(path, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pinned == nil {
		l.pinned = map[string]string{}
	}
	l.pinned[path] = reason
}

// Apply settles one settings change and reports what it did.
//
// source is SourceFile, where the file is read again under the stored route overlay and overlay
// names the paths the file has taken back — each mapped to nil, dropped from the stored overlay
// so the file's own value settles again (internal/settings reads the file's own values and
// decides which those are); a value there is the route's business and is refused. Or source is
// SourceRoute, where overlay is the requested dotted path to value changes: a path the class
// table does not hold is refused as an unknown setting, a fixed path is refused by name with its
// reason, a path mapped to nil is withdrawn from the overlay, and the rest are merged into it.
// by is whoever asked, which every Change carries.
//
// The merged overlay is laid over the file's own document and resolved through the loader. A
// resolution that fails changes nothing: the error comes back with the pointer and the stored
// overlay as they were.
//
// Every resolution reads the whole file again, whichever arm asked, so what it settles for the
// fixed paths is pinned back to the value boot settled (pinFixed): no snapshot ever carries a
// fixed value the daemon is not running on, and the class table's promise that a change of such
// a key reaches the next boot is true of the snapshot itself rather than of each reader's
// discipline. The file arm names every path it pinned in refused, with that path's own reason,
// since the file is where such an edit comes from. The route arm names only what its caller
// asked for: a fixed path it refuses before resolving, and a fixed member of a keyed map the
// caller set whole, which the pin takes back and askedFor names, since the change of the map
// the caller reads carries the value the daemon holds and would otherwise not say which member
// of the request it left out, or why. A path pinned under neither is a file edit this
// resolution read beside the request, which the file arm reports at the settle the watcher
// makes of it.
//
// A reading that names a runtime other than the one the daemon booted on settles nothing at
// all, whichever arm asked: both arms answer *OtherRuntime, with the paths refused on their own
// account beside it, and the snapshot and the stored overlay stand exactly as they were. The
// loader's cross-key checks are the checks of the runtime the reading names: `checkPodReachable`
// and the `worker_stop_timeout_seconds` floor at shim.MinStopGrace run under `kubernetes`
// alone, and `daemon_url` is required there and optional under tmux (resolve). So every other
// key of such a reading was checked by rules this daemon does not run, and a key of it settled
// here would reach the pods this daemon is supervising: the stop timeout is every Sandbox's
// TerminationGrace, below which the launcher kills a shim holding its CodeGraph lease, and
// `daemon_url` is a launch key recorded in sandbox.LaunchSettings. Pinning `runtime` alone
// would settle each of them under the runtime the daemon booted on, which is why the whole
// reading is refused rather than that one key.
func (l *Live) Apply(source, by string, overlay map[string]any) (applied []Change, refused []Refusal, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	merged := map[string]any{}
	maps.Copy(merged, l.overlay)
	switch source {
	case SourceFile:
		for path, value := range overlay {
			if value != nil {
				return nil, nil, errors.New(`a "file" apply carries no value of its own: it reads the file again, keeps the stored route overlay, and drops only the paths the file has taken back`)
			}
			delete(merged, path)
		}
	case SourceRoute:
		for _, path := range slices.Sorted(maps.Keys(overlay)) {
			switch row, known := ClassOf(path); {
			case !known:
				refused = append(refused, Refusal{Path: path, Reason: "unknown setting"})
			case row.Class == ClassFixed:
				refused = append(refused, Refusal{Path: path, Reason: row.Why})
			case l.pinned[path] != "":
				// Fixed for this daemon alone (RefusePath): the holder a change of it names was
				// never built, so it is refused by name as a fixed path is, before it is merged.
				refused = append(refused, Refusal{Path: path, Reason: l.pinned[path]})
			case overlay[path] == nil:
				delete(merged, path)
			default:
				merged[path] = overlay[path]
			}
		}
	default:
		return nil, nil, fmt.Errorf("unknown settings source %q: the sources are %q and %q", source, SourceFile, SourceRoute)
	}

	next, err := l.resolve(merged)
	if err != nil {
		return nil, refused, err
	}
	settled := l.settled.Load()
	if next.Runtime.Name != settled.Runtime.Name {
		// A reading of the other runtime. Nothing of it settles: no snapshot, no overlay, no
		// observer, and the daemon goes on running the configuration it has.
		return nil, refused, &OtherRuntime{Path: "runtime", File: next.Runtime.Name, Booted: settled.Runtime.Name}
	}
	// The reading as the loader resolved it, before the pin edits next: every path the pin
	// writes is ClassFixed, whose changes are dropped below, so the diff of two readings is the
	// diff of what the daemon runs on for every path it reports. A member of a block this
	// reading dropped is absent here and reads as unset, whether or not the pin then carries
	// the block over for a fixed member of it, and a block the next reading puts back reads as
	// arriving from unset, since this reading did not carry it either. So one file dropping
	// `runtime.tmux` reads the same under a boot that took over on nothing and under one that
	// did, and no later settle reports the carrier's zero values as changes of its own.
	reading := settingsOf(&next)
	pinned := pinFixed(settled, &next)
	pinned = append(pinned, pinDaemon(settled, &next, l.pinned)...)
	// A keyed map is read again once the pin is done with it, the map whole and every member of
	// every entry, because it is the one value the pin writes into that is not itself fixed:
	// pinKeyed puts a fixed member back inside `projects`, a rebuild-class value, and carries an
	// entry the reading dropped for the fixed members it held. Read before the pin, the change
	// of `projects` named the map the caller asked for, with a memory mode the daemon refused in
	// it, and a reading that moved a fixed member alone rebuilt the launch specs and the runtime
	// for a map the daemon holds unmoved. Read here, the change is the map Snapshot answers, and
	// a fixed member the pin took back moves nothing; the refusal beside it says which member
	// that was. An entry the pin carried is one the map holds, so its other members read as the
	// zero values it holds them at, not as unset as a carried block's do. The pin drops no entry
	// and no member, so this walk writes over every keyed path the first one read.
	WalkSettings(&next, func(path string, row Row, value reflect.Value) {
		if row.Keyed || path != row.Path {
			reading[path] = settingOf(row, value)
		}
	})
	if source == SourceFile {
		refused = append(refused, pinned...)
	} else {
		refused = append(refused, askedFor(overlay, pinned)...)
	}
	applied = diff(l.reading, reading, source, by)
	// A fixed path is never a change of the daemon's, whatever the reading carries: it holds the
	// value boot settled, and the pin put that value back wherever the reading moved it
	// (pinFixed). A fixed path the reading moved, a fixed member of a block only one of the two
	// readings carries among them, would otherwise be reported changed to a caller that named no
	// such key. A path this daemon pins for itself (pinDaemon) is the same: the holder it names
	// was never built, so the reading's value is pinned back and the change is no change of the
	// daemon's, dropped here so no observer is told to rebuild a holder that does not exist.
	applied = slices.DeleteFunc(applied, func(change Change) bool {
		_, daemonPinned := l.pinned[change.Path]
		return change.Class == ClassFixed || daemonPinned
	})
	l.overlay = merged
	l.settled.Store(&next)
	l.reading = reading
	for _, observe := range l.observers {
		observe(&next, applied)
	}
	return applied, refused, nil
}

// askedFor is the refusals of pinned that the caller's own request made and the answer can say
// no other way: a path under a keyed map the request set whole (Row.Keyed), whose value
// replaces the file's own mapping entire (overlayDocument), so every entry under it, dropped
// or written, is that request's doing.
//
// It is the route arm's filter, and it is narrow because the answer already says the rest.
// A fixed path the caller named is refused before this resolution, and a fixed member of a
// block is a path of its own the diff compares, so the answer carries no change of it at all
// — `github_apps.review` beside an implement App a caller sets through the `github_apps`
// block. A keyed map's own line is the one that answers the caller's value applied while the
// daemon took less of it: the change of the map carries the map the daemon holds (Apply), so a
// member the pin took back is at its boot value there and at the request's value in what the
// caller sent, and nothing on that line says which member differs or why. What this filter
// leaves out of a route answer is every path pinned under no requested path, which is a file
// edit this resolution read beside the request and the file arm names at the settle the
// watcher makes of it (Apply).
func askedFor(requested map[string]any, pinned []Refusal) []Refusal {
	var asked []Refusal
	for _, refusal := range pinned {
		for path := range requested {
			if row, known := ClassOf(path); !known || !row.Keyed {
				continue
			}
			if strings.HasPrefix(refusal.Path, path+".") {
				asked = append(asked, refusal)
				break
			}
		}
	}
	return asked
}

// resolve reads the file again, lays overlay over its document, runs the whole of it through the
// loader, App private keys included, exactly as a boot's Load does, and then the caller's own fix
// of what the file says (Normalize).
func (l *Live) resolve(overlay map[string]any) (Config, error) {
	if l.path == "" {
		return Config{}, errors.New("this settings holder was built from a configuration value (config.Settled) and has no file to read again")
	}
	document, err := readDocument(l.path)
	if err != nil {
		return Config{}, err
	}
	if err := overlayDocument(document, overlay); err != nil {
		return Config{}, err
	}
	cfg, err := loadDocument(document, l.path, l.env, true)
	if err != nil {
		return Config{}, err
	}
	if l.normalize != nil {
		if err := l.normalize(&cfg); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

// overlayDocument lays the overlay over the file's own document, in place: each dotted path names
// a chain of mapping keys, and the last holds the value. A key the file does not carry is created,
// one it carries is replaced, and a null one is filled, so the loader reads the two layers as one
// file. The paths are laid in sorted order, so two paths that meet settle the same way every time.
func overlayDocument(document *yaml.Node, overlay map[string]any) error {
	if len(overlay) == 0 {
		return nil
	}
	if document.Kind == 0 || len(document.Content) == 0 {
		document.Kind = yaml.DocumentNode
		document.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	root, err := rootMapping(document)
	if err != nil {
		return err
	}
	for _, path := range slices.Sorted(maps.Keys(overlay)) {
		segments := strings.Split(path, ".")
		mapping := root
		for depth, name := range segments[:len(segments)-1] {
			member := mappingMember(mapping, name)
			if member == nil {
				member = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				mapping.Content = append(mapping.Content, scalarNode(name), member)
			}
			if member.Tag == "!!null" {
				*member = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
			if member.Kind != yaml.MappingNode {
				return fmt.Errorf("%s cannot be set: the file's %s is not a mapping", path, strings.Join(segments[:depth+1], "."))
			}
			mapping = member
		}
		var encoded yaml.Node
		if err := encoded.Encode(overlay[path]); err != nil {
			return fmt.Errorf("%s cannot be set: %w", path, err)
		}
		name := segments[len(segments)-1]
		if member := mappingMember(mapping, name); member != nil {
			*member = encoded
			continue
		}
		mapping.Content = append(mapping.Content, scalarNode(name), &encoded)
	}
	return nil
}

// mappingMember is the value node a mapping holds under name, nil when it holds none. A file that
// carries one key twice is read as readKeys reads it — the last member wins — so an overlay laid
// over such a file changes the value the loader would have taken, not the one it discards.
func mappingMember(mapping *yaml.Node, name string) *yaml.Node {
	var member *yaml.Node
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == name {
			member = mapping.Content[i+1]
		}
	}
	return member
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

// setting is one path's value twice over: as the diff compares it, and as a Change prints it. A
// secret's two differ, so a rotation is seen without being shown.
type setting struct{ compared, shown string }

// settingsOf is cfg by dotted path: one entry per path the class table calls a value rather than a
// block, a keyed map's members each under the path its entry's name makes of the row. A block the
// configuration does not carry contributes nothing, and the diff reads its members as unset.
func settingsOf(cfg *Config) map[string]setting {
	settings := map[string]setting{}
	WalkSettings(cfg, func(path string, row Row, value reflect.Value) {
		settings[path] = settingOf(row, value)
	})
	return settings
}

// settingOf is one value of row as the diff compares it and a Change shows it.
func settingOf(row Row, value reflect.Value) setting {
	rendered := Render(value)
	if row.Secret {
		return setting{compared: rendered, shown: withheldValue}
	}
	return setting{compared: rendered, shown: rendered}
}

// WalkSettings visits every value of cfg the class table names, by the dotted path it is read at
// and the row it is read into: one visit per path the table calls a value rather than a block,
// with a block descended into and a block the configuration does not carry contributing nothing.
// A field the table has no row for is a derived value that is no legion.yaml key, which config's
// own census test names.
//
// A keyed map (Row.Keyed) is visited whole at its own path and then descended into, entry by
// entry, each member at the path the entry's name makes of its row's template (Row.PathAt):
// `projects`, then `projects.DEMO.commit_issue_trailer_repos` and every other member of DEMO.
// A member's class is its own and not the map's — `commit_issue_trailer_repos` and
// `memory.config_files` are launch keys of a rebuild-class map, `production_check` a decision —
// so a walk that stopped at the map answered a caller who set one member that nothing moved,
// and recorded neither launch key in any pod's launch settings. So path is row.Path for every
// value but a member of a keyed map, the one place the two differ. The entries are visited in no
// particular order: every visitor keys what it reads by path.
//
// It is the one walk of one reading of the configuration the daemon has: config's own diff
// reads its settings through it (settingsOf), and runtime/sandbox records a pod's launch keys
// through it (sandbox.LaunchSettings), so a row added to the table reaches both and the two can
// never disagree on which fields the table names. The pin of the fixed paths walks two readings
// at once and is the one walk that does (pinBlock); what the two share is settingFields, the
// one rule that says which field of a block is which row, so they can differ only in what they
// do with a pointer block, with a block a reading does not carry, and with an entry one reading
// of a keyed map does not carry.
func WalkSettings(cfg *Config, visit func(path string, row Row, value reflect.Value)) {
	walkSettings(reflect.ValueOf(cfg).Elem(), "", visit)
}

// settingFields is every field of one block of the configuration the class table names, as the
// field's index in typ and the row it is read into. A field the table has no row for, and an
// unexported one, is no `legion.yaml` key and is passed over.
//
// It is the one field-to-row rule the daemon has, and both walks of the configuration iterate
// it: the walk of one reading (walkSettings) and the pin's walk of two (pinBlock). Written
// twice, the two can disagree about which field is which row, and a member one walk reaches
// and the other does not is a value the daemon holds and never reports.
func settingFields(typ reflect.Type) iter.Seq2[int, Row] {
	return func(yield func(int, Row) bool) {
		for i := range typ.NumField() {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			row, known := rowOf(typ.Name() + "." + field.Name)
			if !known {
				continue
			}
			if !yield(i, row) {
				return
			}
		}
	}
}

// walkSettings is WalkSettings over one block, key being the name of the keyed map's entry the
// block sits inside, "" for a block of the configuration itself (pinBlock's key).
func walkSettings(block reflect.Value, key string, visit func(path string, row Row, value reflect.Value)) {
	for i, row := range settingFields(block.Type()) {
		value := block.Field(i)
		if row.Keyed {
			visit(row.PathAt(key), row, value)
			for entry := value.MapRange(); entry.Next(); {
				walkSettings(entry.Value(), entry.Key().String(), visit)
			}
			continue
		}
		if !row.Block {
			visit(row.PathAt(key), row, value)
			continue
		}
		if value.Kind() == reflect.Pointer {
			if value.IsNil() {
				continue
			}
			value = value.Elem()
		}
		walkSettings(value, key, visit)
	}
}

// pinFixed puts the value settled holds for every fixed path back into next and names each path
// it pinned with the reason that path's row gives for refusing a change of it.
//
// It is what makes ClassFixed true of the snapshot. The route refuses a fixed path by name, but
// every resolution reads the whole file again (resolve), so an operator's edit of such a key in
// `legion.yaml` would otherwise settle the moment anything applied — a file settle, or a route
// change of some other key — and reach every reader that takes its value from Snapshot: the
// engine's project and the consumer spec the NATS rebuild opens on, the runtime name the
// plugin-install rule reads, the hooks the engine fires. Pinned here, each of those reads the
// value the daemon booted on, and a reader that takes a fixed value from the boot configuration
// of its own is a second defence rather than the only one.
//
// `runtime` is no part of its work: a reading naming the other runtime settles nothing at all,
// and Apply answers it before anything is pinned, so the two readings this walk compares name
// one runtime and carry that runtime's block alone. Pinning the selector and taking the file's
// block is what that rule replaced — it would hold a configuration no loader produces, whose
// nil block the Sandbox build dereferences (internal/daemon's readSandbox,
// `*cfg.Runtime.Kubernetes`) inside this very apply, under the holder's lock, in a daemon that
// is supervising pods.
//
// Which paths are fixed is Classes and no second list, and the walk descends by Row.Block and
// Row.Keyed as WalkSettings does, so a row the table makes fixed is pinned with nothing else
// edited and the pin and the diff can never disagree about which paths the configuration holds.
// A keyed map is a map whose names the file chooses, which is `projects` and nothing else today,
// so the fixed rows the table holds under `projects.<KEY>` — `memory.mode` and
// `memory.instructions` — are pinned per project key and refused by the path that key makes of
// the template (pinKeyed, Row.PathAt).
//
// Pinning a member of that map pins no block, and a member's class is its own whatever the
// class of the value around it: `projects` is one rebuild-class value, and what this walk writes
// is the member alone, so the project's Slack channels, its commit trailer repositories, its
// production check and its memory overlays settle from a reading that moves a fixed member
// beside them. What the diff reports of such a reading is the map as this walk leaves it, read
// after it (Apply): `projects` is reported changed where something the daemon now holds of it
// moved, with the value the daemon holds, each member that moved beside it under its own path,
// and the refusal is what says which member of the block the daemon did not take. So a fixed
// member is refused whatever named it: its own path on the route, the block around it, or the
// file.
func pinFixed(settled, next *Config) []Refusal {
	return pinBlock(reflect.ValueOf(settled).Elem(), reflect.ValueOf(next).Elem(), "", nil)
}

// pinDaemon pins every path this daemon refuses for itself (Live.pinned, RefusePath) back to the
// value boot settled, into next, and names each it moved with the reason the daemon gave it. It
// is pinFixed's per-daemon twin: the path's class in the table is rebuild, but this daemon built
// no holder the change could reach, so for this daemon the key is fixed and reaches nothing until
// a restart. The paths are read in sorted order, so two readings are refused the same way every
// time. A path whose value did not move is pinned nothing and refused nothing, as pinFixed leaves
// an unmoved fixed path, so the route arm's own pre-resolve refusal of the key the caller named
// (Apply) is not doubled here.
func pinDaemon(settled, next *Config, paths map[string]string) []Refusal {
	var pinned []Refusal
	for _, path := range slices.Sorted(maps.Keys(paths)) {
		was, now := fieldByPath(settled, path), fieldByPath(next, path)
		if !was.IsValid() || !now.IsValid() || Render(now) == Render(was) {
			continue
		}
		now.Set(was)
		pinned = append(pinned, Refusal{Path: path, Reason: paths[path]})
	}
	return pinned
}

// fieldByPath is the configuration field the dotted path is read into, as a reflect.Value of
// cfg's own — settable when cfg is addressable, as a *Config's fields are — or the zero Value
// when no row of the class table names the path, or the path sits under a block this reading
// does not carry. It walks the table's one field-to-row rule (WalkSettings), so it reaches
// exactly the paths the diff and the pins do, but for a member of a keyed map: that is a copy
// the map hands out, no field of cfg's own and settable by nobody, so pinDaemon could not pin
// it, and it is the one visit whose path is not its row's.
func fieldByPath(cfg *Config, path string) reflect.Value {
	var found reflect.Value
	WalkSettings(cfg, func(visited string, row Row, value reflect.Value) {
		if visited == path && visited == row.Path {
			found = value
		}
	})
	return found
}

// pinBlock pins every fixed value of one block of the configuration — was as boot settled it,
// now as this resolution read it — and descends into the blocks it holds, appending each path
// it pinned to pinned. key is the name the file gave the keyed map's entry this block sits
// inside, "" for a block of the configuration itself, and is what a refusal writes into the
// row's template (Row.PathAt).
//
// A fixed member keeps its boot value whether or not either reading carries its block, because
// a block a reading does not carry is that reading holding every member of it at its zero
// value and not that reading saying nothing about them: an invalid was, and a nil block on
// either side, read that way. So a reading that brings a block the boot had none of has its
// fixed members pinned back to "" — the tmux daemon booted with no `runtime.tmux`, whose file
// then names `take_over`, which the rebuilt runtime would otherwise take over on for the whole
// of its life against whyTakeOve — and a reading that drops a block whose boot held a fixed
// value carries that value in a block of its own, holding it and nothing else, so every launch
// key the reading dropped holds the zero value it dropped them to. What the diff reports of
// such a block is the reading's, taken before this walk (Apply): each member the reading
// dropped reads as unset there, carried or not. A block neither reading's fixed members move
// stays exactly as the reading left it, dropped included, which is the file that drops
// `runtime.tmux` under a boot that took over on nothing: its members are launch keys an
// operator is entitled to change (TestABlockTheFileDropsReadsAsUnset).
func pinBlock(was, now reflect.Value, key string, pinned []Refusal) []Refusal {
	typ := now.Type()
	for i, row := range settingFields(typ) {
		fieldType := typ.Field(i).Type
		boot := was
		if boot.IsValid() {
			boot = was.Field(i)
		} else {
			boot = reflect.Zero(fieldType)
		}
		value := now.Field(i)
		if row.Keyed {
			pinned = pinKeyed(boot, value, pinned)
			continue
		}
		if !row.Block {
			if row.Class != ClassFixed || Render(value) == Render(boot) {
				continue
			}
			value.Set(boot)
			pinned = append(pinned, Refusal{Path: row.PathAt(key), Reason: row.Why})
			continue
		}
		if fieldType.Kind() != reflect.Pointer {
			pinned = pinBlock(boot, value, key, pinned)
			continue
		}
		if boot.IsNil() {
			boot = reflect.Value{}
		} else {
			boot = boot.Elem()
		}
		if !value.IsNil() {
			pinned = pinBlock(boot, value.Elem(), key, pinned)
			continue
		}
		if !boot.IsValid() {
			// Neither reading carries the block, so no fixed member of it can have moved.
			continue
		}
		// The reading drops the block. Its fixed members are pinned all the same, into a block
		// of this reading's own carrying them and nothing else; a block no fixed member of
		// which moved is left dropped, since the pin is the whole of what this walk may add to
		// a reading.
		carried := reflect.New(fieldType.Elem())
		pinnedBefore := len(pinned)
		pinned = pinBlock(boot, carried.Elem(), key, pinned)
		if len(pinned) == pinnedBefore {
			continue
		}
		value.Set(carried)
	}
	return pinned
}

// pinKeyed pins every fixed member of every entry of one keyed map — `projects`, whose entries
// the file names itself (Row.Keyed) — reading the boot's entry of a name against this
// reading's, and names each path it pinned with that name written in, so a refusal says
// `projects.DEMO.memory.mode` and never the template. The entries are read in name order, so
// two readings that disagree about several projects are refused in one order every time.
//
// A map is three cases a plain block is not, and each is read as pinBlock reads a block: an
// entry one reading does not carry is that reading holding every member of it at its zero
// value, and not that reading saying nothing about it.
//
//   - An entry this reading drops keeps its boot's fixed members, carried in an entry of this
//     reading's own holding them and nothing else, as a dropped block is: the boot drew every
//     tree of that project under the mode it read, and a project the file takes away cannot
//     take that mode back from the trees running under it. Every other member of the entry
//     goes with the entry, since those are the operator's to change, and an entry no fixed
//     member of which moved stays dropped.
//   - An entry this reading adds, which the boot never had, has its fixed members pinned back
//     to the zero value the boot held them at, as a block the boot had none of does: boot read
//     no mode and no instructions file for a project it did not have, so the mode nothing was
//     given is off and the instructions nothing read are none. The rest of the entry settles
//     under its own class.
//   - An entry whose `memory` block one side carries and the other does not is that side
//     holding every member of the block at its zero value, and there is no third state to
//     read: Project.Memory is a value and not a pointer, so a project that names no block is a
//     project whose block says `off` and names no file. So a reading that drops the block is
//     pinned back to the mode boot read, and a reading that brings one the boot had none of is
//     pinned back to "", each refused by name with the overlays beside it left as the reading
//     has them.
//
// A map value is not addressable, so each entry is pinned in a copy of its own and written
// back only where something was pinned in it: an entry this walk changes nothing of is left
// exactly as the reading has it, dropped included.
func pinKeyed(was, now reflect.Value, pinned []Refusal) []Refusal {
	names := map[string]bool{}
	for _, reading := range []reflect.Value{was, now} {
		if !reading.IsValid() {
			continue
		}
		for _, name := range reading.MapKeys() {
			names[name.String()] = true
		}
	}
	entryType := now.Type().Elem()
	for _, name := range slices.Sorted(maps.Keys(names)) {
		// The name as the map holds it, so a keyed map whose key is a string type of its own
		// reads the same as `projects`, whose key is the string itself.
		key := reflect.ValueOf(name).Convert(now.Type().Key())
		boot := reflect.Zero(entryType)
		if was.IsValid() {
			if entry := was.MapIndex(key); entry.IsValid() {
				boot = entry
			}
		}
		carried := reflect.New(entryType).Elem()
		if entry := now.MapIndex(key); entry.IsValid() {
			carried.Set(entry)
		}
		pinnedBefore := len(pinned)
		pinned = pinBlock(boot, carried, name, pinned)
		if len(pinned) == pinnedBefore {
			continue
		}
		if now.IsNil() {
			now.Set(reflect.MakeMap(now.Type()))
		}
		now.SetMapIndex(key, carried)
	}
	return pinned
}

// Render is one value as a log line shows it: a duration as its own string, another scalar as it
// is, and anything composite as JSON, whose map keys are sorted and whose pointers are followed.
// fmt's %v would print a pointer's address, which differs between two loads of one file and would
// make every reload report a change.
//
// A pod's recorded launch settings are rendered through it too (sandbox.LaunchSettings): two
// equal configurations must encode identically, since a record that differs is drift.
func Render(value reflect.Value) string {
	if duration, ok := value.Interface().(time.Duration); ok {
		return duration.String()
	}
	switch value.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return fmt.Sprintf("%v", value.Interface())
	}
	encoded, err := json.Marshal(value.Interface())
	if err != nil {
		return fmt.Sprintf("%+v", value.Interface())
	}
	return string(encoded)
}

// Changed is one path two readings of the settings disagree on, with what each reading holds and
// UnsetValue for a reading that does not carry the path at all.
type Changed struct{ Path, Old, New string }

// Compare is every path the two readings of the settings disagree on, in path order. A path one
// reading does not carry reads as UnsetValue there, so a key that arrived or was dropped is a
// change naming what it was and what it is.
//
// It is the one comparison of two readings the daemon has: config's own diff of two resolved
// configurations reads through it, and runtime/sandbox compares a pod's recorded launch keys
// against the runtime's through it (launchSettingsDrift), so the two can never disagree about
// which paths moved or about how a path only one of them carries reads.
func Compare(old, next map[string]string) []Changed {
	paths := slices.Collect(maps.Keys(next))
	for path := range old {
		if _, held := next[path]; !held {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)

	changed := make([]Changed, 0, len(paths))
	for _, path := range paths {
		before, hadBefore := old[path]
		after, hasAfter := next[path]
		if !hadBefore {
			before = UnsetValue
		}
		if !hasAfter {
			after = UnsetValue
		}
		if before == after {
			continue
		}
		changed = append(changed, Changed{Path: path, Old: before, New: after})
	}
	return changed
}

// diff is one Change per path whose value the two configurations disagree on, in path order. A
// secret is compared by its value and shown as "(withheld)" (setting), so a rotation is a change
// without being printed.
//
// A keyed map is reported twice over where one of its members moved: once whole, under its own
// path, and once per member that moved, under the member's own (WalkSettings). The member's
// change is the answer to a caller who set that member by its path, with its own class, holder
// and values: `projects.DEMO.commit_issue_trailer_repos` is answered next-launch with the
// repositories it held and now holds, not left unchanged while the map beside it moved. The
// map's change stays, because its holders are the ones a rebuild of the map answers for: the
// launch specs and the runtime, which between them hold every member a rebuild can reach, the
// runtime among them for the launch settings it records of each pod (sandbox.LaunchSettings).
// It is also the one line a caller that set the map whole reads, and it carries the map the
// daemon holds after the pin (Apply). The cost is that one member's edit is two changes: two
// log lines and two posts to the controller (internal/daemon's settingsAnnouncer), a source
// recorded for each path, and a rebuild failure answered under both; and a project added or
// dropped whole is its map's change and one change per member it carries. The rebuilder builds
// each holder once per apply however many changes name it, so nothing is built twice. The
// other design, the route reading the settled configuration at each path it was asked for,
// answers that caller alone: the file's own edit of a member, the log and the settings sources
// would still name only the map, and the route would read a configuration a later apply may
// already have replaced.
func diff(old, next map[string]setting, source, by string) []Change {
	var changes []Change
	for _, moved := range Compare(comparedValues(old), comparedValues(next)) {
		row, _ := ClassOf(moved.Path)
		changes = append(changes, Change{
			Path: moved.Path, Old: shownValue(old, moved.Path), New: shownValue(next, moved.Path),
			Class: row.Class, Outcome: row.Class.Outcome(), Holder: row.Holder,
			Source: source, By: by,
		})
	}
	return changes
}

// comparedValues is each path's value as the comparison reads it, and shownValue one path's
// value as a Change prints it, UnsetValue for a path the configuration does not carry.
func comparedValues(settings map[string]setting) map[string]string {
	values := make(map[string]string, len(settings))
	for path, value := range settings {
		values[path] = value.compared
	}
	return values
}

func shownValue(settings map[string]setting, path string) string {
	value, held := settings[path]
	if !held {
		return UnsetValue
	}
	return value.shown
}
