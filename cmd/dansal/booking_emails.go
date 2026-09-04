package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
)

// bookingMailStrings holds localized email text for one language.
type bookingMailStrings struct {
	VerifySubject    string
	VerifyBody       string // args: name, verifyURL, expiryHours (int)
	ConfirmedSubject string
	ConfirmedBody    string // args: name, eventTitle, checkinURL
	ApprovedSubject  string
	ApprovedBody     string // args: name, eventTitle, checkinURL
}

var bookingMail = map[string]bookingMailStrings{
	"de": {
		VerifySubject:    "Buchung bestaetigen",
		VerifyBody:       "Hallo %s,\n\nvielen Dank fuer deine Buchungsanfrage. Bitte bestaetigen deine E-Mail-Adresse:\n\n%s\n\nDieser Link ist %d Stunden gueltig.\n\nWenn du keine Buchung vorgenommen hast, ignoriere bitte diese E-Mail.\n",
		ConfirmedSubject: "Buchung bestaetigt",
		ConfirmedBody:    "Hallo %s,\n\ndeine Buchung fuer %s wurde bestaetigt.\n\nDein QR-Link fuer den Einlass (bitte zeige ihn an der Tuer vor):\n\n%s\n\nBitte speichere diesen Link.\n",
		ApprovedSubject:  "Buchung genehmigt",
		ApprovedBody:     "Hallo %s,\n\ndeine Buchung fuer %s wurde von den Veranstaltern genehmigt.\n\nDein QR-Link fuer den Einlass (bitte zeige ihn an der Tuer vor):\n\n%s\n\nBitte speichere diesen Link.\n",
	},
	"en": {
		VerifySubject:    "Confirm your booking",
		VerifyBody:       "Hello %s,\n\nThank you for your booking request. Please confirm your email address:\n\n%s\n\nThis link expires in %d hours.\n\nIf you did not make this booking request, please ignore this email.\n",
		ConfirmedSubject: "Booking confirmed",
		ConfirmedBody:    "Hello %s,\n\nYour booking for %s is confirmed.\n\nYour check-in link (show this at the door):\n\n%s\n\nPlease save this link.\n",
		ApprovedSubject:  "Booking approved",
		ApprovedBody:     "Hello %s,\n\nYour booking for %s has been approved by the organisers.\n\nYour check-in link (show this at the door):\n\n%s\n\nPlease save this link.\n",
	},
	"fr": {
		VerifySubject:    "Confirmez votre reservation",
		VerifyBody:       "Bonjour %s,\n\nMerci pour votre demande de reservation. Veuillez confirmer votre adresse e-mail :\n\n%s\n\nCe lien expire dans %d heures.\n\nSi vous n'avez pas fait cette demande, ignorez cet e-mail.\n",
		ConfirmedSubject: "Reservation confirmee",
		ConfirmedBody:    "Bonjour %s,\n\nVotre reservation pour %s est confirmee.\n\nVotre lien d'entree (a presenter a la porte) :\n\n%s\n\nMerci de sauvegarder ce lien.\n",
		ApprovedSubject:  "Reservation approuvee",
		ApprovedBody:     "Bonjour %s,\n\nVotre reservation pour %s a ete approuvee par les organisateurs.\n\nVotre lien d'entree (a presenter a la porte) :\n\n%s\n\nMerci de sauvegarder ce lien.\n",
	},
	"es": {
		VerifySubject:    "Confirma tu reserva",
		VerifyBody:       "Hola %s,\n\nGracias por tu solicitud de reserva. Por favor confirma tu correo electronico:\n\n%s\n\nEste enlace caduca en %d horas.\n\nSi no hiciste esta solicitud, ignora este correo.\n",
		ConfirmedSubject: "Reserva confirmada",
		ConfirmedBody:    "Hola %s,\n\nTu reserva para %s esta confirmada.\n\nTu enlace de entrada (muestralo en la puerta):\n\n%s\n\nGuarda este enlace.\n",
		ApprovedSubject:  "Reserva aprobada",
		ApprovedBody:     "Hola %s,\n\nTu reserva para %s ha sido aprobada por los organizadores.\n\nTu enlace de entrada (muestralo en la puerta):\n\n%s\n\nGuarda este enlace.\n",
	},
	"it": {
		VerifySubject:    "Conferma la tua prenotazione",
		VerifyBody:       "Ciao %s,\n\nGrazie per la tua richiesta di prenotazione. Conferma il tuo indirizzo e-mail:\n\n%s\n\nQuesto link scade tra %d ore.\n\nSe non hai fatto questa richiesta, ignora questa e-mail.\n",
		ConfirmedSubject: "Prenotazione confermata",
		ConfirmedBody:    "Ciao %s,\n\nLa tua prenotazione per %s e' confermata.\n\nIl tuo link di ingresso (mostralo all'ingresso):\n\n%s\n\nSalva questo link.\n",
		ApprovedSubject:  "Prenotazione approvata",
		ApprovedBody:     "Ciao %s,\n\nLa tua prenotazione per %s e' stata approvata dagli organizzatori.\n\nIl tuo link di ingresso (mostralo all'ingresso):\n\n%s\n\nSalva questo link.\n",
	},
	"nl": {
		VerifySubject:    "Bevestig je boeking",
		VerifyBody:       "Hallo %s,\n\nBedankt voor je boekingsaanvraag. Bevestig je e-mailadres:\n\n%s\n\nDeze link verloopt over %d uur.\n\nAls je deze aanvraag niet hebt gedaan, negeer dan deze e-mail.\n",
		ConfirmedSubject: "Boeking bevestigd",
		ConfirmedBody:    "Hallo %s,\n\nJe boeking voor %s is bevestigd.\n\nJe inchecklink (toon dit aan de deur):\n\n%s\n\nSla deze link op.\n",
		ApprovedSubject:  "Boeking goedgekeurd",
		ApprovedBody:     "Hallo %s,\n\nJe boeking voor %s is goedgekeurd door de organisatoren.\n\nJe inchecklink (toon dit aan de deur):\n\n%s\n\nSla deze link op.\n",
	},
	"br": {
		VerifySubject:    "Kadarnaaat hoc'h enrolladenn",
		VerifyBody:       "Demat %s,\n\nTrugarez evit ho goulenn enrollan. Kadarnaaat hoc'h chomlec'h postel:\n\n%s\n\nAr liamm-man a vo berzet a-benn %d eur.\n\nMa n'hoc'h eus ket graet ar goulenn-man, distaogit ar postel-man.\n",
		ConfirmedSubject: "Enrolladenn kadarnaet",
		ConfirmedBody:    "Demat %s,\n\nHoc'h enrolladenn evit %s zo kadarnaet.\n\nHo liamm enman (diskouezit ouzh an nor):\n\n%s\n\nEnrollit ar liamm-man mar plij.\n",
		ApprovedSubject:  "Enrolladenn aotreet",
		ApprovedBody:     "Demat %s,\n\nHoc'h enrolladenn evit %s zo bet aotreet gant an aozourien.\n\nHo liamm enman (diskouezit ouzh an nor):\n\n%s\n\nEnrollit ar liamm-man mar plij.\n",
	},
}

var supportedBookingLangs = map[string]bool{
	"br": true, "de": true, "en": true,
	"es": true, "fr": true, "it": true, "nl": true,
}

const defaultBookingLang = "de"

// bookingMailStringsFor returns localized strings, falling back to defaultBookingLang.
func bookingMailStringsFor(lang string) bookingMailStrings {
	if s, ok := bookingMail[lang]; ok {
		return s
	}
	return bookingMail[defaultBookingLang]
}

// parseLang extracts a normalized primary language tag from an Accept-Language
// value and returns it if supported, else defaultBookingLang.
func parseLang(acceptLang string) string {
	for part := range strings.SplitSeq(acceptLang, ",") {
		tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		primary := strings.ToLower(strings.SplitN(tag, "-", 2)[0])
		if supportedBookingLangs[primary] {
			return primary
		}
	}
	return defaultBookingLang
}

// bookingLangFromRequest reads Accept-Language from r.
func bookingLangFromRequest(r *http.Request) string {
	return parseLang(r.Header.Get("Accept-Language"))
}

// eventTitle fetches the event title for a given event ID.
func eventTitle(eventID int) string {
	var title string
	db.QueryRow("SELECT title FROM events WHERE id=?", eventID).Scan(&title)
	return title
}

// sendBookingConfirmedEmail sends the post-verification confirmed email.
func sendBookingConfirmedEmail(name, email, lang string, eventID int, qrToken string) {
	s := bookingMailStringsFor(lang)
	base := strings.TrimRight(config.Server.BaseURL, "/")
	checkinURL := base + "/checkin/" + qrToken
	body := fmt.Sprintf(s.ConfirmedBody, name, eventTitle(eventID), checkinURL)
	if _, err := SendEmail(email, s.ConfirmedSubject, body, false); err != nil {
		log.Printf("bookings: confirmed email failed for %s: %v", email, err)
	}
}

// sendBookingApprovedEmail sends the approval notification email.
func sendBookingApprovedEmail(name, email, lang string, eventID int, qrToken string) {
	s := bookingMailStringsFor(lang)
	base := strings.TrimRight(config.Server.BaseURL, "/")
	checkinURL := base + "/checkin/" + qrToken
	body := fmt.Sprintf(s.ApprovedBody, name, eventTitle(eventID), checkinURL)
	if _, err := SendEmail(email, s.ApprovedSubject, body, true); err != nil {
		log.Printf("bookings: approved email failed for %s: %v", email, err)
	}
}
