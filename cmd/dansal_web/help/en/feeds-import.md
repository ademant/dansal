# Importing events from feeds

Go to **Admin → Fetch URLs** to manage automatic feed sources.

## Supported formats

- **iCal / ICS** — standard calendar format
- **JSON** — Gancio or dansal-compatible JSON event feeds
- **RSS** — some event aggregators publish RSS

## Adding a feed

1. Click **+ Add feed URL**
2. Paste the feed URL
3. Select the format (auto-detected where possible)
4. Optionally assign an organisation and add tags
5. Click **Save**

The feed is fetched on the next automatic poll cycle. Click **Run** for an immediate fetch.

## Templates

Apply an event template to a feed to fill in fields that the source does not provide (organisation, dance styles, tags). Set the template mode:

- **Feed dominates** — the template fills only empty fields
- **Template dominates** — template values overwrite feed values

## Reviewing imports

Imported events appear in the event list. Use the **Unpublished** filter to review them before making them public.
