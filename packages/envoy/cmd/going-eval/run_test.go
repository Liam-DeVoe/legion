package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/going/eval"
)

// TestCountCallsFoldsCallToolAndCountsReferenceReads: a try's tool steps count each tool by name,
// a call_tool call under the tool it names and one that names none under call_tool; a read of a
// block's reference, by dispatch:// or by a dashboard URL under the try's server, and a call_tool
// of dispatch_read_issue_references or of dispatch_read_document_references each read the
// reference graph, and a read of an issue, a search, a plan's write and a step that is no call do
// not. The try's --answers-out line carries both.
func TestCountCallsFoldsCallToolAndCountsReferenceReads(t *testing.T) {
	const serverURL = "http://127.0.0.1:4100"
	step := func(tool string, arguments map[string]any) []byte {
		body, err := json.Marshal(map[string]any{
			"call":   map[string]any{"id": "call_1", "tool": tool, "arguments": arguments},
			"result": "a result",
			"error":  false,
		})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	callTool := func(tool string, arguments map[string]any) []byte {
		return step("call_tool", map[string]any{"tool": tool, "arguments": arguments})
	}
	steps := [][]byte{
		step("read", map[string]any{"ref": "dispatch://DEMO-1/spec#b-x"}),
		step("read", map[string]any{"ref": serverURL + "/issues/DEMO-1/spec#b-x"}),
		callTool("dispatch_read_issue_references", map[string]any{"issue": "DEMO-1"}),
		callTool("dispatch_read_document_references", map[string]any{"document": "0190c7d2-1111-7222-8333-444444444444"}),
		step("read", map[string]any{"ref": "dispatch://DEMO-1"}),
		step("read", map[string]any{"issue": "DEMO-1"}),
		step("search", map[string]any{"query": "quernstone"}),
		callTool("dispatch_search", map[string]any{"query": "quernstone"}),
		step("find_tools", map[string]any{"need": "what an issue references"}),
		step("call_tool", map[string]any{"arguments": map[string]any{}}),
		step("dispatch_comment", map[string]any{"issue": "DEMO-1", "body": "Done."}),
		[]byte(`{"result": "a step that names no call"}`),
	}

	tools, reads := countCalls(steps, serverURL)
	want := map[string]int{
		"read": 4, "search": 1, "find_tools": 1, "call_tool": 1, "dispatch_comment": 1, "dispatch_search": 1,
		"dispatch_read_issue_references": 1, "dispatch_read_document_references": 1,
	}
	if !maps.Equal(tools, want) {
		t.Errorf("tools = %v, want %v", tools, want)
	}
	if reads != 4 {
		t.Errorf("reference reads = %d, want 4: two reads of a block and the two references tools", reads)
	}

	line, err := json.Marshal(tryResult{Task: "find-references", Tools: tools, ReferenceReads: reads})
	if err != nil {
		t.Fatal(err)
	}
	for _, held := range []string{`"tools":{`, `"dispatch_read_issue_references":1`, `"reference_reads":4`} {
		if !bytes.Contains(line, []byte(held)) {
			t.Errorf("the --answers-out line %s holds no %s", line, held)
		}
	}
}

// TestTheReportListsTheTasksThatReadTheReferenceGraph: the report's section names each task any of
// whose tries read the reference graph, with how many of its tries did, and the --tasks that runs
// them again; a task none of whose tries read it is not named.
func TestTheReportListsTheTasksThatReadTheReferenceGraph(t *testing.T) {
	tasks := []eval.Task{{ID: "trace-links"}, {ID: "close-issue"}, {ID: "find-backlinks"}}
	byTask := map[string][]tryResult{
		"trace-links":    {{Try: 1, ReferenceReads: 2}, {Try: 2}, {Try: 3, ReferenceReads: 1}},
		"close-issue":    {{Try: 1}, {Try: 2}, {Try: 3}},
		"find-backlinks": {{Try: 1, ReferenceReads: 1}, {Try: 2, ReferenceReads: 3}, {Try: 3, ReferenceReads: 1}},
	}
	var report strings.Builder
	writeReferenceReads(&report, tasks, byTask)
	text := report.String()
	for _, held := range []string{
		"## Reads of the reference graph",
		"GET /api/v1/references",
		"which a change to those routes reaches: 2.",
		"- trace-links: 2 of 3 tries\n",
		"- find-backlinks: 3 of 3 tries\n",
		"Run them again with `--tasks trace-links,find-backlinks`.",
	} {
		if !strings.Contains(text, held) {
			t.Errorf("the section holds no %q:\n%s", held, text)
		}
	}
	if strings.Contains(text, "close-issue") {
		t.Errorf("the section names close-issue, none of whose tries read the graph:\n%s", text)
	}
}
