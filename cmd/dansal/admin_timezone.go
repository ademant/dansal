package main

import "log"

// Admin-socket commands for server.timezone (#1394), following the exact
// shape adminHeartbeatGet/adminHeartbeatSet (admin_notify.go) already use for
// a live config.yaml value: validate, mutate in-memory config, persist via
// saveConfig, and — the one difference from every other admin-socket config
// setter — apply the change to the running process immediately instead of
// requiring a restart, since instanceTimezone is exactly the kind of
// process-wide value main()'s own SIGHUP reloadConfig() already knows how to
// swap safely. dansal-webmin is the intended caller: it has no file access
// of its own and no restart mechanism, only the admin socket, so doing the
// write-and-apply here (where the file and the in-memory value are already
// the same process) is both simpler and safer than having webmin write
// /etc/dansal/<instance>/config.yaml directly.

// adminTimezoneGet reports the instance's effective timezone, for webmin's
// settings page to display before offering a change.
func adminTimezoneGet() adminResponse {
	return adminResponse{OK: true, Data: map[string]string{"timezone": config.Server.Timezone}}
}

// adminTimezoneSet validates req.Timezone as an IANA zone name, persists it
// to config.yaml, and applies it to the running process immediately — a
// caller never needs to separately restart or reload dansal for this to take
// effect. Rejects an invalid zone before touching anything, matching every
// other admin-socket setter's validate-before-write order.
func adminTimezoneSet(req adminRequest) adminResponse {
	if req.Timezone == "" {
		return adminResponse{OK: false, Error: "timezone is required"}
	}
	loc, err := validateInstanceTimezone(req.Timezone)
	if err != nil {
		return adminResponse{OK: false, Error: err.Error()}
	}
	prev := config.Server.Timezone
	config.Server.Timezone = req.Timezone
	if configFilePath != "" {
		if err := saveConfig(configFilePath); err != nil {
			config.Server.Timezone = prev // don't apply a change that failed to persist
			return adminResponse{OK: false, Error: "config save failed: " + err.Error()}
		}
	}
	instanceTimezone = loc
	log.Printf("server.timezone changed to %q via admin socket — every event now displays in this zone", req.Timezone)
	return adminResponse{OK: true}
}
