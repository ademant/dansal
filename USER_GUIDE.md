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

### Privacy authentications
Users who want to manage events have several methods for authentication:
- **Old shool passwords**: Use username and password like many other services
- **TOTP**: Google authenticator can be used as second factor
- **macic link**: When email / telegram / matrix is provided and verified, a login link is sent to this address
- **Passkey**: Use modern Passkey, which is secure stored on your device.

Only with Passkey it is possible to manage events without username.

### Account Types & Roles

| Role | Permissions |
|---|---|
| **admin** | Full access to all features and settings |
| **publisher** | Create/edit events, manage locations and musicians |
| **user** | Create events for their own organization only |

### Profile Settings

- **Personal Info**: Name, email, profile picture
- **Notification Preferences**: Email, Telegram, Matrix alerts
- **Language Preferences**: Set your default language
- **Account Security**: Change password, enable 2FA
- **Linked Accounts**: Connect Telegram, Matrix, etc.

## ✨ Event Creation

### Major feature
The data are stored in several structure to make management of new events as easy as possible:

- Events are assigned to location and location based informations are bound to the event (adress, accessibility etc.)
- Events are assigned to organization to filter easily for events of a specific organization
- Events can be created using templates
- Events can be grouped into series with easy methods for editing details of single event
- Events can be imported automatic by iCal/JSON feeds

### Basic Event Creation

1. Navigate to **Events → Create New Event**
2. Fill in basic information:
   - **Title**: Clear, descriptive name
   - **Start/End Date & Time**: Event duration
   - **Location**: Select existing or create new venue
   - **Organization**: Select your organization
3. Choose **Event Type**:
   - **Ball** (social dance event)
   - **Workshop** (with difficulty: beginner/advanced/pro)
   - **Festival** (multi-day event)
   - **Combination** (e.g., workshop + ball)

### Event Details

- **Description**: Rich text with formatting options
- **Pricing**: Add multiple pricing tiers (free, donation, early bird, regular, door, etc.)
- **Booking URL**: Link to ticketing system
- **Tags**: Dance styles, themes, special features
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
- Link to MusicBrainz for automatic discography
- Add social media links for each musician

### Event Management

- **Edit**: Update any event details
- **Cancel**: Mark event as cancelled (visible with notice)
- **Duplicate**: Create copy for recurring events
- **Export**: Download as iCal or JSON

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
- **Templates**: Create reusable event configurations

### iCal Feed Integration

1. Go to organization edit page
2. Add iCal feed URL
3. Set import schedule (manual or automatic)
4. Map feed fields to dansal fields
5. Test import and review events

## 🎻 Musician Profiles

### Adding Musicians

1. Go to **Musicians → Add New Musician**
2. Fill in details:
   - Name, description
   - MusicBrainz ID (for automatic discography)
   - Social media links (Mastodon, Instagram, etc.)
   - Website, SoundCloud, Bandcamp links

### Musician Features

- **Discography**: Automatically fetched from MusicBrainz
- **Upcoming Events**: Shows all events featuring this musician
- **Band Members**: For group profiles
- **Genres**: Tag by musical style

## 🚀 Advanced Features

### Event Templates

Create reusable templates for:
- Regular weekly events
- Standard workshop formats
- Festival structures
- Common pricing schemes

### Bulk Operations

- **Bulk Import**: Upload multiple events via CSV or JSON
- **Bulk Export**: Download events for backup or migration
- **Bulk Publishing**: Publish multiple draft events at once

### Recurring Events

Set up recurring events with:
- Daily, weekly, monthly, or yearly patterns
- Custom exceptions (e.g., skip holidays)
- Automatic end dates
- Variable pricing by date

---

**Need more help?** Check the **[Admin Guide](ADMIN_GUIDE.md)** for system configuration or **[Developer Guide](DEVELOPER_GUIDE.md)** for API access.

**Found a bug?** Report issues on [GitHub](https://github.com/ademant/dansal/issues)
