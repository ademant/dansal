package main

import (
	"testing"
	"time"
)

func TestTitleCancellationSignal(t *testing.T) {
	cases := []struct {
		title string
		want  titleSignal
	}{
		{"ABGESAGT Tanzabend", titleSignalCancelled}, // prod #141
		{"Tanzabend - abgesagt", titleSignalCancelled},
		{"Tanzabend (ABGESAGT)", titleSignalCancelled},
		{"+++ Abgesagt +++ Tanzabend", titleSignalCancelled},
		{"Tanzabend fällt aus", titleSignalCancelled},
		{"Folk Ball [CANCELLED]", titleSignalCancelled},
		{"Canceled: Folk Ball", titleSignalCancelled},
		{"ANNULÉ - Bal folk", titleSignalCancelled},
		{"Bal folk annulé", titleSignalCancelled},
		{"Geannuleerd: Folkbal", titleSignalCancelled},
		{"Ballo folk (annullato)", titleSignalCancelled},
		{"Cancelado - Baile folk", titleSignalCancelled},
		{"ODWOŁANY Bal folkowy", titleSignalCancelled},
		{"Скасовано: Бал фолк", titleSignalCancelled},
		{"VERSCHOBEN: Tanzabend", titleSignalPostponed},
		{"Folk Ball (postponed)", titleSignalPostponed},
		{"Bal folk – reporté", titleSignalPostponed},
		// negatives
		{"20:00-22:00 Tanzabend", titleSignalNone},
		{"Tanzabend ohne Absage-Risiko", titleSignalNone},
		{"20:00-22:00 Tanzabend - eventuell mit Live-Musik", titleSignalNone},
		{"Verschobener Tanzabend vom Mai", titleSignalNone},
		{"Bal folk des non-annulés", titleSignalNone},
		{"Die abgesagten Lieder Tanzabend", titleSignalNone},
		{"", titleSignalNone},
	}
	for _, c := range cases {
		if got := titleCancellationSignal(c.title); got != c.want {
			t.Errorf("titleCancellationSignal(%q) = %d, want %d", c.title, got, c.want)
		}
	}
}

// #1840: the normal re-import update path must write the title, not only the
// merge path.
func TestInsertEventUpdatesTitleOnReimport(t *testing.T) {
	setupDedupTestDB(t)
	stubTitleCancelNotify(t)
	start := time.Date(2030, 6, 1, 19, 0, 0, 0, time.UTC).Unix()
	base := EventInput{
		Title: "20:00-22:00 Tanzabend", StartTime: start, EndTime: start + 2*3600,
		UID: "t-1840", FetchSourceID: 9, Source: "https://example.org/cal.ics",
		SourceLastModified: 1000, IsPublished: true,
	}
	id, _, _, err := insertEvent(db, base)
	if err != nil {
		t.Fatal(err)
	}
	title := func() string {
		var s string
		db.QueryRow("SELECT title FROM events WHERE id=?", id).Scan(&s)
		return s
	}

	// Normal path, source != "": changed_at=0, so no merge.
	db.Exec("UPDATE events SET changed_at=0 WHERE id=?", id)
	in := base
	in.Title, in.SourceLastModified = "20:00-22:00 Tanzabend - eventuell mit Live-Musik", 2000
	if _, _, outcome, err := insertEvent(db, in); err != nil || outcome != outcomeUpdated {
		t.Fatalf("normal path: outcome=%s err=%v", outcome, err)
	}
	if got := title(); got != in.Title {
		t.Errorf("normal path (source set): title=%q, want %q", got, in.Title)
	}

	// Normal path, source == "".
	db.Exec("UPDATE events SET changed_at=0 WHERE id=?", id)
	in.Source, in.Title, in.SourceLastModified = "", "Tanzabend mit Live-Musik", 3000
	if _, _, _, err := insertEvent(db, in); err != nil {
		t.Fatal(err)
	}
	if got := title(); got != in.Title {
		t.Errorf("normal path (no source): title=%q, want %q", got, in.Title)
	}

	// Merge path (manual edit older than the source) keeps updating the title.
	db.Exec("UPDATE events SET changed_at=3500 WHERE id=?", id)
	in.Source, in.Title, in.SourceLastModified = base.Source, "Weihnachtstanzabend", 4000
	if _, _, _, err := insertEvent(db, in); err != nil {
		t.Fatal(err)
	}
	if got := title(); got != in.Title {
		t.Errorf("merge path: title=%q, want %q", got, in.Title)
	}
}

type titleCancelCall struct {
	id    int
	title string
	sig   titleSignal
}

func stubTitleCancelNotify(t *testing.T) chan titleCancelCall {
	ch := make(chan titleCancelCall, 10)
	orig := notifyTitleCancellation
	notifyTitleCancellation = func(id int, title string, sig titleSignal) { ch <- titleCancelCall{id, title, sig} }
	t.Cleanup(func() { notifyTitleCancellation = orig })
	return ch
}

// #1841: a feed title turning into a cancellation notice notifies admins once,
// without cancelling the event.
func TestInsertEventNotifiesTitleCancellationOnce(t *testing.T) {
	setupDedupTestDB(t)
	ch := stubTitleCancelNotify(t)
	expect := func(want int) []titleCancelCall {
		t.Helper()
		var got []titleCancelCall
		deadline := time.After(500 * time.Millisecond)
		for {
			select {
			case c := <-ch:
				got = append(got, c)
			case <-deadline:
				if len(got) != want {
					t.Fatalf("got %d notifications %v, want %d", len(got), got, want)
				}
				return got
			}
		}
	}
	start := time.Date(2030, 6, 1, 19, 0, 0, 0, time.UTC).Unix()
	in := EventInput{
		Title: "20:00-22:00 Tanzabend", StartTime: start, EndTime: start + 2*3600,
		UID: "t-1841", FetchSourceID: 9, Source: "https://example.org/cal.ics",
		SourceLastModified: 1000, IsPublished: true,
	}
	id, _, _, err := insertEvent(db, in)
	if err != nil {
		t.Fatal(err)
	}
	expect(0)

	db.Exec("UPDATE events SET changed_at=0 WHERE id=?", id)
	in.Title, in.SourceLastModified = "ABGESAGT Tanzabend", 2000
	insertEvent(db, in)
	got := expect(1)
	if got[0].id != id || got[0].sig != titleSignalCancelled {
		t.Errorf("notification = %+v, want id %d cancelled", got[0], id)
	}
	var cancelled bool
	db.QueryRow("SELECT is_cancelled FROM events WHERE id=?", id).Scan(&cancelled)
	if cancelled {
		t.Error("event was cancelled automatically; must only notify")
	}

	// Another fetch with a newer LAST-MODIFIED but the same signal: no repeat.
	db.Exec("UPDATE events SET changed_at=0 WHERE id=?", id)
	in.Title, in.SourceLastModified = "ABGESAGT: Tanzabend", 3000
	insertEvent(db, in)
	expect(0)

	// Already cancelled in the feed: nothing to tell.
	in2 := in
	in2.UID, in2.StartTime, in2.EndTime, in2.IsCancelled = "t-1841b", start+86400*7, start+86400*7+3600, true
	insertEvent(db, in2)
	expect(0)

	// New event inserted with a cancellation title notifies; a manual create doesn't.
	in3 := in
	in3.UID, in3.StartTime, in3.EndTime = "t-1841c", start+86400*14, start+86400*14+3600
	insertEvent(db, in3)
	expect(1)
	in4 := in3
	in4.UID, in4.FetchSourceID, in4.Source, in4.StartTime, in4.EndTime = "t-1841d", 0, "", start+86400*21, start+86400*21+3600
	insertEvent(db, in4)
	expect(0)
}
