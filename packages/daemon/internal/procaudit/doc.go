// Package procaudit holds one check of this module's tests, and the cases that show the check can
// fail: no test may list the machine's /proc, the process table of every user's processes, unless
// it gives that listing a /proc of its own or a `host-proc:` comment on its way says why it needs
// the machine's.
//
// A test that judges by what the machine's /proc holds passes or fails by what else runs on the
// machine. On a machine several users share, the launcher's git lock sweep keeps every lock while a
// git or jj of another user runs whose open files it cannot read, and two of its tests failed on
// such a machine while they passed in CI. A test's result must not depend on processes it did not
// start.
package procaudit
