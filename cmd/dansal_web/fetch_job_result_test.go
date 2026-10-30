package main

import "testing"

// TestFetchRunResultFromJobMapsRobotsRefusal covers #1709 with #1545: a job that
// failed on robots.txt must still reach the acceptance redirect, which keys on
// the apiHTTPError's robots_not_acknowledged code, and other failures must not.
func TestFetchRunResultFromJobMapsRobotsRefusal(t *testing.T) {
	_, err := fetchRunResultFromJob(FetchJob{State: "error", Error: "robots.txt restricts this feed", ErrorCode: "robots_not_acknowledged"})
	if _, restricted := robotsNotAcknowledgedMessage(err); !restricted {
		t.Fatalf("robots job error %v not recognised as robots refusal", err)
	}

	_, err = fetchRunResultFromJob(FetchJob{State: "error", Error: "boom"})
	if _, restricted := robotsNotAcknowledgedMessage(err); restricted {
		t.Fatalf("generic job error %v wrongly recognised as robots refusal", err)
	}
	if err == nil || err.Error() != "boom" {
		t.Fatalf("generic job error = %v, want boom", err)
	}
}
