export const ADMIN = {
  email: "e2e-admin@dansal.test",
  password: "E2e-Admin-2026!",
  name: "E2E Admin",
  role: "admin",
};

export const EDITOR = {
  email: "e2e-editor@dansal.test",
  password: "E2e-Editor-2026!",
  name: "E2E Editor",
  role: "publisher",
};

export const VIEWER = {
  email: "e2e-viewer@dansal.test",
  password: "E2e-Viewer-2026!",
  name: "E2E Viewer",
  role: "user",
};

export const ORG = {
  name: "Bal Test Association",
  website: "https://bta.example.com",
  description: "Test organisation for bal-folk events",
};

export const LOCATION = {
  location: "Salle des Fêtes Testville",
  short_name: "Salle Testville",
  address: "1 Rue de la Danse",
  zipcode: "35000",
  town: "Testville",
  country: "France",
  country_code: "FR",
  latitude: 48.11,
  longitude: -1.68,
};

// randomFutureDate picks a day between minDays and maxDays from now
// (exclusive upper bound). Runs against a pre-filled/shared database (e.g.
// the persistent dev instance) reuse the same DB across many test runs, so
// a fixed/deterministic date would repeatedly land on the same day and get
// silently merged into a stale event from an earlier run by dansal's
// location+time / title+time dedup tiers (see cmd/dansal/dedup.go).
// Randomizing per run makes same-slot collisions between independent runs
// very unlikely; keep the range narrow enough that callers rendering the
// index page (capped at ~100 soonest events) still see it — see
// EVENT_DATE_MIN_DAYS/MAX_DAYS below for the concrete tradeoff.
export function randomFutureDate(minDays: number, maxDays: number): Date {
  const days = minDays + Math.floor(Math.random() * (maxDays - minDays));
  const d = new Date();
  d.setDate(d.getDate() + days);
  d.setHours(0, 0, 0, 0);
  return d;
}

function isoDateTime(d: Date, hour: number, minute = 0): string {
  const dd = new Date(d);
  dd.setHours(hour, minute, 0, 0);
  return dd.toISOString().replace(".000Z", "");
}

// isoDate formats d's own local calendar date (not toISOString()'s UTC
// date, which can land on the previous/next day depending on the host's
// timezone offset and desync from the local date isoDateTime/
// datetimeLocalValue actually build the event around). Also the format the
// admin event form's <input type="date"> fields expect.
export function isoDate(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

// hhmm formats an hour/minute pair as the admin event form's separate
// <input type="time"> fields expect (bare HH:MM, no date component).
export function hhmm(hour: number, minute: number): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(hour)}:${pad(minute)}`;
}

// titleWithDate appends the event's own date to its title so repeated runs
// against the same database produce visibly distinct, self-describing
// events instead of same-named rows that pile up or get deduped together.
export function titleWithDate(base: string, d: Date): string {
  return `${base} – ${isoDate(d)}`;
}

// The index page only ever fetches the soonest ~100 published future events
// (see cmd/dansal_web/frontend.go's indexHandler), so a random date needs to
// land well inside that window to still be visible to discover.spec.ts's
// event-table assertions on a pre-filled database with plenty of its own
// upcoming events. 3–45 days keeps a comfortable margin under 100 events
// even on a fairly busy instance, while still varying per run.
export const EVENT_DATE_MIN_DAYS = 3;
export const EVENT_DATE_MAX_DAYS = 45;

// maxDays lets callers narrow the window further (see seed.ts's
// safeMaxDaysOut) once they know how many events the live database already
// has coming up soon — the static EVENT_DATE_MAX_DAYS default is only a
// reasonable guess for a mostly-empty instance.
export function seedEvents(
  orgId: number,
  locationId: number,
  maxDays: number = EVENT_DATE_MAX_DAYS
) {
  // Per-call nonce appended to every title (#1207): Tier 4 dedup
  // (title + start_time ±3h) fires unconditionally when Tier 3 misses.
  // Two concurrent fullSeed() calls that happen to pick the same random
  // date produce identical title + start_time → Tier 4 merges them onto
  // the same event row, then both POST-timetable calls append 3 entries
  // each, accumulating over runs. A short random tag makes each call's
  // titles unique, so Tier 4 can never match across runs.
  const nonce = Math.random().toString(36).slice(2, 6);
  const balDate = randomFutureDate(EVENT_DATE_MIN_DAYS, maxDays);
  const workshopDate = randomFutureDate(EVENT_DATE_MIN_DAYS, maxDays);
  const festDate = randomFutureDate(EVENT_DATE_MIN_DAYS, maxDays);

  return [
    {
      title: titleWithDate("Bal de Testville", balDate) + ` [${nonce}]`,
      description:
        "Un bal test avec musique live de Accordéon Trio",
      start_time: isoDateTime(balDate, 20, 30),
      end_time: isoDateTime(balDate, 23, 30),
      tags: ["bal-folk"],
      organization_id: orgId,
      location_id: locationId,
      food: "potluck",
      drink: "alcohol",
      floor_condition: "parquet",
      contact_name: "Jean Test",
      contact_email: "jean@test.example.com",
    },
    {
      title: titleWithDate("Atelier Bourrée du Berry", workshopDate) + ` [${nonce}]`,
      description:
        "Atelier de bourrée pour débutants. Pas besoin d'expérience.",
      start_time: isoDateTime(workshopDate, 14, 0),
      end_time: isoDateTime(workshopDate, 16, 30),
      tags: ["dance-workshop"],
      workshop_difficulty: "beginner",
      organization_id: orgId,
      location_id: locationId,
      contact_name: "Marie Test",
      contact_email: "marie@test.example.com",
    },
    {
      title: titleWithDate("Bal et Atelier Testville", festDate) + ` [${nonce}]`,
      description:
        "Un week-end complet avec bal le samedi et atelier le dimanche.",
      start_time: isoDateTime(festDate, 20, 0),
      end_time: isoDateTime(festDate, 23, 59),
      tags: ["bal-folk", "dance-workshop"],
      organization_id: orgId,
      location_id: locationId,
      booking_url: "https://tickets.example.com/testville",
    },
  ];
}

export function seedTimetable(_eventId: number) {
  return [
    {
      start_time: "20:30",
      end_time: "21:00",
      title: "Accueil & musique d'ambiance",
      entry_type: "bal",
    },
    {
      start_time: "21:00",
      end_time: "21:30",
      title: "Plotlin pour débutants",
      entry_type: "workshop",
    },
    {
      start_time: "21:30",
      end_time: "23:30",
      title: "Bal libre",
      entry_type: "bal",
    },
  ];
}

export const SEARCH_TERM = "Testville";
export const EVENT_HREF_PREFIX = "/events/";

export const MUSICIAN = {
  bandname: "Accordéon Trio Test",
  short_name: "AT Test",
  description: "Trio d'accordéon pour tests E2E",
  genre: "bal-folk",
  country: "France",
  email: "contact@accordion-trio.test.example.com",
};

export const INSTRUCTOR = {
  name: "Marie Professeur",
  bio: "Enseignante de bourrée et danse traditionnelle.",
  email: "marie@instructeur.test.example.com",
};
