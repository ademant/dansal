package webcommon

// Default event-type sentences (#1290), one YAML mapping of language code to
// text per tag bucket (ball/workshop/festival — the same buckets
// has_ball/has_workshop/has_festival already use, see CLAUDE.md). Used to
// compose a default event description when the organizer left one blank.
// Shared by dansal-web (fallback/merge base, same pattern as
// DefaultHomeIntroYAML) and dansal-webmin (pre-fills each bucket's textarea
// with working content instead of a blank field of unclear format).
//
// These are the author's own translations, not certified/professional ones
// for every language listed — same caliber as most of dansal-web's existing
// i18n.yaml content. An instance operator can override any subset of
// languages per bucket directly via webmin's "Default event descriptions"
// section; a language missing from that override falls back to this
// default, not to blank.
const (
	DefaultDescBallYAML = `de: "Dies ist ein Bal Folk – ein geselliger Tanzabend mit Livemusik zu traditionellen Volkstänzen."
br: "Ur bal-folk eo an darvoud-mañ, un abardaez dañsal gant sonerezh bev war dañsoù hengounel."
en: "This is a bal-folk dance evening, a social gathering with live music for traditional folk dances."
es: "Este es un bal-folk, una velada social de baile con música en vivo de danzas tradicionales."
fr: "Il s'agit d'un bal folk, une soirée dansante conviviale avec musique live sur des danses traditionnelles."
it: "Questo è un bal-folk, una serata di ballo sociale con musica dal vivo su danze tradizionali."
nl: "Dit is een bal folk, een gezellige dansavond met livemuziek op traditionele volksdansen."
uk: "Це бал-фолк — вечір соціальних танців із живою музикою під традиційні народні танці."
ca: "Això és un bal folk, una vetllada de ball social amb música en directe de danses tradicionals."
pt: "Este é um bal-folk, uma noite de dança social com música ao vivo de danças tradicionais."
pl: "To bal folk – wieczór tańca towarzyskiego z muzyką na żywo do tradycyjnych tańców ludowych."
cs: "Toto je bal folk – společenský taneční večer s živou hudbou k tradičním lidovým tancům."
`

	DefaultDescWorkshopYAML = `de: "Dies ist ein Tanzworkshop, in dem Schritte und Techniken für traditionelle Tänze vermittelt werden."
br: "Ur stummadenn dañs eo an darvoud-mañ, evit deskiñ tres ha teknikoù an dañsoù hengounel."
en: "This is a dance workshop teaching steps and technique for traditional folk dances."
es: "Este es un taller de baile en el que se enseñan pasos y técnica de danzas tradicionales."
fr: "Il s'agit d'un atelier de danse pour apprendre les pas et la technique des danses traditionnelles."
it: "Questo è un workshop di danza per imparare passi e tecnica dei balli tradizionali."
nl: "Dit is een dansworkshop waarin passen en techniek van traditionele volksdansen worden aangeleerd."
uk: "Це танцювальний майстер-клас, де навчають крокам і техніці традиційних народних танців."
ca: "Això és un taller de dansa on s'ensenyen passos i tècnica de danses tradicionals."
pt: "Este é um workshop de dança que ensina passos e técnica de danças tradicionais."
pl: "To warsztaty taneczne, na których uczy się kroków i techniki tradycyjnych tańców ludowych."
cs: "Toto je taneční workshop, na kterém se učí kroky a technika tradičních lidových tanců."
`

	DefaultDescFestivalYAML = `de: "Dies ist ein mehrtägiges Folk-Festival mit Tanz, Musik und Workshops."
br: "Ur gouel folk meur a zevezh eo an darvoud-mañ, gant dañs, sonerezh ha stummadennoù."
en: "This is a multi-day folk festival featuring dance, music, and workshops."
es: "Este es un festival folk de varios días con baile, música y talleres."
fr: "Il s'agit d'un festival folk de plusieurs jours avec danse, musique et ateliers."
it: "Questo è un festival folk di più giorni con danza, musica e workshop."
nl: "Dit is een meerdaags folkfestival met dans, muziek en workshops."
uk: "Це багатоденний фольклорний фестиваль із танцями, музикою та майстер-класами."
ca: "Això és un festival folk de diversos dies amb dansa, música i tallers."
pt: "Este é um festival folk de vários dias com dança, música e workshops."
pl: "To wielodniowy festiwal folkowy z tańcem, muzyką i warsztatami."
cs: "Toto je vícedenní folkový festival s tancem, hudbou a workshopy."
`
)
