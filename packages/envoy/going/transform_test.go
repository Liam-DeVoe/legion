package going_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/going"
)

// transformIssues is the rows of one list read of a run: seven issues of DEMO, with a status, an
// estimate (one with none), the time each closed (two still open) and their labels.
var transformIssues = []map[string]any{
	{"key": "DEMO-1", "status": "done", "estimate": 3, "closed_at": "2026-11-02T10:00:00Z", "labels": []string{"bug"}},
	{"key": "DEMO-2", "status": "done", "estimate": 5, "closed_at": "2026-11-04T09:00:00Z", "labels": []string{"bug", "ui"}},
	{"key": "DEMO-3", "status": "done", "estimate": 2, "closed_at": "2026-11-10T12:00:00Z", "labels": []string{"ui"}},
	{"key": "DEMO-4", "status": "in_progress", "estimate": 8, "closed_at": nil, "labels": []string{}},
	{"key": "DEMO-5", "status": "todo", "estimate": nil, "closed_at": nil, "labels": []string{"bug"}},
	{"key": "DEMO-6", "status": "done", "estimate": 1, "closed_at": "2026-11-15T23:00:00Z", "labels": []string{}},
	{"key": "DEMO-7", "status": "done", "estimate": 0.5, "closed_at": "2026-11-12T08:00:00Z", "labels": []string{"ui"}},
}

// issuePage is the saved result of step, call 1: the issues of DEMO from offset, at most limit of
// them, as the issue list answers a paged read, {issues, total, limit, offset}.
func issuePage(t *testing.T, step, offset, limit int) going.Result {
	t.Helper()
	end := min(offset+limit, len(transformIssues))
	body, err := json.Marshal(map[string]any{"issues": transformIssues[offset:end], "total": len(transformIssues), "limit": limit, "offset": offset})
	if err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(map[string]any{"project": "DEMO", "limit": limit, "offset": offset})
	return going.Result{Step: step, Call: 1, Tool: "dispatch_issues", Arguments: arguments, Status: 200, Body: body}
}

// draw parses spec and draws it from results, answering its rows as JSON.
func draw(t *testing.T, spec string, results ...going.Result) (string, error) {
	t.Helper()
	block, err := going.ParseRunBlock(json.RawMessage(spec))
	if err != nil {
		return "", err
	}
	data, err := block.Draw(results)
	if err != nil {
		return "", err
	}
	rows, err := json.Marshal(data.Rows)
	if err != nil {
		t.Fatal(err)
	}
	return string(rows), nil
}

// TestEachTransformMakesItsNumber: each of the eight steps of a chain, and a chain of them, makes
// the rows a block draws from a run's saved results (settlement 9, R3), and every number in them
// is one a result held or a step made. A spec that carries a number is refused, and so is a chain
// over a result the server cut or over a list read the run did not read to its last page, each
// saying the data was not whole.
func TestEachTransformMakesItsNumber(t *testing.T) {
	whole := func(t *testing.T) going.Result { return issuePage(t, 2, 0, 50) }
	for _, row := range []struct {
		name, spec, want string
	}{
		{
			name: "filter",
			spec: `{"kind":"table","title":"Done","steps":[{"step":2,"call":1}],"chain":[{"op":"filter","field":"status","is":"done"}],"columns":["key"]}`,
			want: `[{"key":"DEMO-1"},{"key":"DEMO-2"},{"key":"DEMO-3"},{"key":"DEMO-6"},{"key":"DEMO-7"}]`,
		},
		{
			name: "group",
			spec: `{"kind":"table","title":"Weeks","steps":[{"step":2,"call":1}],"chain":[{"op":"filter","field":"closed_at","present":true},{"op":"group","field":"closed_at","per":"week"}],"columns":["week"]}`,
			want: `[{"week":"2026-11-02"},{"week":"2026-11-09"}]`,
		},
		{
			name: "count",
			spec: `{"kind":"table","title":"By status","steps":[{"step":2,"call":1}],"chain":[{"op":"group","field":"status"},{"op":"count"}],"columns":["status","count"]}`,
			want: `[{"count":5,"status":"done"},{"count":1,"status":"in_progress"},{"count":1,"status":"todo"}]`,
		},
		{
			name: "sum",
			spec: `{"kind":"table","title":"Estimates","steps":[{"step":2,"call":1}],"chain":[{"op":"group","field":"status"},{"op":"sum","field":"estimate"}],"columns":["status","sum"]}`,
			want: `[{"status":"done","sum":11.5},{"status":"in_progress","sum":8},{"status":"todo","sum":0}]`,
		},
		{
			name: "mean",
			spec: `{"kind":"metric","title":"Mean estimate","steps":[{"step":2,"call":1}],"chain":[{"op":"mean","field":"estimate"}],"field":"mean"}`,
			want: `[{"mean":3.25}]`,
		},
		{
			name: "min",
			spec: `{"kind":"metric","title":"Least estimate","steps":[{"step":2,"call":1}],"chain":[{"op":"min","field":"estimate"}],"field":"min"}`,
			want: `[{"min":0.5}]`,
		},
		{
			name: "max",
			spec: `{"kind":"metric","title":"Greatest estimate","steps":[{"step":2,"call":1}],"chain":[{"op":"max","field":"estimate"}],"field":"max"}`,
			want: `[{"max":8}]`,
		},
		{
			name: "sort",
			spec: `{"kind":"table","title":"Largest first","steps":[{"step":2,"call":1}],"chain":[{"op":"sort","field":"estimate","order":"desc"}],"columns":["key"]}`,
			want: `[{"key":"DEMO-4"},{"key":"DEMO-2"},{"key":"DEMO-1"},{"key":"DEMO-3"},{"key":"DEMO-6"},{"key":"DEMO-7"},{"key":"DEMO-5"}]`,
		},
		{
			name: "a chain of filter, group, count and sort draws the issues closed each week",
			spec: `{"kind":"chart","title":"Issues closed each week","steps":[{"step":2,"call":1}],"chain":[` +
				`{"op":"filter","field":"closed_at","present":true},{"op":"group","field":"closed_at","per":"week"},{"op":"count"},{"op":"sort","field":"week","order":"desc"}],` +
				`"x":"week","y":"count"}`,
			want: `[{"count":3,"week":"2026-11-09"},{"count":2,"week":"2026-11-02"}]`,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			got, err := draw(t, row.spec, whole(t))
			if err != nil {
				t.Fatalf("the block was refused: %v", err)
			}
			if got != row.want {
				t.Errorf("the block draws %s, want %s", got, row.want)
			}
		})
	}

	t.Run("a block spec that carries a number is refused", func(t *testing.T) {
		const counted = `{"kind":"metric","title":"Issues closed","steps":[{"step":2,"call":1}],"chain":[{"op":"filter","field":"status","is":"done"},{"op":"count"}],"field":"count"}`
		if got, err := draw(t, counted, whole(t)); err != nil || got != `[{"count":5}]` {
			t.Fatalf("the spec with no number drew %s, %v; want the server's count of 5", got, err)
		}
		for name, spec := range map[string]string{
			"a number in the chain": strings.Replace(counted, `"is":"done"`, `"is":5`, 1),
			"a number of its own":   strings.Replace(counted, `"field":"count"}`, `"field":"count","rows":[{"count":42}]}`, 1),
			"a number in its title": strings.Replace(counted, `"title":"Issues closed"`, `"title":"42 issues closed"`, 1),
		} {
			_, err := going.ParseRunBlock(json.RawMessage(spec))
			if err == nil || !strings.Contains(err.Error(), "holds no number") {
				t.Errorf("a spec with %s was answered %v, want it refused for holding a number", name, err)
			}
		}
	})

	t.Run("a chain over a cut result is refused and says so", func(t *testing.T) {
		cut := whole(t)
		cut.Body = cut.Body[:len(cut.Body)/2]
		cut.NotWhole = "was cut by the server at 1048576 bytes, and what it held past them is not saved"
		_, err := draw(t, `{"kind":"metric","title":"Issues","steps":[{"step":2,"call":1}],"chain":[{"op":"count"}],"field":"count"}`, cut)
		if err == nil || !strings.Contains(err.Error(), "the data was not whole") || !strings.Contains(err.Error(), "cut by the server") {
			t.Fatalf("a chain over a cut result was answered %v, want it refused, saying the data was not whole because the server cut it", err)
		}
	})

	t.Run("a chain over a list read not read to its last page is refused", func(t *testing.T) {
		const count = `{"kind":"metric","title":"Issues","steps":[%s],"chain":[{"op":"count"}],"field":"count"}`
		first, second, last := issuePage(t, 2, 0, 3), issuePage(t, 4, 3, 3), issuePage(t, 6, 6, 3)
		for name, steps := range map[string]string{
			"its first page":         `{"step":2,"call":1}`,
			"its first two pages":    `{"step":2,"call":1},{"step":4,"call":1}`,
			"its first and its last": `{"step":2,"call":1},{"step":6,"call":1}`,
		} {
			_, err := draw(t, strings.Replace(count, "%s", steps, 1), first, second, last)
			if err == nil || !strings.Contains(err.Error(), "the data was not whole") {
				t.Errorf("a chain over %s of seven issues was answered %v, want it refused, saying the data was not whole", name, err)
			}
		}
		got, err := draw(t, strings.Replace(count, "%s", `{"step":2,"call":1},{"step":4,"call":1},{"step":6,"call":1}`, 1), first, second, last)
		if err != nil || got != `[{"count":7}]` {
			t.Errorf("a chain over every page drew %s, %v; want the count of all 7", got, err)
		}
	})
}
