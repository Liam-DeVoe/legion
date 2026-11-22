package seedcorpus

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/sjawhar/envoy/internal/dispatch/api"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// premortemTitle is the issue the captured pre-mortem is replayed on.
const premortemTitle = "The graph model's pre-mortem, as captured"

// replayPremortem replays the captured pre-mortem: an issue of its own, written by recorderSession,
// the captured document uploaded to it under its captured name, and the document converted by the
// API's own conversion (api.ConvertPremortem, through POST .../premortem-convert) into one lens of
// kind pre-mortem with one risk directly under it for each risk the document holds, eleven, none
// under another (decision 6). No check of the corpus prevents a captured risk, so each entry of
// the conversion's map names none, with the reason.
func (s *seeder) replayPremortem(ctx context.Context, capture premortemCapture) error {
	var issue model.Issue
	if err := s.api.call(ctx, asSession(recorderSession), http.MethodPost, "/api/v1/issues", map[string]any{
		"project": s.project, "title": premortemTitle, "spec": "The issue the captured pre-mortem of the graph model is replayed on.\n", "force": true,
	}, &issue, http.StatusCreated); err != nil {
		return fmt.Errorf("replay %s: %w", premortemFile, err)
	}
	var uploaded struct {
		Artifact model.Artifact `json:"artifact"`
	}
	if err := s.api.call(ctx, asSession(recorderSession), http.MethodPost, "/api/v1/issues/"+issue.Key+"/artifacts", map[string]any{
		"name": capture.Document.Name, "content": capture.Document.Markdown,
	}, &uploaded, http.StatusCreated); err != nil {
		return fmt.Errorf("upload %s: %w", premortemFile, err)
	}
	entries := make([]api.PremortemEntry, 0, len(capture.Risks))
	for _, risk := range capture.Risks {
		entries = append(entries, api.PremortemEntry{Risk: risk.Index, Checks: []string{},
			Reason: "no check of the seeded corpus prevents this captured risk"})
	}
	var report api.PremortemReport
	if err := s.api.call(ctx, asSession(recorderSession), http.MethodPost,
		"/api/v1/issues/"+issue.Key+"/artifacts/"+uploaded.Artifact.Slug+"/premortem-convert",
		map[string]any{"prevented_by": entries}, &report); err != nil {
		return fmt.Errorf("convert %s: %w", premortemFile, err)
	}
	if report.Lens.Address == nil || len(report.Risks) != premortemRisks {
		return fmt.Errorf("the conversion of %s wrote %d risks under %v, want %d under one lens", premortemFile, len(report.Risks), report.Lens.Address, premortemRisks)
	}
	s.corpus.set("premortem.issue", issue.Key)
	s.corpus.set("premortem.lens", *report.Lens.Address)
	for _, risk := range report.Risks {
		if risk.Address == nil {
			return fmt.Errorf("the conversion of %s wrote risk %d with no address", premortemFile, risk.Number)
		}
		s.corpus.set(fmt.Sprintf("premortem.risk.%d", risk.Number), *risk.Address)
	}
	if !strings.HasPrefix(*report.Lens.Address, "dispatch://lens/") {
		return fmt.Errorf("the conversion of %s answered the lens address %s", premortemFile, *report.Lens.Address)
	}
	return nil
}

// preventedRisk is the one risk of the captured pre-mortem a second conversion's map names a check
// for, as c-lens writes its map: risk 8 names one seeded check and every other risk, risk 3 among
// them, names none.
const preventedRisk = 8

// PreventedBy is the map file `envoy-dispatch premortem-convert --prevented-by` reads for a second
// conversion of the captured pre-mortem, which the browser file and lens.sh each run on an issue of
// their own: a comment line, then one line for each risk the captured document numbers, in the
// order this seed's own conversion answered them, of three fields with one tab between them - the
// risk's number, `none` or the check address, and the reason. Risk 8 is prevented by the seeded
// check k-pass; every other risk by none. The seeded issue's key exists only once the seed has
// run, so the loader writes the map from the names it seeded, and no map file is committed.
func (c Corpus) PreventedBy() (string, error) {
	key := c.Get("model.issue")
	if key == "" {
		return "", fmt.Errorf("the corpus names no seeded issue, whose check k-pass the map names")
	}
	lines := []string{"# risk\tchecks\treason"}
	for _, name := range c.names {
		number, isRisk := strings.CutPrefix(name, "premortem.risk.")
		if !isRisk {
			continue
		}
		if number == fmt.Sprint(preventedRisk) {
			lines = append(lines, number+"\t"+verificationRef(key, "k-pass")+"\tThe seeded check that draws every answered ask covers this risk.")
		} else {
			lines = append(lines, number+"\tnone\tNo check of the seeded corpus prevents risk "+number+".")
		}
	}
	if len(lines) != premortemRisks+1 {
		return "", fmt.Errorf("the corpus names %d risks of the captured pre-mortem, want %d", len(lines)-1, premortemRisks)
	}
	return strings.Join(lines, "\n") + "\n", nil
}
