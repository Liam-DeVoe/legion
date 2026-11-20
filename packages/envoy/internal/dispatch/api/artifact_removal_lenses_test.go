package api

import (
	"bytes"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/refs"
)

// Removing a document takes every lens on it to gone, and all that stands under each, in the
// transaction that deletes the document's row (spec v59 R5, c-settle): a lens on a document that is
// no more would read live with nothing to stand on. On c-settle's rig (settleRig), alice makes a
// document of the spec's issue by typing a title and writes nothing in it, invokes a trade-off
// analysis LD on it with one finding FD, and the second agent invokes a lens LH on FD, reserved for
// itself, which alice may not see. Her DELETE /api/v1/artifacts/{id} answers 204, and the row of
// each of LD, FD and LH reads gone; her GET /api/v1/lenses?on= LD and GET /api/v1/references?to=
// LD, and the second agent's same two reads of LH, each answer what the read of a lens nobody wrote
// answers, 404 NOT_FOUND, byte for byte once the id is substituted. No lens stands on a block of
// the document: the route removes only a document whose one version is empty, which holds no
// block.
func TestRemovingADocumentTakesEveryLensOnItGone(t *testing.T) {
	settle := newSettleRig(t)
	document := emptyDocument(t, settle.handler, settle.issue.Key, "Notes nobody wrote.md")
	address := "dispatch://" + settle.issue.Key + "/artifact/" + document.Slug
	invoke := func(name string, as sender, body map[string]any) lensInvoked {
		t.Helper()
		response := as(http.MethodPost, "/api/v1/issues/"+settle.issue.Key+"/lenses", body)
		if response.Code != http.StatusCreated {
			t.Fatalf("%s: status=%d body=%s", name, response.Code, response.Body.String())
		}
		return decodeBody[lensInvoked](t, response)
	}
	ld := invoke("alice's trade-off analysis LD on the document", settle.asAlice, map[string]any{
		"on": address, "kind": "trade-off", "findings": []string{"FD, the one finding of LD."},
	})
	fd := ld.Findings[0]
	lh := invoke("the second agent's lens LH on FD", settle.asSecond, map[string]any{
		"on": fd.Address, "kind": "trade-off", "visibility": graphstore.VisibilityAuthor,
	})

	removedDocument(t, settle.handler, document.ID)
	for _, node := range []string{ld.Address, fd.Address, lh.Address} {
		if state := storedNodeOf(t, settle.database, node).State; state != graphstore.StateGone {
			t.Errorf("after DELETE /api/v1/artifacts/%s, %s reads %s, want gone with the document it stood on", document.ID, node, state)
		}
	}

	nobody := uuid.NewString()
	for _, read := range []struct {
		reader string
		as     sender
		lens   lensInvoked
	}{{"alice", settle.asAlice, ld}, {"the second agent", settle.asSecond, lh}} {
		for _, route := range []string{"/api/v1/lenses?on=", "/api/v1/references?to="} {
			own := read.as(http.MethodGet, route+url.QueryEscape(read.lens.Address), nil)
			nobodys := read.as(http.MethodGet, route+url.QueryEscape(refs.StoreRef(graphstore.KindLens, nobody)), nil)
			if own.Code != http.StatusNotFound || nobodys.Code != http.StatusNotFound ||
				!bytes.Equal(own.Body.Bytes(), bytes.ReplaceAll(nobodys.Body.Bytes(), []byte(nobody), []byte(read.lens.ID))) {
				t.Errorf("after DELETE /api/v1/artifacts/%s, %s's GET %s%s answers %d %s, and of a lens nobody wrote %d %s: want one 404 NOT_FOUND once the id is substituted",
					document.ID, read.reader, route, read.lens.Address, own.Code, own.Body.String(), nobodys.Code, nobodys.Body.String())
			}
		}
	}
}
