package api

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// writtenColumnTriggers record, in written_columns, each writtenColumn a statement writes: an
// update that names issues.repo, issues.parent_key or issues.project_key in its SET list, whatever
// value it sets (`update of`), an insert of an issue (its project always, its repository and its
// parent where it names one), and any insert, update or delete of a repo_projects row that is or
// was its project's default. projects holds no repository column in this build, so no trigger is
// on it.
const writtenColumnTriggers = `
	create table written_columns (id bigserial primary key, written text not null);
	create function record_written_column() returns trigger language plpgsql as $$
	begin
		insert into written_columns (written) values (tg_argv[0]);
		return null;
	end $$;
	create function record_written_issue() returns trigger language plpgsql as $$
	begin
		insert into written_columns (written) values ('issues.project');
		if new.repo is not null then insert into written_columns (written) values ('issues.repo'); end if;
		if new.parent_key is not null then insert into written_columns (written) values ('issues.parent'); end if;
		return null;
	end $$;
	create function record_written_default() returns trigger language plpgsql as $$
	begin
		if (tg_op <> 'DELETE' and new.is_default) or (tg_op <> 'INSERT' and old.is_default) then
			insert into written_columns (written) values ('repo_projects.default');
		end if;
		return null;
	end $$;
	create trigger written_issue_repo after update of repo on issues for each row execute function record_written_column('issues.repo');
	create trigger written_issue_parent after update of parent_key on issues for each row execute function record_written_column('issues.parent');
	create trigger written_issue_project after update of project_key on issues for each row execute function record_written_column('issues.project');
	create trigger written_issue after insert on issues for each row execute function record_written_issue();
	create trigger written_default after insert or update or delete on repo_projects for each row execute function record_written_default();`

// takeWrittenColumns is every column the triggers recorded since the last take, each once, sorted,
// and forgets them.
func takeWrittenColumns(t *testing.T, database *store.Store) []writtenColumn {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `delete from written_columns returning written`)
	if err != nil {
		t.Fatalf("take the written columns: %v", err)
	}
	written, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read the written columns: %v", err)
	}
	columns := make([]writtenColumn, 0, len(written))
	for _, column := range written {
		if !slices.Contains(columns, writtenColumn(column)) {
			columns = append(columns, writtenColumn(column))
		}
	}
	slices.Sort(columns)
	return columns
}

// TestEveryWriteDeclaresItsColumns (c-proxy-by-repository-id): apiRoute.Writes (writeColumns) names
// every column of the repository rule each write route changes, which the routes that record a
// repository's GitHub id are found by. Over the hidden-write walk's seed, with triggers on issues and
// repo_projects recording each such column a statement writes (writtenColumnTriggers):
//
//   - each route writeColumns names writes, through a body that sets them, every column it declares
//     and no other, and each is a write row;
//   - every write row of the table, called by carol, who sees both projects, with the request its
//     Probe already sends (writeProbes, its objects PRIV's; noProjectWrites' bodies; an empty
//     object for a row that names no object), writes no column it does not declare.
//
// Control: write issues.parent in a route that does not declare it (an `update issues set
// parent_key = parent_key` in the claim's write), and the test fails naming the route and the
// column.
func TestEveryWriteDeclaresItsColumns(t *testing.T) {
	for _, name := range slices.Sorted(maps.Keys(writeColumns)) {
		if _, ok := writeClasses[name]; !ok {
			t.Errorf("writeColumns names %s, which is no write row", name)
		}
	}
	walk := seedHiddenWrites(t)
	// The columns a route writes do not turn on who may see what it writes, and with no project
	// private a repository write reads no GitHub id (writtenRepository), which this seed's server
	// has no GitHub to answer.
	if _, err := walk.database.Pool.Exec(context.Background(), `update projects set visibility = 'public'`); err != nil {
		t.Fatalf("make every project public: %v", err)
	}
	if _, err := walk.database.Pool.Exec(context.Background(), writtenColumnTriggers); err != nil {
		t.Fatalf("record the written columns: %v", err)
	}
	created := ""
	for _, write := range []struct {
		name, path string
		body       func() any
	}{
		{"POST /api/v1/issues", "/api/v1/issues", func() any {
			return map[string]any{"project": seedPublic, "title": "Declares the columns it writes", "parent": walk.issue[seedPublic], "repo": "acme/declared", "force": true}
		}},
		{"PATCH /api/v1/issues/{key}", "", func() any {
			return map[string]any{"repo": "acme/declaredagain", "parent": walk.issue[seedPublic]}
		}},
		{"PUT /api/v1/settings/repo-projects/{owner}/{repo}", "/api/v1/settings/repo-projects/acme/declaredmap", func() any {
			return map[string]any{"project": seedPublic, "default": true}
		}},
		{"DELETE /api/v1/settings/repo-projects/{owner}/{repo}", "/api/v1/settings/repo-projects/acme/declaredmap", func() any { return nil }},
	} {
		method, _, _ := strings.Cut(write.name, " ")
		path := write.path
		if path == "" {
			path = "/api/v1/issues/" + created
		}
		response := walk.request(t, callerCarol, method, path, write.body())
		if response.Code < 200 || response.Code > 299 {
			t.Fatalf("carol: %s %s = %d %s", method, path, response.Code, strings.TrimSpace(response.Body.String()))
		}
		if method == http.MethodPost {
			created = decodeBody[struct {
				Key string `json:"key"`
			}](t, response).Key
		}
		written := takeWrittenColumns(t, walk.database)
		if !slices.Equal(written, slices.Sorted(slices.Values(writeColumns[write.name]))) {
			t.Errorf("%s, through a body that sets every column it declares, wrote %v, want exactly the %v it declares", write.name, written, writeColumns[write.name])
		}
	}

	for _, name := range slices.Sorted(maps.Keys(writeClasses)) {
		method, pattern, _ := strings.Cut(name, " ")
		path := pattern
		var body any = json.RawMessage(`{}`)
		switch probe, probed := writeProbes[name]; {
		case probed:
			path, _ = walk.fill(t, pattern, probe, true, true)
			body = nil
			if probe.Body != "" {
				body = json.RawMessage(probe.Body)
			}
		case noProjectWrites[name].Reason != "":
			path = strings.NewReplacer("{owner}", "acme", "{repo}", "probed", "{name}", "probed-group").Replace(pattern)
			body = nil
			if write := noProjectWrites[name]; write.Body != "" {
				body = json.RawMessage(write.Body)
			}
		}
		path = patternWildcard.ReplaceAllString(path, "probed")
		response := walk.request(t, callerCarol, method, path, body)
		for _, column := range takeWrittenColumns(t, walk.database) {
			if !slices.Contains(writeColumns[name], column) {
				t.Errorf("%s (%s, answered %d) wrote %s, which it does not declare in writeColumns", name, path, response.Code, column)
			}
		}
	}
}
