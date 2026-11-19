package api

import (
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/sjawhar/envoy/steprecord"

	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// PlanRatingContract is the daemon contract that added the rating of the plan to the completion
// that ends the retro (version.go's 168): planRating, a whole number from 1 (the plan cost the
// work) to 5 (the plan fit the work), and planReason, why, the two together. The agent that does
// the retro gives it, so the route takes the two on the completion whose phase is retro (phase.Of,
// never the role) and refuses them on every other completion and on a production check's report
// (PLAN_RATING_UNEXPECTED). A claim whose plugin declared this contract or a later one must rate
// (PLAN_RATING_REQUIRED); a plugin below it never sends the two, and its retro, where the daemon
// serves its claim (staleClaim), completes with no rating.
const PlanRatingContract = 168

// planRatingOf holds a completion's rating of the plan to PlanRatingContract's rule, and returns
// the rating it held there (nil for a completion that carries none) or the refusal. endsRetro is
// whether the completion is the one that ends the retro (phase.Of is retro), pluginContract the
// contract the claim's plugin declared at its registration (supervise.Claim.PluginContract), and
// what names the completion and its issue in a refusal. The rating is built only where each rule
// has held, so no caller reads either member of the request itself.
// Every refusal is a 400 its caller writes before anything is recorded, so the same completion
// sent again, corrected, is taken:
//
//   - PLAN_RATING_UNEXPECTED: a completion that does not end the retro names planRating or
//     planReason, an empty one included. Dropping them unread would let an agent believe it had
//     rated the plan.
//   - PLAN_RATING_REQUIRED: the completion that ends the retro names neither, from a claim whose
//     plugin declared PlanRatingContract or a later one, whose tool takes the two. A plugin before
//     it, and a claim that recorded no contract (0), never sends them and completes unrated.
//   - PLAN_RATING_INPUT: one of the two without the other, or a rating and a reason that break
//     the one rule of a rating, which Dispatch's store holds the rating to as well
//     (steprecord.CheckRating): a rating outside steprecord.RatingLeast to RatingMost, or a reason
//     that is blank or holds more than RatingReasonMax characters, counted as code points.
func planRatingOf(req HandoffCompleteRequest, endsRetro bool, pluginContract int, what string) (*record.PlanRating, *intake.Refusal) {
	if req.PlanRating == nil && req.PlanReason == nil {
		if endsRetro && pluginContract >= PlanRatingContract {
			return nil, &intake.Refusal{Status: http.StatusBadRequest, Code: "PLAN_RATING_REQUIRED", Message: fmt.Sprintf(
				"%s carries no rating of the plan, and the completion that ends the retro rates it: rate the plan the issue ran from %d (the plan cost the work) to %d (the plan fit the work) in planRating, say why in planReason, and send it again; nothing was recorded", what, steprecord.RatingLeast, steprecord.RatingMost)}
		}
		return nil, nil
	}
	if !endsRetro {
		return nil, &intake.Refusal{Status: http.StatusBadRequest, Code: "PLAN_RATING_UNEXPECTED", Message: fmt.Sprintf(
			"%s names planRating or planReason, and only the completion that ends the retro rates the plan; send it again without both, and nothing was recorded", what)}
	}
	// A rating named on the completion that ends the retro, held to the pair and then to the rule
	// of a rating. Each member is read only past the case that finds it named.
	var wrong string
	switch {
	case req.PlanRating == nil:
		wrong = "planReason comes with no planRating"
	case req.PlanReason == nil:
		wrong = fmt.Sprintf("planRating %d comes with no planReason", *req.PlanRating)
	default:
		switch steprecord.CheckRating(*req.PlanRating, *req.PlanReason) {
		case steprecord.RatingHolds:
			return &record.PlanRating{Rating: *req.PlanRating, Reason: *req.PlanReason}, nil
		case steprecord.RatingOffScale:
			wrong = fmt.Sprintf("planRating %d is outside %d to %d", *req.PlanRating, steprecord.RatingLeast, steprecord.RatingMost)
		case steprecord.RatingReasonBlank:
			wrong = "planReason is blank"
		case steprecord.RatingReasonLong:
			wrong = fmt.Sprintf("planReason is %d characters, and it holds at most %d", utf8.RuneCountInString(*req.PlanReason), steprecord.RatingReasonMax)
		}
	}
	return nil, &intake.Refusal{Status: http.StatusBadRequest, Code: "PLAN_RATING_INPUT", Message: fmt.Sprintf(
		"%s: a rating of the plan is a whole number from %d to %d in planRating with why in planReason, one to %d characters, the two together; nothing was recorded", wrong, steprecord.RatingLeast, steprecord.RatingMost, steprecord.RatingReasonMax)}
}
