package evals

import (
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/delivery/measures"
)

// TestEveryJudgeADeliveryTargetTakesIsStatedByTheDeliveryAnswers (R5): a person may name any
// statistic an eval's read states as a judge of its target, and the measures answer and the
// Pipeline judge the six delivery targets' figures by the same targets (VerdictOf). So for each
// statistic each series of those six evals states in the eval route (FoldStatistics), a register
// write naming it as a judge is either taken, and then the measures answer states a value of it
// over a window that measured everything (ResultStatistics), as the Pipeline does of its two lead
// times (LeadTimeStatistics), or refused EVAL_FIELD_INVALID naming target_judges[0], since no
// delivery answer could judge it: a judge the eval route judged while a delivery answer left it
// null would be a target written and then ignored there.
func TestEveryJudgeADeliveryTargetTakesIsStatedByTheDeliveryAnswers(t *testing.T) {
	failures := measures.ChangeFailureRate{
		PerPR: measures.FlagCounts{Confirmed: 1, Pending: 1, Rejected: 1, Reverts: 1, Total: 9, Rate: 0.1},
		PerDeploy: measures.DeployFlagCounts{
			FlagCounts:         measures.FlagCounts{Confirmed: 1, Pending: 1, Rejected: 1, Reverts: 1, Total: 8, Rate: 0.125},
			ConfirmedOrPending: 2, UpperBoundRate: 0.25,
		},
	}
	spread := measures.Spread{MedianMinutes: new(30.0), P90Minutes: new(50.0), MaxMinutes: new(70.0)}
	result := measures.Result{
		DeployFrequency:   measures.DeployFrequency{SuccessfulDeploys: 14, DeploysWithPRs: 7, PerDay: 2, WithPRsPerDay: 1},
		LeadTime:          measures.LeadTime{MergeToProduction: spread, OpenedToMerge: spread},
		ChangeFailureRate: &failures,
		DeployRunSuccess:  measures.DeployRunSuccess{Concluded: 10, ReachedProduction: 9, Cancelled: 1, Rate: new(0.9)},
	}
	result.LeadTime.MergeToProduction.N, result.LeadTime.OpenedToMerge.N = 5, 5
	answer := ResultStatistics(result, 2)
	pipeline := LeadTimeStatistics(measures.LeadTimeCell{N: 5, MedianMinutes: new(30.0), P90Minutes: new(50.0), MaxMinutes: new(70.0)})
	taken, refused := 0, 0
	for _, slug := range DeliveryTargetSlugs {
		index := slices.IndexFunc(catalogue, func(entry catalogueEntry) bool { return entry.definition.Slug == slug })
		if index < 0 {
			t.Fatalf("the catalogue holds no %s", slug)
		}
		entry := catalogue[index]
		read := FoldStatistics(entry.definition, entry.series, nil, Range{})
		for _, series := range entry.series {
			for _, stat := range slices.Sorted(maps.Keys(read[series.Series])) {
				judge := series.Series + "." + stat
				err := validateRegistration(Registration{Slug: slug, TargetJudges: Set([]string{judge})})
				if err != nil {
					var refusal *Error
					if !errors.As(err, &refusal) || refusal.Code != CodeFieldInvalid || refusal.Field != "target_judges[0]" {
						t.Errorf("%s judged by %s: the register write answers %v, want it taken or refused %s naming target_judges[0]", slug, judge, err, CodeFieldInvalid)
					}
					refused++
					t.Logf("%s judged by %s: refused, %v", slug, judge, err)
					continue
				}
				taken++
				if answer[slug][series.Series][stat] == nil {
					t.Errorf("%s judged by %s: the register write takes it, and the measures answer states no value of it", slug, judge)
				}
				if (slug == SlugMergeToProduction || slug == SlugPROpenToMerge) && pipeline[stat] == nil {
					t.Errorf("%s judged by %s: the register write takes it, and the Pipeline states no value of it", slug, judge)
				}
			}
		}
	}
	if taken == 0 {
		t.Fatal("no judge of the six delivery targets was taken, so this test examined nothing")
	}
	t.Logf("%d judges of the six delivery targets taken and stated by the delivery answers, %d refused", taken, refused)
}
