## Problem
The board notes form has several UX friction points:
1. **Verification visibility:** Unverified posts appear alongside verified ones, making it hard to find trustworthy coordination posts
2. **Type selection:** Dropdown is less visual than icons; users miss the post type categories
3. **Location precision:** City text input lacks geolocation context; no way to distinguish between districts (e.g., Köln vs. Köln-Ehrenfeld)
4. **Persons input:** Standard HTML number input has spinner arrows on the right side; awkward UX on mobile for adjusting group size
5. **Contact method selector:** The current radio buttons for choosing email vs Telegram are low-visibility

## Solution

### 1. Verified-Only Display
- Show **only verified posts** (unverified posts filtered out by default)
- Order posts chronologically by creation date
- Add icon filter buttons *above* the post list (🚗, 🏠, 🎟️, 🔍, 📦) to filter by type
- All type filters start "active" (showing all types)
- Display each post as: `type-icon + type-label + city-district + persons + nickname`

### 2. Icon Tabs Replace Type Dropdown
- Remove the `<select>` dropdown for post type from the form
- Add horizontal icon button group (tab-bar style): 8 buttons for the 8 post types
  - 🚗 Ride Offer / Ride Request
  - 🏠 Sleep Offer / Sleep Request
  - 🎟️ Ticket Offer / Ticket Request
  - 🔍 Lost Item
  - 📦 Found Item
- Clicking an icon tab **pre-selects that type** in the form (removes the dropdown entirely)
- Form fields adjust dynamically (e.g., hide "city" for lost/found items; hide "persons" where not applicable)
- Remove contact method selector from the **create** form — posting doesn't require choosing how others reach you; email/telegram fields remain optional for replies

### 3. Nominatim Location Search with OSM ID
- Replace plain `city` text input with a Nominatim search interface (reuse the implementation from `events_suggest.html`)
- **DB migration:** Add `osm_id INTEGER` column to `contact_posts` table with safety-net migration
- Search behavior:
  - User types partial city name (e.g., "Köln")
  - Nominatim returns matches; user clicks to select
  - Form populates: city name **with district** (e.g., "Köln-Ehrenfeld") and stores osm_id
  - Districts are populated from Nominatim address details; city and district displayed as `City - District`
- For now, location data is display-only; future enhancements can add geo-filtering

### 4. Persons Input: Left/Right Arrows + Mobile Swipe
- Replace standard HTML number spinner with custom control
- **Desktop:** Left (−) button on left, right (+) button on right
- **Mobile:** Support horizontal swipe gestures (swipe left = decrease, swipe right = increase)
- Constrain to valid range (1–20)
- Center value between arrows; hide the control when not applicable (lost/found)

### 5. Contact Method Selector: Icon Toggle + Data Deletion on Remove
- Replace radio buttons for choosing reply method (email vs Telegram) with **icon buttons** (envelope, Telegram paper-plane). Behavior:
  - Only one icon can be active at a time (mutually exclusive)
  - Clicking an icon toggles selection for the contact reply form
  - Use the same pattern in both the per-post contact form and the manage page
- **Data deletion on post removal:** When a contact post is deleted (via the manage flow or admin delete), any stored private contact fields (`email`, `telegram_username`, `poster_telegram_chat_id`, etc.) must be removed/zeroed in the database as part of the delete operation, not just the row deletion. This avoids leaking contact info in backups or logs and ensures privacy when a post is removed.
  - Backend change: `DeleteContactPost` / `DeleteContactPostByManageToken` API should clear these fields (or set them to empty strings) before removing or archive them via an irreversible wipe step.

## Scope
- **Templates:** `event.html` (board section), contact-manage templates
- **JS:** Icon tab logic, Nominatim search, persons control, icon-based contact selector
- **DB:** Add `osm_id` to `contact_posts`; ensure deletion wipes contact fields
- **Go handlers:** `contactBoardPostHandler`, `contactBoardDeleteHandler`, `contactManagePostHandler` & API `DeleteContactPost*` to accept/store osm_id and ensure contact data wiping
- **i18n:** New strings for `city search placeholder`, `no results`, `persons controls`, tooltips for contact icons
- **CSS:** Style icon tabs, filter buttons, custom persons input, swipe hints

## Implementation Priority
1. DB migration: add `osm_id` column
2. Nominatim search UI in form
3. Icon type tabs (replace dropdown)
4. Custom persons input with L/R arrows + swipe
5. Icon contact-method selector (mutually exclusive)
6. Backend: ensure contact fields are deleted/wiped when a post is removed
7. Filter buttons above list (type filtering)
8. Verified-only display
9. Remove contact method from create form
10. Display city-district on posts
11. Styling/responsiveness

## Notes for implementer
- Reuse `events_suggest.html` Nominatim client code to populate hidden `osm_id` field
- Follow DB migration safety-net pattern from `CLAUDE.md` when adding `osm_id`
- Touch only `contact_posts` schema and board templates/handlers; avoid unrelated migrations or refactors
- Add tests for: Nominatim population of `osm_id`, persons control boundaries, and ensure delete endpoint wipes contact fields
