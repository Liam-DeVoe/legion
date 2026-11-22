package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// R5: an erase deletes or rewrites every row that holds the erased text and derives from the
// node, and its listing names each row it leaves. An ask block's options are paragraphs of the
// block, and the settlement writes the ask from them: asks.options holds each option split into
// its label and its description, and ask_blocks, ask_block_texts, the ask.opened event, the ask's
// embedding and its keyword version hold the description too. The test writes an ask block
// through the edits route as a person, with an option whose description holds a secret, waits
// until the settlement has written the ask from it (the control), and erases the option's
// paragraph. Every column that still holds the secret afterwards must be named by the listing as
// a row the run leaves: a search for the paragraph's text as written, `REST: <description>`,
// finds none of the ask's rows, which hold it split.
func TestTheEraseOfAnAskOptionNamesEveryRowThatKeepsItsText(t *testing.T) {
	ctx := context.Background()
	database := storetest.Open(t)
	databaseURL := database.Pool.Config().ConnString()
	server := startDispatchProcess(t, databaseURL)
	server.waitHealthy(t)
	server.call(t, http.MethodPost, "/api/v1/projects", `{"key":"ERA","name":"Erase"}`, http.StatusCreated)

	secret := eraseTestSecret()
	issue, spec := createEraseIssue(t, server, "ERA", "A transport choice", "# Spec\n\nTransport notes.\n")
	ask := ":::ask{#transport}\nWhich transport?\n\n- REST: Matches the " + secret + " platform\n- gRPC: Adds streaming\n:::"
	edit, err := json.Marshal(map[string]any{"ops": []map[string]any{{"op": "insert", "after": "end", "markdown": ask}}})
	if err != nil {
		t.Fatalf("encode the edit: %v", err)
	}
	if answer := server.send(t, http.MethodPost, "/api/v1/artifacts/"+spec+"/edits", string(edit), asUser(dispatchTestLogin)); answer.status != http.StatusOK {
		t.Fatalf("write the ask block on %s: %s", issue, answer)
	}
	var option string
	deadline := time.Now().Add(30 * time.Second)
	for {
		var asks int
		err := database.Pool.QueryRow(ctx, `select count(*) from asks where issue_key = $1 and position($2 in options::text) > 0`, issue, secret).Scan(&asks)
		if err == nil && asks == 1 {
			err = database.Pool.QueryRow(ctx, `
				select a.ref_key || '#' || t.block_id from document_block_texts t join artifacts a on a.id = t.artifact_id
				where t.artifact_id = $1::uuid and position($2 in t.text) > 0
				  and not exists (select 1 from document_blocks c where c.artifact_id = t.artifact_id and c.parent_block_id = t.block_id)
				  and exists (select 1 from graph_nodes n where n.kind = 'block' and n.id = a.ref_key || '#' || t.block_id)
				order by t.version desc limit 1`, spec, secret).Scan(&option)
			if err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the settlement wrote no ask holding the option and no block node of the option within 30s (%v), so this test would prove nothing", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	waitForSettlementOwed(t, database, spec, false)

	erasure, err := confirmErase(ctx, databaseURL, "block", option, time.Millisecond)
	if err != nil || !erasure.Applied {
		t.Fatalf("erase %s: applied %t, %v", option, erasure != nil && erasure.Applied, err)
	}
	waitForSettlementOwed(t, database, spec, false)
	places, searched := columnsHolding(t, database, secret)
	var unnamed []string
	for _, place := range places {
		// The term statistics count the words of every item of the project, so an ask the run
		// leaves keeps its lexemes counted there; the statistics are not a row of the option.
		if strings.HasPrefix(place, "public.search_term_stats.") {
			continue
		}
		table := place[:strings.LastIndex(place[:strings.Index(place, " ")], ".")]
		if !listed(erasure, "leave "+table+" ") {
			unnamed = append(unnamed, place)
		}
	}
	if len(unnamed) > 0 {
		t.Errorf("after the erase of the option %s the secret is still in %s (%d columns searched), and the listing names none of those rows as a row the run leaves:\n%s",
			option, strings.Join(unnamed, ", "), searched, strings.Join(erasure.Listing, "\n"))
	}
}
