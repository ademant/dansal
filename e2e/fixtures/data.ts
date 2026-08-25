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

function daysFromNow(n: number): Date {
  const d = new Date();
  d.setDate(d.getDate() + n);
  d.setHours(0, 0, 0, 0);
  return d;
}

function nextWeekday(n: number, weekday: number): Date {
  const d = daysFromNow(n);
  while (d.getDay() !== weekday) d.setDate(d.getDate() + 1);
  return d;
}

function isoDateTime(d: Date, hour: number, minute = 0): string {
  const dd = new Date(d);
  dd.setHours(hour, minute, 0, 0);
  return dd.toISOString().replace(".000Z", "");
}

export function seedEvents(orgId: number, locationId: number) {
  const saturday = nextWeekday(3, 6);
  const sunday = nextWeekday(4, 0);
  const nextSat = nextWeekday(10, 6);

  return [
    {
      title: "Bal de Testville",
      description:
        "Un bal test avec musique live de Accordéon Trio",
      start_time: isoDateTime(saturday, 20, 30),
      end_time: isoDateTime(saturday, 23, 30),
      tags: ["bal-folk"],
      organization_id: orgId,
      location_id: locationId,
      food: "potluck",
      drink: "bar",
      floor_condition: "parquet",
      contact_name: "Jean Test",
      contact_email: "jean@test.example.com",
    },
    {
      title: "Atelier Bourrée du Berry",
      description:
        "Atelier de bourrée pour débutants. Pas besoin d'expérience.",
      start_time: isoDateTime(sunday, 14, 0),
      end_time: isoDateTime(sunday, 16, 30),
      tags: ["dance-workshop"],
      workshop_difficulty: "beginner",
      organization_id: orgId,
      location_id: locationId,
      contact_name: "Marie Test",
      contact_email: "marie@test.example.com",
    },
    {
      title: "Bal et Atelier Testville",
      description:
        "Un week-end complet avec bal le samedi et atelier le dimanche.",
      start_time: isoDateTime(nextSat, 20, 0),
      end_time: isoDateTime(nextSat, 23, 59),
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
