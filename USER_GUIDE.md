# 👥 User Guide - Managing Events & Organizations

For event organizers, publishers, and regular users who create and manage content on dansal.

## 📋 Table of Contents

- [Account Management](#-account-management)
- [Event Creation](#-event-creation)
- [Venue Management](#-venue-management)
- [Organization Management](#-organization-management)
- [Musician Profiles](#-musician-profiles)
- [Advanced Features](#-advanced-features)
- [User Roles & Permissions](#-user-roles--permissions)

## 🔑 Account Management

### Creating an Account

Getting an account via
1. **Self-registration**: Create a request, which is confirmed by admin
2. **Invitation link**: Already existing user can provide you link or QR code for registration

### Authentication methods
Users who want to manage events have several methods for authentication:
- **Old school passwords**: Use email/display name and password like many other services
- **TOTP**: A TOTP authenticator app can be used as a second factor
- **Magic link**: When email, Telegram, or Matrix is provided and verified, a one-time login link is sent to that address
- **Passkey**: Use a modern passkey stored securely on your device

Only with a passkey is it possible to manage events without an email address.

### Account Types & Roles

| Role | Permissions |
|---|---|
| **admin** | Full access to all features and settings |
| **publisher** | Create/edit events, manage locations and musicians |
| **user** | Create events for their own organization only |

### Profile Settings

- **Personal Info**: Display name, email
- **Notification Preferences**: Email, Telegram, Matrix alerts
- **Language Preferences**: Set your default language
- **Account Security**: Change password, manage passkeys
- **Linked Accounts**: Connect Telegram, Matrix, Mastodon, etc.

## ✨ Event Creation

### Key concepts
Data is structured to make managing new events as easy as possible:

- Events are assigned to a location; location details (address, accessibility, etc.) are inherited by the event
- Events are assigned to an organization for easy filtering
- Events can be grouped into series, with tools for editing individual occurrences
- Events can be imported automatically via iCal or JSON feeds

### Basic Event Creation

1. Navigate to **Events → Create New Event**
2. Fill in basic information:
   - **Title**: Clear, descriptive name
   - **Start/End Date & Time**: Event duration
   - **Location**: Select existing or create new venue
   - **Organization**: Select your organization
3. Choose **Event Type**:
   - **Ball** (social dance event)
   - **Workshop** (with difficulty: beginner/intermediate/advanced)
   - **Festival** (multi-day event)
   - **Combination** (e.g., workshop + ball)

### Event Details

- **Description**: Formatted text
- **Pricing**: Add multiple pricing tiers (free, donation, early bird, regular, door, etc.)
- **Booking URL**: Link to ticketing system
- **Tags**: Event format, type, and level tags
- **Featured Image**: Upload event poster or photo

### Advanced Event Options

#### Timetable (for multi-slot events)
```
Room A:
- 14:00-15:30: Workshop (Beginner) with Band X
- 16:00-17:30: Workshop (Advanced) with Band Y
- 20:00-01:00: Evening Ball with Band Z

Room B:
- 15:00-16:30: Technique Class
- 17:00-18:30: Musicality Workshop
```

#### Musicians
- Search and add musicians from the database
- Link to a musician's MusicBrainz profile page
- Add social media links for each musician

### Event Management

- **Edit**: Update any event details
- **Cancel**: Mark event as cancelled (visible with notice)
- **Duplicate**: Create a copy of an event

## 🏢 Venue Management

### Creating a Venue

1. Go to **Locations → Add New Location**
2. Fill in venue details:
   - Name, short name, address
   - Geo coordinates (for map display)
   - Website, contact info
   - Accessibility features
   - Parking information
   - Dance floor details (size, surface, etc.)

### Assigning Venues to Organizations

1. Edit the venue
2. Select the organization from dropdown
3. Save changes

## 🎭 Organization Management

### Creating an Organization

1. Go to **Organizations → New Organization**
2. Fill in organization details:
   - Name, description
   - Website, social media links
   - Contact email
   - Logo/image

### Organization Features

- **Multiple Venues**: Assign all venues used by this organization
- **iCal Feeds**: Set up automatic event imports
- **Members**: Add users who can create/manage events

### iCal Feed Integration

1. Go to organization edit page
2. Add iCal or JSON feed URL
3. Set import schedule (manual or automatic)
4. Optionally configure a template to map or override feed fields
5. Test import and review events

## 🎻 Musician Profiles

### Adding Musicians

1. Go to **Musicians → Add New Musician**
2. Fill in details:
   - Name, description
   - MusicBrainz ID (links to the musician's MusicBrainz profile)
   - Social media links (Mastodon, Instagram, etc.)
   - Website, SoundCloud, Bandcamp links

### Musician Features

- **Upcoming Events**: Shows all events featuring this musician
- **MusicBrainz link**: Links to the musician's profile on musicbrainz.org

## 🚀 Advanced Features

### Event Series

Group related events into a series for easier management:
- Edit shared details across all events in the series
- Override details for individual occurrences

### Recurring iCal Events

When importing via iCal feeds, recurring events (RRULE) are expanded automatically into individual occurrences.

---

**Need more help?** Check the **[Admin Guide](ADMIN_GUIDE.md)** for system configuration or **[Developer Guide](DEVELOPER_GUIDE.md)** for API access.

**Found a bug?** Report issues on [GitHub](https://github.com/ademant/dansal/issues)
