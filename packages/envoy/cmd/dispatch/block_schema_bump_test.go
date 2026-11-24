package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/pmdoc/schemanodes"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// specChangeMigration is the name every type migration of the change whose approvals write the
// approved spec takes, <timestamp>_spec_change_<what>.up.sql: the type migrations this branch adds
// are the ones of store.TypeMigrations it names. The type migrations before them, the last of
// which (<timestamp>_issue_tree_<what>.up.sql) moved the block schema to version 7, are the ones
// before this branch's.
var specChangeMigration = regexp.MustCompile(`^[0-9]{14}_spec_change_[0-9a-z_]+\.up\.sql$`)

// The branch moves the block schema one version, once (dispatch://INFRA-1253/spec R21): its
// elements stand at the version after the one the type migrations before it carry, however many
// type migrations it adds. The test reads the version blocks.json gives at this head and the one
// the store's type nodes build once every migration has run, which must agree, and then the
// version the nodes build on a store this tree's runner migrated through every migration numbered
// below this branch's first type migration, which is the version the store held before this
// branch's type migrations: a type migration written after the store's backfill runs once and
// cannot run again over its own rows (supertag_nodes_test.go's typeMigrations), so the earlier
// store is migrated to that point rather than rolled back to it. A second bump - blocks.json, or a
// type migration of this branch, moving past the version after that one - is one step too many,
// and so is none.
func TestTheBlockSchemaMovesOneVersionPastTheTypeMigrationsBeforeThisBranch(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	file := readBlockSchemaFile(t)

	head, err := schemanodes.Read(ctx, database.Pool)
	if err != nil {
		t.Fatalf("build the schema from the store's nodes at this head: %v", err)
	}
	if head.Version != file.Version {
		t.Fatalf("the store's type nodes build version %d at this head, and %s is at version %d: a type migration of this branch and the file disagree",
			head.Version, blockSchemaFile, file.Version)
	}

	var before, ours []string
	for _, name := range store.TypeMigrations {
		if specChangeMigration.MatchString(name) {
			ours = append(ours, name)
			continue
		}
		before = append(before, name)
	}
	if len(ours) == 0 || len(before) == 0 {
		t.Fatalf("store.TypeMigrations names %d type migration(s) of this branch and %d before them, want at least one of each, or nothing moves the block schema's version", len(ours), len(before))
	}
	// The runner migrates through an embedded version, so the earlier store stops at the last type
	// migration before this branch's first: every migration after it and below this branch's is
	// no type migration, and moves no version.
	through, err := strconv.Atoi(before[len(before)-1][:14])
	if err != nil {
		t.Fatalf("read the version of %s: %v", before[len(before)-1], err)
	}
	if ours[0] <= before[len(before)-1] {
		t.Fatalf("this branch's first type migration %s is not after %s, the last before it", ours[0], before[len(before)-1])
	}
	earlierStore := storetest.OpenFrom(t, fmt.Sprintf("this tree's runner through %d, below %s", through, ours[0]), func(databaseURL string) error {
		database, err := store.Open(ctx, databaseURL)
		if err != nil {
			return fmt.Errorf("open the database: %w", err)
		}
		defer database.Pool.Close()
		return database.MigrateThrough(ctx, through)
	})
	earlier, err := schemanodes.Read(ctx, earlierStore.Pool)
	if err != nil {
		t.Fatalf("build the schema from the store's nodes as the type migrations before this branch's leave them: %v", err)
	}
	t.Logf("the type migrations before this branch's (%d of them) carry version %d; this branch's %v and %s carry version %d",
		len(before), earlier.Version, ours, blockSchemaFile, file.Version)
	if file.Version != earlier.Version+1 {
		t.Fatalf("%s is at version %d and the type migrations before this branch's carry version %d: the branch moves the block schema %d steps, want one",
			blockSchemaFile, file.Version, earlier.Version, file.Version-earlier.Version)
	}
}
