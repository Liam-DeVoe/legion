//go:build !unix

package cmdtest

// removeBuildsOfEndedProcesses removes nothing here: on this platform cmdtest cannot tell whose a
// directory is or whether its process runs, so a directory that go test's alarm left stays.
func removeBuildsOfEndedProcesses() {}
