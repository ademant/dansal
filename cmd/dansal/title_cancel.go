package main

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Title-only cancellations (#1841): organisers often cancel by renaming the
// event ("ABGESAGT Tanzabend") and leave STATUS:CONFIRMED in the feed. The
// import never cancels on the title alone; it notifies admins so a person
// decides.

// titleSignal is what a title keyword announces.
type titleSignal int

const (
	titleSignalNone titleSignal = iota
	titleSignalCancelled
	titleSignalPostponed
)

// titleCancelKeywords is the single keyword list (lower case). Multi-word
// entries match as a phrase. Postponed = rescheduled, not cancelled.
var titleCancelKeywords = map[titleSignal][]string{
	titleSignalCancelled: {
		// de
		"abgesagt", "fällt aus", "faellt aus", "entfällt", "entfaellt", "storniert",
		// en
		"cancelled", "canceled", "called off",
		// fr
		"annulé", "annulée", "annule", "annulee",
		// nl
		"geannuleerd", "afgelast",
		// it
		"annullato", "annullata",
		// es, pt
		"cancelado", "cancelada", "suspendido", "suspendida",
		// ca
		"cancel·lat", "cancel·lada", "cancellat", "anul·lat", "anul·lada",
		// cs
		"zrušeno", "zrušen", "zrušena",
		// pl
		"odwołany", "odwołana", "odwołane",
		// uk
		"скасовано", "скасований", "скасована", "відмінено",
		// br
		"nullet",
	},
	titleSignalPostponed: {
		"verschoben", "postponed", "reporté", "reportée", "uitgesteld",
		"rinviato", "rinviata", "aplazado", "aplazada", "adiado", "adiada",
		"ajornat", "ajornada", "odloženo", "przełożony", "przełożona", "przełożone",
		"перенесено", "перенесений",
	},
}

// titleSegmentSeparators split a title into parts; a keyword must start or
// end one of them. " - " is handled separately so hyphenated words
// ("Absage-Risiko") stay intact.
const titleSegmentSeparators = ":|/()[]{}*+!?•·,;–—\"'«»„“”"

// titleCancellationSignal reports whether title announces a cancellation or
// postponement. Conservative: the keyword must sit at the start or end of
// the title or of a bracketed/separated part, as a whole word,
// case-insensitive. Cancellation wins over postponement.
func titleCancellationSignal(title string) titleSignal {
	t := strings.ToLower(strings.ReplaceAll(title, " - ", " | "))
	segs := strings.FieldsFunc(t, func(r rune) bool { return strings.ContainsRune(titleSegmentSeparators, r) })
	for _, sig := range []titleSignal{titleSignalCancelled, titleSignalPostponed} {
		for _, seg := range segs {
			seg = strings.TrimFunc(seg, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
			for _, kw := range titleCancelKeywords[sig] {
				if hasWordPrefix(seg, kw) || hasWordSuffix(seg, kw) {
					return sig
				}
			}
		}
	}
	return titleSignalNone
}

func hasWordPrefix(s, kw string) bool {
	if !strings.HasPrefix(s, kw) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[len(kw):])
	return len(s) == len(kw) || !unicode.IsLetter(r)
}

func hasWordSuffix(s, kw string) bool {
	if !strings.HasSuffix(s, kw) {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:len(s)-len(kw)])
	return len(s) == len(kw) || !unicode.IsLetter(r)
}

// notifyTitleCancellation is a var so tests can capture the call.
var notifyTitleCancellation = notifyAdminsTitleCancellation

// checkTitleCancellation notifies admins (in a goroutine) when a feed import
// turned an uncancelled event's title into a cancellation/postponement
// notice. Fires once per change: only when the old title didn't already
// carry the same signal. Manual edits (fetchSourceID 0) never notify.
func checkTitleCancellation(fetchSourceID, eventID int, oldTitle, newTitle string, isCancelled bool) {
	if fetchSourceID <= 0 || isCancelled {
		return
	}
	sig := titleCancellationSignal(newTitle)
	if sig == titleSignalNone || titleCancellationSignal(oldTitle) == sig {
		return
	}
	go notifyTitleCancellation(eventID, newTitle, sig)
}

// notifyAdminsTitleCancellation tells admins a feed title looks like a
// cancellation/postponement, with a one-click link to the event's edit page.
func notifyAdminsTitleCancellation(eventID int, title string, sig titleSignal) {
	subject, what := "Feed title signals a cancellation", "cancelled — check and cancel the event if so"
	if sig == titleSignalPostponed {
		subject, what = "Feed title signals a postponement", "postponed — check and update the date"
	}
	msg := fmt.Sprintf("Imported event #%d is now titled %q but is not marked cancelled. It may be %s.", eventID, title, what)
	forEachAdmin(func(id int, email, chatID, matrixID string, matrixVerified bool) {
		notifyUser(chatID, matrixID, matrixVerified, email, subject, msg+" — "+adminReviewLink(id, fmt.Sprintf("/admin/events/%d/edit", eventID)))
	})
}
