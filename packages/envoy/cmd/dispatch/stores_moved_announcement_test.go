package main

import (
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/cmdtest"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// storesMovedBuildStamp is the commit the build TestThisBuildAnnouncesStoresMoved boots registers
// and announces under.
const storesMovedBuildStamp = "5705ed5705ed5705ed5705ed5705ed5705ed5705"

// storesMovedCapability is the word a build that has moved the priority store and the workflow
// store into the graph store lists among the tuple kinds it announces (processes.CapabilityStoresMoved).
// It is spelled out here rather than read from that constant, because the release after this one
// reads this word out of the announcements a database holds: a rename of the constant would leave
// every announcement this build wrote naming a word that release does not look for.
const storesMovedCapability = "stores_moved"

// TestThisBuildAnnouncesStoresMoved boots a build stamped with a commit, as the image build stamps
// one (cmdtest.BuildStamped), over a migrated database, waits for the announcement its announcer
// writes once every live process serves that commit (startup.Announcer), and fails unless that
// row's tuple_kinds lists stores_moved: the capability the boot step of the release after this one
// reads before it drops the views and the triggers this build's move leaves for the release before
// (dispatch://INFRA-1098's R32). The binary's announcer takes its list from refs.TupleKinds, which
// carries processes.Capabilities. With stores_moved taken out of processes.Capabilities, the row
// lists no such word and the test fails.
func TestThisBuildAnnouncesStoresMoved(t *testing.T) {
	binary := cmdtest.BuildStamped(t, "envoy-dispatch", storesMovedBuildStamp)
	d := &decisionsDatabase{url: storetest.Open(t).Pool.Config().ConnString()}
	d.boot(t, binary)
	waitForAnnouncement(t, d, storesMovedBuildStamp)
	var kinds []string
	d.scan(t, `select tuple_kinds from server_build_announcements where commit = '`+storesMovedBuildStamp+`' order by seq desc limit 1`, &kinds)
	if !slices.Contains(kinds, storesMovedCapability) {
		t.Fatalf("the announcement of %s lists %v, which holds no %s: the release after this one drops the views and the triggers the move leaves only where the newest announcement of another build lists it",
			storesMovedBuildStamp, kinds, storesMovedCapability)
	}
	t.Logf("the announcement of %s lists %s among its %d tuple kinds", storesMovedBuildStamp, storesMovedCapability, len(kinds))
	d.stop(t)
}
