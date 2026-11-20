package docs

import (
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// editShapesMainStores are texts main 1c7c8253 stores through an agent's edit, each holding an
// email address beside markup or another link. The first ten are shapes the deep review of
// INFRA-4224 generated at its fixed seed (named by their index), the last is a text of the shape
// table INFRA-4224's email rule kept (`*<http://example.com>*_%c@d.co_`).
var editShapesMainStores = []struct{ name, markdown string }{
	{"generated shape 1794", "x  \n*'~<http://a.b>Z?@example.com~*_!\\.@a_b.c%c@d.co_\n"},
	{"generated shape 4781", "- a\n  - ).www.a.b&-@e.co&#45;c@d.co*&#x2D;x@y.co/\\-x@y.co@e.co--**a.b\\-\\-_*\\.@a.b.c.test`*\n"},
	{"generated shape 6772", ":::callout{#c1 kind=\"note\" title=\"T\"}\n_`c`**éhttp://&amp;\\_@example.com.&amp;c@d.co_** _e_x@y.coZ\\.@a.b%c@d.co***[x@y.co](mailto:x@y.co)&#64;ftp://!#\\-@ex-ample.co.uk.www.q.com*\n:::\n"},
	{"generated shape 9161", "1. *}**b**!&#45;hosts@example.com~**\\_=\\-+@ex-ample.co.uk~_\\*}_www.&#45;@www.e.co._\n"},
	{"generated shape 9382", "> - `x` =[x@y.co](mailto:x@y.co)_agent-hosts_@x_y.com*[a/<http://a.b>http://a.b/^/@example.com.](http://l.k)`x` a@b.co+{x@y.co/*@a.b_\n"},
	{"generated shape 11970", "> .)a.b&period;\\-#@ex-ample.co.uk/bob-smith@example.com `y`***e*)&*@a.b.c.test%c@d.co**_**q/}hostsZ@a_b.c,c@d.co_\n"},
	{"generated shape 15366", "# mailto:[x@y.co](mailto:x@y.co)b.c@example.com)***b**&period;\\-agent-hostsx@y.co@example.com&amp;c@d.co*\n"},
	{"generated shape 17501", "# \\*%'@a.b&#64;e.co*x@y.co[x@y.co](mailto:x@y.co)*www.a.bhosts@a.b/bob-smith@example.com*(\\.@x_y.com%c@d.co `y`\n"},
	{"generated shape 17667", "x\\\n_&amp;<http://a.b>_9@a.b.c.test%c@d.co_**^b.cwww.@x_y.com'c@d.co**~~ftp://&#64;x@y.cohostswww.a.b?@example.com?x~~\n"},
	{"generated shape 19977", "ref[^1]\n\n[^1]: &amp;&amp;@e.co- and y@z.co~~&x@y.co/&#x2D;www.a.b@ex-ample.co.uk/b-c@d.co~~\n"},
	{"an autolink beside an email address", "*<http://example.com>*_%c@d.co_\n"},
}

// An agent's edit that writes a text main stores is applied, and the document then stores what an
// upload of the edited document's text stores. The tester found at 8d2cff66, on real servers (POST
// /api/v1/artifacts/{id}/edits against that head's and main 1c7c8253's envoy-dispatch), that an
// insert of each generated shape was refused 400 INVALID_OP ("reads back otherwise"), where main
// applies it and stores the text an upload stores; and that an insert or a replacement of
// `*<http://example.com>*_%c@d.co_`, or a replacement with shape 1794, was applied but stored the
// autolink written bare, so `<http://example.com>` and the email link after it read back as one
// link to `http://example.com%c@d.co` (INFRA-4224 tester round 12).
func TestAnEditOfATextMainStoresStoresWhatAnUploadOfItStores(t *testing.T) {
	writer := model.Actor{Kind: "session", ID: "session-0123456789abcdef"}
	type edit struct {
		name   string
		op     func(markdown string) model.EditOp
		result func(markdown string) string
	}
	insert := edit{"an insert", func(markdown string) model.EditOp {
		return model.EditOp{Op: "insert", After: "end", Markdown: markdown}
	}, func(markdown string) string { return "Start.\n\n" + markdown }}
	replace := edit{"a replacement", func(markdown string) model.EditOp {
		return model.EditOp{Op: "replace", Find: "Start.", With: strings.TrimSuffix(markdown, "\n")}
	}, func(markdown string) string { return markdown }}
	for _, shape := range editShapesMainStores {
		edits := []edit{insert}
		if shape.name == "generated shape 1794" || shape.name == "an autolink beside an email address" {
			edits = append(edits, replace)
		}
		for _, row := range edits {
			t.Run(row.name+" of "+shape.name, func(t *testing.T) {
				_, uploaded, err := pmdoc.ParseWritten(row.result(shape.markdown), nil)
				if err != nil {
					t.Fatalf("an upload of the edited document's text is refused: %v", err)
				}
				service, artifactID := newTestService(t)
				service.settle = time.Hour // the test runs each settlement itself
				seedServiceText(t, service, artifactID, "Start.\n")
				settleCurrentGeneration(t, service, artifactID)
				if _, err := joinedApplyOps(service, artifactID, []model.EditOp{row.op(shape.markdown)}, writer, nil); err != nil {
					t.Fatalf("the edit was refused: %v", err)
				}
				settleCurrentGeneration(t, service, artifactID)
				if err := service.roomFailure(artifactID); err != nil {
					t.Fatalf("the settlement failed the room: %v", err)
				}
				if got := latestVersionMarkdown(t, service, artifactID); got != uploaded.Markdown {
					t.Errorf("the edit stored %q, where an upload of the same text stores %q", got, uploaded.Markdown)
				}
			})
		}
	}
}
