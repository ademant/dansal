import { Page } from "@playwright/test";
import { apiGet, apiPost } from "./seed";

const API_BASE = process.env.API_URL ?? "http://localhost:8000";

// Home-page filter buttons (.type-btn) are one per HomeGroup key, built from
// tags.yaml's emoji-bearing tags (see homeGroups() in
// cmd/dansal_web/templatefuncs_tags.go) — bal-folk/fest-noz collapse into
// one "ball" button, workshop/dance-workshop/musician-workshop/music-course
// into one "workshop" button. One representative slug per group is enough
// to give that button's filter something to narrow.
const HOME_GROUP_TAGS = ["festival", "bal-folk", "concert", "workshop", "session"];

// Two named locations, created once and reused by name across runs (same
// idea as seedOrg's reuse-by-name) rather than jittered fresh every run like
// seedLocation() — these need a *stable* identity so "does the map fixture
// already exist" can be checked, not just "create another one".
const NEARBY_LOCATION = {
  location: "E2E Index Nearby",
  short_name: "IdxNear",
  town: "Testville",
  country: "France",
  country_code: "FR",
  latitude: 48.115,
  longitude: -1.681,
};
// A different continent entirely so it can never fall inside the nearby
// pair's cluster radius at any sane post-fitBounds zoom level.
const FAR_LOCATION = {
  location: "E2E Index Far",
  short_name: "IdxFar",
  town: "Tokyo",
  country: "Japan",
  country_code: "JP",
  latitude: 35.68,
  longitude: 139.69,
};

export interface IndexWeekFixture {
  weekDates: string[]; // 7 ISO dates, Monday..Sunday (includes past days)
  // Dates from today through Sunday — the only days the public index can
  // ever show, since it filters to future events only (include_past
  // defaults to false). Every invariant below is scoped to these, not the
  // full 7, so the fixture stays correct regardless of which day of the
  // week the suite happens to run on.
  visibleDates: string[];
  // Whichever visible date ended up strictly the busiest — Friday when the
  // suite runs early enough in the week to still see it, otherwise the
  // last remaining visible day (so this is never a past date).
  busiestDate: string;
  nearbyLocationId: number;
  farLocationId: number;
  countsByDate: Record<string, number>;
  // The single far-location event's own title — tag/date vary run to run
  // depending on which home-group tag happened to be missing, so tests
  // locate its map marker by this exact title rather than guessing it.
  farEventTitle: string;
}

// Replicates index.html's own getWeekStart(0) exactly:
//   d.setDate(d.getDate() + (day===0 ? -6 : 1-day))
// A day-of-week mismatch here would silently seed events into what the app
// itself considers a *different* ISO week than the one the test navigates to.
function mondayOf(d: Date): Date {
  const day = d.getDay();
  const monday = new Date(d);
  monday.setDate(d.getDate() + (day === 0 ? -6 : 1 - day));
  monday.setHours(0, 0, 0, 0);
  return monday;
}

function isoDateOnly(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

function weekDatesFrom(monday: Date): string[] {
  const out: string[] = [];
  for (let i = 0; i < 7; i++) {
    const d = new Date(monday);
    d.setDate(monday.getDate() + i);
    out.push(isoDateOnly(d));
  }
  return out;
}

async function ensureNamedLocation(
  page: Page,
  token: string,
  def: typeof NEARBY_LOCATION
): Promise<number> {
  const existing = await apiGet(
    page,
    `/api/v1/locations?name=${encodeURIComponent(def.location)}&limit=10`
  );
  if (Array.isArray(existing)) {
    const found = existing.find((l: any) => l.location === def.location);
    if (found) return found.id;
  }
  const data = await apiPost(page, "/api/v1/locations", token, def);
  return data[0].location.id;
}

// Tracks how many fixture events already exist (or have been created this
// run) at each (date, locationId) pair, so every new one can be given a
// start time safely outside every other's dedup window. Without this,
// several events legitimately needing the same day+location (the day-
// filler, the nearby-cluster pair, tag-coverage top-ups) all land within a
// few hours of each other and dansal's own Tier-3 dedup
// (location_id + start_time ±3h, no title check — see cmd/dansal/dedup.go)
// silently merges what should be N distinct events into one row, quietly
// dropping tags/titles from every merge but the first.
//
// For *today* specifically, slots must also stay after the current
// wall-clock hour: the public index only shows future events, so an event
// scheduled at, say, 00:00 on a run that starts at 14:00 is already over by
// the time anything queries for it — created successfully, then silently
// invisible, which looks exactly like a dedup collision until you check
// each create call's own response instead of just the final list.
class SlotAllocator {
  private counts = new Map<string, number>();

  constructor(private todayIso: string, private nowHour: number) {}

  seed(date: string, locationId: number): void {
    const key = `${date}|${locationId}`;
    this.counts.set(key, (this.counts.get(key) ?? 0) + 1);
  }

  // 4h apart is comfortably outside dedup's ±3h window; wraps after 6
  // slots/day, far more than any single invariant ever needs at one
  // date+location in practice — except on today, where the safe window
  // only starts after `nowHour`, so fewer slots may fit before midnight.
  nextHour(date: string, locationId: number): number {
    const key = `${date}|${locationId}`;
    const n = this.counts.get(key) ?? 0;
    this.counts.set(key, n + 1);
    const base = date === this.todayIso ? this.nowHour + 1 : 0;
    // Clamped so a late-in-the-day run never produces an invalid HH>23
    // string — date-role selection below already keeps how many slots
    // "today" ever needs to a minimum (usually just one), so collisions
    // from clamping are a late-night-only edge case, not the common path.
    return Math.min(base + n * 4, 21);
  }
}

async function createFixtureEvent(
  page: Page,
  token: string,
  orgId: number,
  locationId: number,
  date: string,
  tag: string,
  nonce: string,
  slots: SlotAllocator
): Promise<string> {
  const hour = slots.nextHour(date, locationId);
  const pad = (n: number) => String(n).padStart(2, "0");
  const title = `E2E Index Fixture ${date} ${pad(hour)}h ${tag} [${nonce}]`;
  await apiPost(page, "/api/v1/events", token, {
    title,
    description: "E2E index-page fixture event — safe to ignore/delete.",
    start_time: `${date}T${pad(hour)}:00:00`,
    end_time: `${date}T${pad(hour + 2)}:00:00`,
    tags: [tag],
    organization_id: orgId,
    location_id: locationId,
  });
  return title;
}

async function fetchWeekEvents(page: Page, weekDates: string[]): Promise<any[]> {
  // No server-side date-range filter is used here (there is one —
  // end_time_after/end_time_before — but filtering the same broad fetch
  // seedEvents()'s safeMaxDaysOut() already does client-side is simpler and
  // keeps this helper consistent with that existing pattern).
  const resp = await page.request.fetch(`${API_BASE}/api/v1/events?limit=1000`);
  const all = await resp.json();
  if (!Array.isArray(all)) return [];
  return all.filter((e: any) => weekDates.includes((e.start_time || "").slice(0, 10)));
}

// Ensures the *current* ISO week satisfies every invariant the index-page
// discovery tests need, topping up only what's missing rather than
// inserting a fixed batch every run:
//   1. every *visible* day has >=1 event (fills the week view)
//   2. one visible day is strictly the busiest (mini-cal heatmap: highest
//      rank) — Friday when it hasn't passed yet, else the last remaining
//      visible day
//   3. >=2 events share NEARBY_LOCATION (map: should cluster)
//      and >=1 event sits at FAR_LOCATION (map: should not cluster)
//   4. every home-group filter button has >=1 representative event
//
// "Visible" matters because the public index only ever shows *future*
// events (include_past defaults to false, cmd/dansal/events.go) — running
// this suite on, say, a Saturday means the current ISO week's Monday..
// Friday already happened and can never appear on the page no matter what
// exists in the DB. Scoping every invariant to today..Sunday keeps this
// fixture correct regardless of which day of the week the suite runs on,
// rather than quietly seeding unobservable past-dated events.
//
// Idempotent: running this twice against a week that already satisfies all
// four invariants creates nothing new. This matters because other spec
// files' fullSeed() calls create randomly-dated events that can occasionally
// land in "this week" by chance, and because repeated suite runs against a
// shared/persistent dev DB must not pile up fixture events run over run.
export async function ensureIndexWeekFixture(
  page: Page,
  token: string,
  orgId: number
): Promise<IndexWeekFixture> {
  const weekDates = weekDatesFrom(mondayOf(new Date()));
  const friday = weekDates[4];
  const todayIso = isoDateOnly(new Date());
  const visibleDates = weekDates.filter((d) => d >= todayIso);
  const busiestDate = visibleDates.includes(friday) ? friday : visibleDates[visibleDates.length - 1];
  // Map fixtures prefer a *future* (non-today) day, distinct from
  // busiestDate when one's available: today is wall-clock-constrained
  // (only hours after "now" are actually upcoming), while any other
  // visible day is safe at any hour. Both land on the same date — they're
  // at different locations, so there's no dedup collision between them —
  // which also means "today" only ever needs the one event step 1's
  // fill-every-visible-day pass gives it, except in the rare case where
  // today is the *only* visible day (running late on a Sunday), where
  // everything necessarily collapses onto it and the wall-clock-aware
  // SlotAllocator below has to fit what it can into the remaining hours.
  const futureDates = visibleDates.filter((d) => d !== todayIso);
  const nearbyDate = futureDates.find((d) => d !== busiestDate) ?? futureDates[0] ?? busiestDate;
  const farDate = nearbyDate;
  const nonce = Math.random().toString(36).slice(2, 6);

  const nearbyLocationId = await ensureNamedLocation(page, token, NEARBY_LOCATION);
  const farLocationId = await ensureNamedLocation(page, token, FAR_LOCATION);

  const weekEvents = await fetchWeekEvents(page, weekDates);

  const countsByDate: Record<string, number> = {};
  weekDates.forEach((d) => (countsByDate[d] = 0));
  const tagsPresent = new Set<string>();
  const slots = new SlotAllocator(todayIso, new Date().getHours());
  let nearbyCount = 0;
  let farCount = 0;
  let farEventTitle = "";
  weekEvents.forEach((e: any) => {
    const d = (e.start_time || "").slice(0, 10);
    if (d in countsByDate) countsByDate[d]++;
    (e.tags || []).forEach((t: string) => tagsPresent.add(t));
    if (e.location_id) slots.seed(d, e.location_id);
    if (e.location_id === nearbyLocationId) nearbyCount++;
    if (e.location_id === farLocationId) {
      farCount++;
      if (!farEventTitle) farEventTitle = e.title;
    }
  });

  const missingTags = HOME_GROUP_TAGS.filter((t) => !tagsPresent.has(t));
  const nextMissingTag = () => missingTags.shift() ?? "bal-folk";

  // Re-checked after every later step, since topping up for the map/tag
  // invariants can itself push another visible day's count past
  // busiestDate's, quietly breaking invariant 2 again.
  async function enforceBusiestDayInvariant(): Promise<void> {
    let otherMax = Math.max(0, ...visibleDates.filter((d) => d !== busiestDate).map((d) => countsByDate[d]));
    while (countsByDate[busiestDate] <= otherMax) {
      await createFixtureEvent(page, token, orgId, nearbyLocationId, busiestDate, nextMissingTag(), nonce, slots);
      countsByDate[busiestDate]++;
      otherMax = Math.max(0, ...visibleDates.filter((d) => d !== busiestDate).map((d) => countsByDate[d]));
    }
  }

  // 1. every visible day >=1 event
  for (const d of visibleDates) {
    if (countsByDate[d] === 0) {
      await createFixtureEvent(page, token, orgId, nearbyLocationId, d, nextMissingTag(), nonce, slots);
      countsByDate[d] = 1;
    }
  }

  // 2. busiestDate strictly the busiest among visible days
  await enforceBusiestDayInvariant();

  // 3. map fixtures — nearby pair + one far, both on stable visible dates
  while (nearbyCount < 2) {
    await createFixtureEvent(page, token, orgId, nearbyLocationId, nearbyDate, nextMissingTag(), nonce, slots);
    countsByDate[nearbyDate]++;
    nearbyCount++;
  }
  if (farCount < 1) {
    farEventTitle = await createFixtureEvent(page, token, orgId, farLocationId, farDate, nextMissingTag(), nonce, slots);
    countsByDate[farDate]++;
    farCount++;
  }
  await enforceBusiestDayInvariant();

  // 4. any home-group tags not already folded into steps 1-3
  for (const tag of [...missingTags]) {
    missingTags.shift();
    await createFixtureEvent(page, token, orgId, nearbyLocationId, nearbyDate, tag, nonce, slots);
    countsByDate[nearbyDate]++;
  }
  await enforceBusiestDayInvariant();

  return { weekDates, visibleDates, busiestDate, nearbyLocationId, farLocationId, countsByDate, farEventTitle };
}

// Following week's only job is proving week-nav/load-more still shows the
// right events after moving forward — kept loose (just "at least a couple
// of events") and every day capped well under the current week's Friday
// count so it can never steal the heatmap's global max from it.
export async function ensureFollowingWeekFixture(
  page: Page,
  token: string,
  orgId: number,
  locationId: number
): Promise<{ weekDates: string[] }> {
  const nextMonday = mondayOf(new Date());
  nextMonday.setDate(nextMonday.getDate() + 7);
  const weekDates = weekDatesFrom(nextMonday);

  const weekEvents = await fetchWeekEvents(page, weekDates);
  // No date here is ever "today" (this week starts 7+ days out), so the
  // wall-clock-aware base never applies — pass values that just make that
  // explicit rather than reusing this run's real today/now.
  const slots = new SlotAllocator("", 0);
  weekEvents.forEach((e: any) => {
    if (e.location_id) slots.seed((e.start_time || "").slice(0, 10), e.location_id);
  });
  const nonce = Math.random().toString(36).slice(2, 6);
  const shortfall = 2 - weekEvents.length;
  for (let i = 0; i < shortfall; i++) {
    await createFixtureEvent(page, token, orgId, locationId, weekDates[1], "bal-folk", nonce, slots);
  }
  return { weekDates };
}
