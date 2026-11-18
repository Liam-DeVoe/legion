package cases

import (
	"os"
	"strings"
	"testing"
)

// It lists the machine's /proc itself and judges by the processes it finds: NOT GIVEN.
func TestCountsTheMachinesGits(t *testing.T) {
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		comm, _ := os.ReadFile("/proc/" + entry.Name() + "/comm")
		if strings.TrimSpace(string(comm)) == "git" {
			t.Fatal("a git runs on this machine")
		}
	}
}

// host-proc: it reads only whether /proc can be listed, which no other process changes.
func TestSaysWhyItNeedsTheMachine(t *testing.T) {
	if _, err := os.ReadDir("/proc"); err != nil {
		t.Skip(err)
	}
}

// It points scan at a root of its own first: OWN ROOT.
func TestGivesTheScanARootOfItsOwn(t *testing.T) {
	ownRoot(t)
	scan()
}

// It scans the default root, the machine's: NOT GIVEN.
func TestScansWithTheDefaultRoot(t *testing.T) {
	scan()
}

// Its first subtest gives scan a root of its own and its second does not: the first is OWN ROOT,
// the second NOT GIVEN, and the test itself, which scans only through them, is not listed.
func TestSubtests(t *testing.T) {
	t.Run("own root", func(t *testing.T) {
		ownRoot(t)
		scan()
	})
	t.Run("default root", func(t *testing.T) {
		scan()
	})
}

// It never sets stand, so serve's gate keeps it out: not listed.
func TestNeverStandsIn(t *testing.T) {
	serve()
}

// It sets stand and gives scan no root: NOT GIVEN.
func TestStandsInWithNoRoot(t *testing.T) {
	standIn(t)
	serve()
}

// It sets stand and gives scan a root of its own: OWN ROOT.
func TestStandsInWithARoot(t *testing.T) {
	standIn(t)
	ownRoot(t)
	serve()
}

func ownRoot(t *testing.T) {
	previous := procRoot
	procRoot = t.TempDir()
	t.Cleanup(func() { procRoot = previous })
}

func standIn(t *testing.T) {
	previous := stand
	stand = func() bool { return true }
	t.Cleanup(func() { stand = previous })
}
