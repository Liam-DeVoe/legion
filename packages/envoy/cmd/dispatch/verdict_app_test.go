package main

import "testing"

// Dispatch reads the verdict App's id from DISPATCH_VERDICT_APP_ID, and from the setting a
// deployment configured before it, DISPATCH_REVIEWER_APP_ID, while the new one is unset: so a
// release that reads the new name can be pinned under the configuration of the release before it.
// When both are set the new name wins, and a refusal names the setting the value came from.
func TestOldVerdictAppSettingIsRead(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "the old name alone", env: map[string]string{"DISPATCH_REVIEWER_APP_ID": "5341243"}, want: "5341243"},
		{name: "both names", env: map[string]string{"DISPATCH_VERDICT_APP_ID": "5341240", "DISPATCH_REVIEWER_APP_ID": "5341243"}, want: "5341240"},
		{name: "the new name alone", env: map[string]string{"DISPATCH_VERDICT_APP_ID": "5341240"}, want: "5341240"},
		{name: "neither name", env: nil, want: ""},
	} {
		if boot := resolveWith(t, c.env); boot.VerdictAppID != c.want {
			t.Errorf("%s: VerdictAppID = %q, want %q", c.name, boot.VerdictAppID, c.want)
		}
	}
	refusedWith(t, map[string]string{"DISPATCH_REVIEWER_APP_ID": "x"}, "DISPATCH_REVIEWER_APP_ID")
	refusedWith(t, map[string]string{"DISPATCH_VERDICT_APP_ID": "x", "DISPATCH_REVIEWER_APP_ID": "5341243"}, "DISPATCH_VERDICT_APP_ID")
}
