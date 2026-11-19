//go:build !race

package daemon

// raceEnabled reports whether the package was built with the race detector. What reads it is the
// sweep's field test (podupdate_sweep_contract_test.go): in the plain build it also sets each pair
// of a claim's fields alone on every kind of claim it tries, and under -race it sets no pair alone,
// at a cost the race step's bound holds; the race build's part is the ask and its publish beside
// the machines' own work, which every other part of the test holds. daemon-go's step of what the
// race build cannot run runs that test plain by name (.github/scripts/ci_job_reads.py,
// DAEMON_RACE_IMPOSSIBLE_SCRIPT).
const raceEnabled = false
