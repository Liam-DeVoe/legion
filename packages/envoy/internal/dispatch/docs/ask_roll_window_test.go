package docs

import (
	"context"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// The fleet guard reads another build by the schema version each live process registered, never
// by the commits among themselves: a process below this build's version is one whose pages read an
// ask's state and answer from its block. So a fleet of base processes alone, as the registry
// stands before this process registers or after its heartbeat lapses, reads two builds; a fleet of
// two commits that both read this build's version, as a later release deployed over this one
// serves, reads one; and a process that registers no schema number, a build older than the fleet
// columns, reads two builds wherever it stands. A registry holding no process, or this build
// alone, reads one.
func TestTheFleetGuardReadsAnotherBuildByTheSchemaVersionTheProcessesRegister(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	// version nil is a row that registers no schema number, which holds the column's default.
	type process struct {
		commit  string
		version *int
	}
	current, base := pmdoc.SchemaVersion(), baseSchemaVersion
	for _, test := range []struct {
		name      string
		fleet     []process
		twoBuilds bool
	}{
		{"no process registered", nil, false},
		{"this build alone", []process{{"this-release", &current}}, false},
		{"base processes alone", []process{{"the-release-before", &base}, {"the-release-before", &base}}, true},
		{"this build beside a base process", []process{{"this-release", &current}, {"the-release-before", &base}}, true},
		{"two commits both at this build's schema version", []process{{"this-release", &current}, {"the-release-after", &current}}, false},
		{"a process that registers no schema number alone", []process{{"the-release-before", nil}}, true},
		{"a process that registers no schema number beside this build", []process{{"this-release", &current}, {"an-older-release", nil}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := database.Pool.Exec(ctx, `delete from server_processes`); err != nil {
				t.Fatalf("empty the registry: %v", err)
			}
			for _, process := range test.fleet {
				var err error
				if process.version == nil {
					_, err = database.Pool.Exec(ctx, `
						insert into server_processes (instance, commit, heartbeat_at)
						values (gen_random_uuid(), $1, now() + interval '1 hour')`, process.commit)
				} else {
					_, err = database.Pool.Exec(ctx, `
						insert into server_processes (instance, commit, schema_version, schema_creatable, heartbeat_at)
						values (gen_random_uuid(), $1, $2, $2, now() + interval '1 hour')`, process.commit, *process.version)
				}
				if err != nil {
					t.Fatalf("register a process of %s: %v", process.commit, err)
				}
			}
			twoBuilds, err := anotherBuildServes(ctx, database.Pool)
			if err != nil {
				t.Fatalf("read the fleet: %v", err)
			}
			if twoBuilds != test.twoBuilds {
				t.Fatalf("anotherBuildServes = %t, want %t", twoBuilds, test.twoBuilds)
			}
		})
	}
}
