package replay

// candidateReasons is why each candidate added to the spec's rules is compared, by the rule's name
// (plan D6): the comparison's document gives it in the rule's row of the trade against main
// (writeDecision). A rule the spec names — main, the rules the recorded windows' daemons ran,
// keep-past-review, blockers-first, returned-by-rank and cover — has none. A candidate added to
// record.SlotRules adds its reason here, one line, in plain words that name no deployment, issue or
// person, since the repository is public.
var candidateReasons = map[string]string{
	"wait-grace":             "A wait of one minute sent a started issue to the end of the returned part of the line, and an operator asked that a wait shorter than a slot's next free moment keep the slot. Five minutes is window one's median time from a slot's release to the next free moment (4 m 44 s); 114 waits gave a slot back in window one, 1 of them under five minutes.",
	"wait-grace-1h":          "The grace is a choice, so the comparison shows how the result moves with it.",
	"returned-by-first-slot": "Issues whose wait had cleared stood at the end of the returned part and could not reach a slot, and an operator asked that a cleared wait return an issue to the place it held. The daemon records no first slot, so a replay keeps one in a table of its own (`replay_first_slots`), and the daemon would need that record to run this rule.",
}
