package main

var APContext = []any{
	"https://www.w3.org/ns/activitystreams",
	"https://w3id.org/security/v1",
	map[string]any{
		"toot":                      "http://joinmastodon.org/ns#",
		"sc":                        "http://schema.org#",
		"discoverable":              "toot:discoverable",
		"indexable":                 "toot:indexable",
		"manuallyApprovesFollowers": "as:manuallyApprovesFollowers",
		"PostalAddress":             "sc:PostalAddress",
		"streetAddress":             "sc:streetAddress",
		"postalCode":                "sc:postalCode",
		"addressLocality":           "sc:addressLocality",
		"addressRegion":             "sc:addressRegion",
		"addressCountry":            "sc:addressCountry",
		"Hashtag":                   "as:Hashtag",
	},
}

type PublicKey struct {
	ID              string `json:"id"`
	Owner           string `json:"owner"`
	PublicKeyPem    string `json:"publicKeyPem"`
	PublicKeyMultibase string `json:"publicKeyMultibase,omitempty"`
}

type APEndpoints struct {
	SharedInbox string `json:"sharedInbox"`
}

type Actor struct {
	Context                   any `json:"@context"`
	Type                      string      `json:"type"`
	ID                        string      `json:"id"`
	Name                      string      `json:"name"`
	Summary                   string      `json:"summary,omitempty"`
	URL                       string      `json:"url,omitempty"`
	PreferredUsername         string      `json:"preferredUsername"`
	Inbox                     string      `json:"inbox"`
	Outbox                    string      `json:"outbox"`
	Followers                 string      `json:"followers"`
	ManuallyApprovesFollowers bool        `json:"manuallyApprovesFollowers"`
	Discoverable              bool        `json:"discoverable"`
	Indexable                 bool        `json:"indexable"`
	Endpoints                 *APEndpoints `json:"endpoints,omitempty"`
	Icon                      *APDocument `json:"icon,omitempty"`
	PublicKey                 PublicKey   `json:"publicKey"`
}

type Activity struct {
	Context any `json:"@context,omitempty"`
	Type    string      `json:"type"`
	ID      string      `json:"id"`
	Actor   string      `json:"actor"`
	Object  any `json:"object"`
	To      []string    `json:"to,omitempty"`
	CC      []string    `json:"cc,omitempty"`
}

type APPostalAddress struct {
	Type            string `json:"type"`
	StreetAddress   string `json:"streetAddress,omitempty"`
	PostalCode      string `json:"postalCode,omitempty"`
	AddressLocality string `json:"addressLocality,omitempty"`
	AddressCountry  string `json:"addressCountry,omitempty"`
}

type APPlace struct {
	Type      string           `json:"type"`
	Name      string           `json:"name"`
	Latitude  *float64         `json:"latitude,omitempty"`
	Longitude *float64         `json:"longitude,omitempty"`
	Address   *APPostalAddress `json:"address,omitempty"`
}

type APHashtag struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Href string `json:"href"`
}

type APDocument struct {
	Type      string `json:"type"`
	MediaType string `json:"mediaType"`
	URL       string `json:"url"`
	Name      string `json:"name,omitempty"`
}

type APEvent struct {
	Context      any          `json:"@context,omitempty"`
	Type         string       `json:"type"`
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Content      string       `json:"content,omitempty"`
	MediaType    string       `json:"mediaType,omitempty"`
	StartTime    string       `json:"startTime,omitempty"`
	EndTime      string       `json:"endTime,omitempty"`
	Published    string       `json:"published,omitempty"`
	Updated      string       `json:"updated,omitempty"`
	AttributedTo string       `json:"attributedTo,omitempty"`
	Organizer    any          `json:"organizer,omitempty"`
	To           []string     `json:"to,omitempty"`
	CC           []string     `json:"cc,omitempty"`
	Location     *APPlace     `json:"location,omitempty"`
	URL          string       `json:"url,omitempty"`
	Tag          []APHashtag  `json:"tag,omitempty"`
	Attachment   []APDocument `json:"attachment,omitempty"`
	Duration     string       `json:"duration,omitempty"` // ISO 8601 duration
	Category     []string     `json:"category,omitempty"` // Event categories
	Icon         *APDocument  `json:"icon,omitempty"`     // Event icon
}

type APLocationActor struct {
	Context           any          `json:"@context"`
	Type              string       `json:"type"`
	ID                string       `json:"id"`
	Name              string       `json:"name"`
	Summary           string       `json:"summary,omitempty"`
	PreferredUsername string       `json:"preferredUsername"`
	Inbox             string       `json:"inbox"`
	Outbox            string       `json:"outbox"`
	Followers         string       `json:"followers"`
	Location          *APPlace     `json:"location,omitempty"`
	Icon              *APDocument  `json:"icon,omitempty"`
	PublicKey         PublicKey    `json:"publicKey"`
}

type APMusicianActor struct {
	Context           any          `json:"@context"`
	Type              string       `json:"type"`
	ID                string       `json:"id"`
	Name              string       `json:"name"`
	Summary           string       `json:"summary,omitempty"`
	PreferredUsername string       `json:"preferredUsername"`
	Inbox             string       `json:"inbox"`
	Outbox            string       `json:"outbox"`
	Followers         string       `json:"followers"`
	Image             *APDocument  `json:"image,omitempty"`
	MusicBrainzID     string       `json:"musicbrainzId,omitempty"`
	PublicKey         PublicKey    `json:"publicKey"`
}

type APRSVP struct {
	Context    any    `json:"@context"`
	Type       string `json:"type"`
	ID         string `json:"id"`
	Actor      string `json:"actor"`
	Object     string `json:"object"`
	RSVPType   string `json:"rsvpType"` // "Yes", "No", "Maybe", "Interested"
	Published  string `json:"published,omitempty"`
	To         []string `json:"to,omitempty"`
	CC         []string `json:"cc,omitempty"`
}

type APLike struct {
	Context    any    `json:"@context"`
	Type       string `json:"type"`
	ID         string `json:"id"`
	Actor      string `json:"actor"`
	Object     string `json:"object"`
	Published  string `json:"published,omitempty"`
	To         []string `json:"to,omitempty"`
	CC         []string `json:"cc,omitempty"`
}

type APAnnounce struct {
	Context    any    `json:"@context"`
	Type       string `json:"type"`
	ID         string `json:"id"`
	Actor      string `json:"actor"`
	Object     string `json:"object"`
	Published  string `json:"published,omitempty"`
	To         []string `json:"to,omitempty"`
	CC         []string `json:"cc,omitempty"`
}

type APComment struct {
	Context    any    `json:"@context"`
	Type       string `json:"type"`
	ID         string `json:"id"`
	Actor      string `json:"actor"`
	InReplyTo  string `json:"inReplyTo,omitempty"`
	Content    string `json:"content"`
	MediaType  string `json:"mediaType,omitempty"`
	Published  string `json:"published,omitempty"`
	To         []string `json:"to,omitempty"`
	CC         []string `json:"cc,omitempty"`
	Tag        []APHashtag `json:"tag,omitempty"`
}

type OrderedCollection struct {
	Context    any `json:"@context"`
	Type       string      `json:"type"`
	ID         string      `json:"id"`
	TotalItems int         `json:"totalItems"`
	First      string      `json:"first,omitempty"`
	Items      []string    `json:"orderedItems,omitempty"`
}

type OrderedCollectionPage struct {
	Context      any   `json:"@context"`
	Type         string        `json:"type"`
	ID           string        `json:"id"`
	PartOf       string        `json:"partOf"`
	TotalItems   int           `json:"totalItems"`
	OrderedItems []any `json:"orderedItems"`
	Next         string        `json:"next,omitempty"`
	Prev         string        `json:"prev,omitempty"`
}

type WebFingerLink struct {
	Rel  string `json:"rel"`
	Type string `json:"type,omitempty"`
	Href string `json:"href,omitempty"`
}

type WebFinger struct {
	Subject string          `json:"subject"`
	Aliases []string        `json:"aliases,omitempty"`
	Links   []WebFingerLink `json:"links"`
}

type NodeInfoSoftware struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Repository string `json:"repository,omitempty"`
	Homepage   string `json:"homepage,omitempty"`
}

type NodeInfoUsage struct {
	Users struct {
		Total int `json:"total"`
	} `json:"users"`
}

type NodeInfoMaintainer struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

type NodeInfoMetadata struct {
	NodeDescription string              `json:"nodeDescription,omitempty"`
	Maintainer      *NodeInfoMaintainer `json:"maintainer,omitempty"`
}

type NodeInfo struct {
	Version           string            `json:"version"`
	Software          NodeInfoSoftware  `json:"software"`
	Protocols         []string          `json:"protocols"`
	Usage             NodeInfoUsage     `json:"usage"`
	OpenRegistrations bool              `json:"openRegistrations"`
	Metadata          *NodeInfoMetadata `json:"metadata,omitempty"`
}
