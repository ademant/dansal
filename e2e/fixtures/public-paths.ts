// Public pages an anonymous visitor reaches without any id in the URL.
// Shared by the Playwright JS-error sweep (public-pages-js-errors.spec.ts)
// and the Ladybird WebDriver runner (ladybird/run.ts), so both engines check
// the same list (#1906).
export const PUBLIC_PATHS = [
  "/",
  "/events/suggest",
  "/feeds/suggest",
  "/search",
  "/board",
  "/organizations",
  "/musicians",
  "/instructors",
  "/login",
  "/register",
  "/help",
  "/cities",
  "/festivals",
  // #1542: impressum/privacy/terms render through the same base.html chrome
  // as every other public page (and 404 harmlessly when unconfigured — see
  // about-and-info-pages.spec.ts), so they belong in this sweep too.
  "/impressum",
  "/privacy",
  "/terms",
];

// Detail pages, reached the way a visitor reaches them: the first link
// matching `link` on the list page `from`.
export const PUBLIC_DETAIL_LINKS: { from: string; link: string }[] = [
  { from: "/", link: 'a[href^="/events/"]' },
  { from: "/organizations", link: 'a[href^="/org/"]' },
  { from: "/cities", link: 'a[href^="/city/"]' },
  { from: "/musicians", link: 'a[href^="/musicians/"]' },
];
