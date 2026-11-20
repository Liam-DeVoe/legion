package launcher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/leakguard"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// The names the check reads in its index folder, spelled here as the interface between the check
// and Legion names them rather than read from leakguard's constants, so a launcher that wrote
// another name fails these tests.
const (
	indexFileName   = "sandbox-lines.idx"
	indexMarkerName = "sandbox-lines.idx.pushed"
)

// indexRefusal begins every refusal of an index slice the launcher answers, the words the daemon
// logs it under.
const indexRefusal = "the leak check index could not be installed"

// indexOf is an index of sandbox lines built at builtAt, as the check's writer lays one out: line
// 1, the header as one JSON object on line 2 with spaces after it until the byte after its LF is
// at a multiple of 8, then entries ascending unsigned 64-bit little-endian integers. It is spelled
// here by hand rather than built through leakguard, so the launcher is held to the file the check
// reads rather than to leakguard's own reading of it. entries sets the size: 32768 entries fill
// one slice of the wire, so 2 fit in one slice, 40000 take two and 80000 three.
func indexOf(builtAt string, entries int) []byte {
	list := make([]byte, 0, 8*entries)
	for value := range entries {
		list = binary.LittleEndian.AppendUint64(list, uint64(2*value+1))
	}
	sum := sha256.Sum256(list)
	head := "PUBLIC-LEAK-CHECK-INDEX 1\n" + fmt.Sprintf(`{"built_at":%q,"canary":"public leak check index canary %032x",`+
		`"entries":%d,"format":1,"generic":{"go":"none","python":"3.12.3"},"min_bytes":40,"public_trees":[],`+
		`"sandbox_files":[],"sha256":"%x"}`, builtAt, entries, entries, sum)
	head += strings.Repeat(" ", (8-(len(head)+1)%8)%8) + "\n"
	return append([]byte(head), list...)
}

// slicedIndex is the launcher_leak_check_index sequence the daemon pushes of body, which must take
// want slices.
func slicedIndex(t *testing.T, body []byte, want int) []shimwire.LauncherLeakCheckIndex {
	t.Helper()
	frames, err := shimwire.SplitLeakCheckIndex(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != want {
		t.Fatalf("the index sliced into %d frames; the test needs %d", len(frames), want)
	}
	return frames
}

// pushIndex sends every slice of frames on conn and answers the launcher's result for each, in
// order, each answering the slice it was sent for in a form the daemon takes.
func pushIndex(t *testing.T, conn *roleConn, frames []shimwire.LauncherLeakCheckIndex) []shimwire.LauncherLeakCheckIndexResult {
	t.Helper()
	results := make([]shimwire.LauncherLeakCheckIndexResult, 0, len(frames))
	for _, frame := range frames {
		got, ok := conn.answer(frame).(shimwire.LauncherLeakCheckIndexResult)
		if !ok {
			t.Fatalf("slice %d was answered %#v, want a launcher_leak_check_index_result", frame.Index, got)
		}
		results = append(results, requireIndexAnswer(t, frame, got))
	}
	return results
}

// requireIndexAnswer holds that got answers frame, in a form the daemon takes, and answers it.
func requireIndexAnswer(t *testing.T, frame shimwire.LauncherLeakCheckIndex, got shimwire.LauncherLeakCheckIndexResult) shimwire.LauncherLeakCheckIndexResult {
	t.Helper()
	if got.ID != frame.ID || got.Index != frame.Index {
		t.Fatalf("slice %d of %s was answered for slice %d of %s", frame.Index, frame.ID, got.Index, got.ID)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("slice %d was answered %#v, which the daemon refuses: %v", frame.Index, got, err)
	}
	return got
}

// requireIndexInstalled holds that a whole sequence was installed at its last slice: every slice
// before it taken, and the last answered Installed.
func requireIndexInstalled(t *testing.T, results []shimwire.LauncherLeakCheckIndexResult) {
	t.Helper()
	for index, got := range results {
		if want := index == len(results)-1; !got.OK || got.Installed != want || got.Error != "" {
			t.Fatalf("slice %d of %d was answered %#v, want OK and installed=%v", index, len(results), got, want)
		}
	}
}

// requireIndexTaken holds that got took its slice and installed nothing yet.
func requireIndexTaken(t *testing.T, got shimwire.LauncherLeakCheckIndexResult) {
	t.Helper()
	if !got.OK || got.Installed || got.Error != "" {
		t.Fatalf("slice %d was answered %#v, want it taken and nothing installed yet", got.Index, got)
	}
}

// requireIndexRefused holds that got refuses its slice with one line beginning indexRefusal, and
// answers that line.
func requireIndexRefused(t *testing.T, got shimwire.LauncherLeakCheckIndexResult) string {
	t.Helper()
	if got.OK || got.Installed || !strings.HasPrefix(got.Error, indexRefusal) || strings.Contains(got.Error, "\n") {
		t.Fatalf("slice %d was answered %#v, want a refusal whose cause is one line beginning %q", got.Index, got, indexRefusal)
	}
	return got.Error
}

// heldIndex is what dir holds as the index, read as the check reads it.
func heldIndex(t *testing.T, dir string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, indexFileName))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// requireHeld holds that dir holds want as the index and nothing beside it but the pushed marker.
func requireHeld(t *testing.T, dir string, want []byte, what string) {
	t.Helper()
	if !bytes.Equal(heldIndex(t, dir), want) {
		t.Fatalf("the folder does not hold %s", what)
	}
	if names := slices.Sorted(maps.Keys(tree(t, dir))); !slices.Equal(names, []string{".", indexFileName, indexMarkerName}) {
		t.Fatalf("the folder holds %v, want the index and the marker alone", names)
	}
}

// newTestIndexInstall is the install a launcher builds for dir, made as newManagers makes it.
func newTestIndexInstall(t *testing.T, dir string) *indexInstall {
	t.Helper()
	c, err := newIndexInstall(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatalf("the install of %s is nil", dir)
	}
	return c
}

// The launcher writes the index folder's pushed marker before it dials the daemon at all, so no
// check of a role it starts later downloads the index. Here the daemon cannot be reached: the
// marker is there while the launcher has made no connection. A launcher whose daemon answers
// says in its hello that it takes the index, and one that cannot write the marker refuses to run,
// naming why, before it dials.
func TestTheLauncherMarksItsIndexFolderBeforeItConnects(t *testing.T) {
	t.Run("with no daemon to reach", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "home", ".cache", "public-leak-check")
		stderr := &lockedBuffer{}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			result <- Run(ctx, Config{
				Connect: "tcp://127.0.0.1:1", Token: "launcher-token", Sandbox: "legion-sandbox",
				Role: "tester", PodUID: "pod-uid", PrivateDir: shortTempDir(t), Stderr: stderr, LeakCheckIndexDir: dir,
			})
		}()
		t.Cleanup(func() {
			cancel()
			select {
			case <-result:
			case <-time.After(5 * time.Second):
				t.Error("the launcher did not leave after its context ended")
			}
		})
		testwait.Eventually(t, "the launcher to fail its first dial of the daemon", func() bool {
			return strings.Contains(stderr.String(), "launcher: daemon connection: dial ")
		})
		info, err := os.Lstat(filepath.Join(dir, indexMarkerName))
		if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
			t.Fatalf("before any connection the marker is %v (%v), want an empty regular file; the launcher's log:\n%s", info, err, stderr.String())
		}
	})
	t.Run("and says it takes the index", func(t *testing.T) {
		g := newSessionRig(t, Config{LeakCheckIndexDir: filepath.Join(t.TempDir(), "public-leak-check")})
		if !g.hello.LeakCheckIndex {
			t.Fatalf("the hello is %#v, want it to say the launcher takes the index", g.hello)
		}
	})
	t.Run("a folder it cannot mark", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-folder")
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := Run(ctx, Config{
			Connect: "tcp://127.0.0.1:1", Token: "launcher-token", Sandbox: "legion-sandbox",
			Role: "tester", PodUID: "pod-uid", PrivateDir: shortTempDir(t), Stderr: &lockedBuffer{},
			LeakCheckIndexDir: filepath.Join(file, "public-leak-check"),
		})
		if err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "pushed marker could not be written in "+filepath.Join(file, "public-leak-check")) {
			t.Fatalf("Run answered %v, want the marker's refusal before it dialed", err)
		}
	})
}

// The controller's pod is the single-role form: its one launcher takes an index pushed in two
// slices, answers the first taken and the last Installed, and writes the index whole where the
// check reads it, holding the build time the daemon sent, with nothing left beside it.
func TestTheLauncherInstallsAWholeIndex(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "public-leak-check")
	g := newSessionRig(t, Config{LeakCheckIndexDir: dir})
	body := indexOf("2026-11-19T09:07:00Z", 40000)

	requireIndexInstalled(t, pushIndex(t, g.roleConn, slicedIndex(t, body, 2)))
	index, err := leakguard.ParseIndex(heldIndex(t, dir))
	if err != nil {
		t.Fatalf("the installed index does not parse: %v", err)
	}
	if want := time.Date(2026, time.November, 19, 9, 7, 0, 0, time.UTC); !index.BuiltAt.Equal(want) {
		t.Fatalf("the installed index was built at %s, want the %s the daemon sent", index.BuiltAt, want)
	}
	requireHeld(t, dir, body, "the index the daemon sent")
}

// An index whose slices rebuild a body of another digest than the one they declare did not arrive
// as it was sent: its last slice is refused, and the folder, which holds an earlier index, is left
// as it was. The refused slices are dropped, so the true index pushed again then installs.
func TestTheLauncherRefusesAWrongDigest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "public-leak-check")
	g := newSessionRig(t, Config{LeakCheckIndexDir: dir})
	earlier := indexOf("2026-11-19T08:07:00Z", 2)
	requireIndexInstalled(t, pushIndex(t, g.roleConn, slicedIndex(t, earlier, 1)))
	before := tree(t, dir)

	later := indexOf("2026-11-19T09:07:00Z", 40000)
	frames := slices.Clone(slicedIndex(t, later, 2))
	payload := slices.Clone(frames[1].Payload)
	payload[0] ^= 0xff
	frames[1].Payload = payload
	results := pushIndex(t, g.roleConn, frames)
	requireIndexTaken(t, results[0])
	if cause, want := requireIndexRefused(t, results[1]), "not the "+frames[0].SHA256+" they declare"; !strings.Contains(cause, want) {
		t.Fatalf("the refusal is %q, want it to say %q", cause, want)
	}
	if after := tree(t, dir); !maps.Equal(after, before) {
		t.Fatalf("the refused index left %v where %v was", slices.Sorted(maps.Keys(after)), slices.Sorted(maps.Keys(before)))
	}

	requireIndexInstalled(t, pushIndex(t, g.roleConn, slicedIndex(t, later, 2)))
	requireHeld(t, dir, later, "the true index pushed again")
}

// An issue pod's one container writes the index once for all its roles: every role's hello says
// it takes the index, the daemon pushes it on the first role's connection, and the second role's
// first slice of the same index is answered Installed at once.
func TestASecondRoleIsAnsweredInstalledAtOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "public-leak-check")
	f := newFleetOf(t, Config{StopGrace: 2 * time.Second, LeakCheckIndexDir: dir}, "tester", "reviewer")
	for _, role := range []string{"tester", "reviewer"} {
		if hello := f.role(role).hello; !hello.LeakCheckIndex {
			t.Fatalf("role %s's hello is %#v, want it to say the launcher takes the index", role, hello)
		}
	}
	body := indexOf("2026-11-19T09:07:00Z", 40000)
	frames := slicedIndex(t, body, 2)

	requireIndexInstalled(t, pushIndex(t, f.role("tester"), frames))
	if got := pushIndex(t, f.role("reviewer"), frames[:1])[0]; !got.OK || !got.Installed {
		t.Fatalf("the reviewer's first slice answered %#v, want Installed at once: the tester's connection installed it", got)
	}
	requireHeld(t, dir, body, "the index the tester's connection installed")
}

// A launcher given no index folder says nothing of the index in its hello, writes nothing, and
// refuses an index slice pushed to it anyway, naming why.
func TestAHelloWithNoIndexFolderSaysNothing(t *testing.T) {
	g := newRig(t)
	if g.hello.LeakCheckIndex {
		t.Fatalf("the hello is %#v, want it to say nothing of the index", g.hello)
	}
	got := pushIndex(t, g.roleConn, slicedIndex(t, indexOf("2026-11-19T09:07:00Z", 2), 1))[0]
	if cause, want := requireIndexRefused(t, got), "no folder to write it in"; !strings.Contains(cause, want) {
		t.Fatalf("the refusal is %q, want it to say %q", cause, want)
	}
}

// indexRaceRounds is how many times TestTwoIndexesPushedAtOnceEndWithTheNewer pushes its two
// indexes at once.
const indexRaceRounds = 50

// Two roles' connections may each carry a whole index at once, an older and a newer.
// leakguard.InstallIndex keeps the newer only of two calls made one after the other: two at once
// can both read the folder before either renames, and the older can rename last. So the launcher
// installs one index at a time, and every round ends with the newer index, whichever arrived
// first, both sequences answered Installed. Each index is one slice, so each call is a whole
// install and the two calls start together.
func TestTwoIndexesPushedAtOnceEndWithTheNewer(t *testing.T) {
	older := slicedIndex(t, indexOf("2026-11-19T09:07:00Z", 2), 1)[0]
	newerBody := indexOf("2026-11-19T10:07:00Z", 3)
	newer := slicedIndex(t, newerBody, 1)[0]
	for round := range indexRaceRounds {
		dir := filepath.Join(t.TempDir(), "public-leak-check")
		c := newTestIndexInstall(t, dir)
		begin := make(chan struct{})
		frames := []shimwire.LauncherLeakCheckIndex{older, newer}
		answers := make([]shimwire.LauncherLeakCheckIndexResult, len(frames))
		var pushes sync.WaitGroup
		for caller, frame := range frames {
			pushes.Go(func() {
				<-begin
				answers[caller] = c.receive(frame)
			})
		}
		close(begin)
		pushes.Wait()
		for caller, got := range answers {
			if got = requireIndexAnswer(t, frames[caller], got); !got.OK || !got.Installed {
				t.Fatalf("round %d: the push of %s was answered %#v, want Installed", round, frames[caller].ID, got)
			}
		}
		if !bytes.Equal(heldIndex(t, dir), newerBody) {
			t.Fatalf("round %d of %d ended with the older index in place of the newer", round, indexRaceRounds)
		}
	}
}

// The check reads an index built far ahead of the clock as a header it cannot read, so the
// launcher does not install one and answers its last slice with the refusal, which the daemon
// logs: a builder's clock fault is seen there before the pod's index grows old. The folder, which
// holds an earlier index, is left as it was. The index is built a day ahead, which any bound of
// the install's on a time ahead refuses.
func TestAnIndexBuiltAheadOfTheClockIsRefusedToTheDaemon(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "public-leak-check")
	g := newSessionRig(t, Config{LeakCheckIndexDir: dir})
	earlier := indexOf("2026-11-19T08:07:00Z", 2)
	requireIndexInstalled(t, pushIndex(t, g.roleConn, slicedIndex(t, earlier, 1)))
	before := tree(t, dir)

	ahead := time.Now().Add(24 * time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	results := pushIndex(t, g.roleConn, slicedIndex(t, indexOf(ahead, 40000), 2))
	requireIndexTaken(t, results[0])
	cause := requireIndexRefused(t, results[1])
	for _, want := range []string{"the index's built_at " + ahead, "ahead of"} {
		if !strings.Contains(cause, want) {
			t.Fatalf("the refusal is %q, want it to say %q", cause, want)
		}
	}
	if after := tree(t, dir); !maps.Equal(after, before) {
		t.Fatalf("the refused index left %v where %v was", slices.Sorted(maps.Keys(after)), slices.Sorted(maps.Keys(before)))
	}
}

// The launcher holds one index's slices at a time, and a push begins at its first slice. So a
// first slice of another index begins that one in place of an index half pushed, and a later
// slice of the index it replaced is refused, naming the slice, rather than taken into the other's
// place; the index begun last installs. The older one pushed again whole after it is answered
// Installed, and the folder keeps the newer.
func TestASliceOfAnIndexBegunInItsPlaceIsRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "public-leak-check")
	c := newTestIndexInstall(t, dir)
	olderBody := indexOf("2026-11-19T09:07:00Z", 40000)
	newerBody := indexOf("2026-11-19T10:07:00Z", 40001)
	older, newer := slicedIndex(t, olderBody, 2), slicedIndex(t, newerBody, 2)

	requireIndexTaken(t, requireIndexAnswer(t, older[0], c.receive(older[0])))
	requireIndexTaken(t, requireIndexAnswer(t, newer[0], c.receive(newer[0])))
	cause := requireIndexRefused(t, requireIndexAnswer(t, older[1], c.receive(older[1])))
	if want := "slice 1 of 2 of " + older[1].ID; !strings.Contains(cause, want) {
		t.Fatalf("the refusal is %q, want it to name %q", cause, want)
	}
	if got := requireIndexAnswer(t, newer[1], c.receive(newer[1])); !got.OK || !got.Installed {
		t.Fatalf("the newer index's last slice answered %#v, want Installed", got)
	}
	requireHeld(t, dir, newerBody, "the index begun last")

	requireIndexTaken(t, requireIndexAnswer(t, older[0], c.receive(older[0])))
	if got := requireIndexAnswer(t, older[1], c.receive(older[1])); !got.OK || !got.Installed {
		t.Fatalf("the older index pushed again answered %#v at its last slice, want Installed", got)
	}
	requireHeld(t, dir, newerBody, "the newer index, kept against the older")
}

// The last slice of an index answers the push's outcome, so a last slice that leaves its index
// with a slice that never arrived is refused, naming that slice, and nothing is installed. The
// whole index pushed again then installs.
func TestTheLastSliceOfAnIndexMissingASliceIsRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "public-leak-check")
	c := newTestIndexInstall(t, dir)
	body := indexOf("2026-11-19T09:07:00Z", 80000)
	frames := slicedIndex(t, body, 3)

	requireIndexTaken(t, requireIndexAnswer(t, frames[0], c.receive(frames[0])))
	cause := requireIndexRefused(t, requireIndexAnswer(t, frames[2], c.receive(frames[2])))
	if want := "slice 1 of 3 never arrived"; !strings.Contains(cause, want) {
		t.Fatalf("the refusal is %q, want it to say %q", cause, want)
	}
	if names := slices.Sorted(maps.Keys(tree(t, dir))); !slices.Equal(names, []string{".", indexMarkerName}) {
		t.Fatalf("the folder holds %v, want the marker alone", names)
	}

	for index, frame := range frames {
		got := requireIndexAnswer(t, frame, c.receive(frame))
		if want := index == len(frames)-1; !got.OK || got.Installed != want {
			t.Fatalf("slice %d of the index pushed again answered %#v, want OK and installed=%v", index, got, want)
		}
	}
	requireHeld(t, dir, body, "the whole index pushed again")
}

// Two roles' connections may carry the same index at once, and when it is refused each is told
// why: the slice of the second connection that comes after the refusal is refused with the same
// cause. A push of it begun again at its first slice is taken afresh and judged again.
func TestEverySliceOfARefusedIndexIsToldItsCause(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "public-leak-check")
	c := newTestIndexInstall(t, dir)
	ahead := time.Now().Add(24 * time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	frames := slicedIndex(t, indexOf(ahead, 40000), 2)

	requireIndexTaken(t, requireIndexAnswer(t, frames[0], c.receive(frames[0])))
	requireIndexTaken(t, requireIndexAnswer(t, frames[0], c.receive(frames[0])))
	first := requireIndexRefused(t, requireIndexAnswer(t, frames[1], c.receive(frames[1])))
	if want := "the index's built_at " + ahead; !strings.Contains(first, want) {
		t.Fatalf("the refusal is %q, want it to say %q", first, want)
	}
	if second := requireIndexRefused(t, requireIndexAnswer(t, frames[1], c.receive(frames[1]))); second != first {
		t.Fatalf("the other connection's last slice was refused %q, want the cause %q", second, first)
	}

	requireIndexTaken(t, requireIndexAnswer(t, frames[0], c.receive(frames[0])))
	if again := requireIndexRefused(t, requireIndexAnswer(t, frames[1], c.receive(frames[1]))); !strings.Contains(again, "the index's built_at "+ahead) {
		t.Fatalf("the index pushed again was refused %q, want it judged again and refused for its built_at %s", again, ahead)
	}
	if names := slices.Sorted(maps.Keys(tree(t, dir))); !slices.Equal(names, []string{".", indexMarkerName}) {
		t.Fatalf("the folder holds %v, want the marker alone", names)
	}
}
