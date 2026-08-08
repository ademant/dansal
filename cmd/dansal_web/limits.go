package main

// Inbound/outbound body-size caps used by the ActivityPub and WebAuthn
// handlers. Named so the magic numbers can't drift between call sites.
const (
	// maxInboundJSONBody caps request bodies read by the web frontend
	// (ActivityPub inboxes, WebAuthn proxy forwards).
	maxInboundJSONBody = 1 << 20 // 1 MiB
	// maxRemoteJSONBody caps JSON responses fetched from remote
	// ActivityPub/WebFinger servers before decoding.
	maxRemoteJSONBody = 1 << 19 // 512 KiB
)
