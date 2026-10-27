package main

import "testing"

// TestFindTransferOrigin covers #1677: sentinel rows in the delivered table
// (0 relay, -1 tag followers, -4/-5 IndexNow) must not be read as the event's
// original org, or every edit after the first IndexNow ping would be treated
// as a transfer.
func TestFindTransferOrigin(t *testing.T) {
	row := func(org int, update bool) deliveredRow { return deliveredRow{OrgID: org, IsUpdate: update} }
	transferred := 7
	transferredRow := deliveredRow{OrgID: 3, TransferredTo: &transferred}

	cases := []struct {
		name    string
		history []deliveredRow
		current int
		wantOrg int
		wantOK  bool
	}{
		{"sentinels only, no transfer", []deliveredRow{row(-4, false), row(0, false), row(-1, false)}, 5, 0, false},
		{"sentinel before the real org row", []deliveredRow{row(-4, false), row(5, false), row(-5, false)}, 5, 0, false},
		{"real transfer past sentinels", []deliveredRow{row(-4, false), row(2, false)}, 5, 2, true},
		{"update rows are ignored", []deliveredRow{row(2, true)}, 5, 0, false},
		{"already transferred origin is ignored", []deliveredRow{transferredRow}, 5, 0, false},
		{"origin equals current org", []deliveredRow{row(5, false)}, 5, 0, false},
		{"no history", nil, 5, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			org, ok := findTransferOrigin(c.history, c.current)
			if ok != c.wantOK || org != c.wantOrg {
				t.Errorf("findTransferOrigin = (%d, %v), want (%d, %v)", org, ok, c.wantOrg, c.wantOK)
			}
		})
	}
}
