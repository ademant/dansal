package main

import (
	"encoding/json"
	"html/template"
	"strconv"
)

// Location template functions — one slice of the merged tmplFuncMap, split
// out of frontend.go (#1031).

// locationOption is the flattened option entry shared by locationsJSON and
// timetableLocationOptionsJSON.
type locationOption struct {
	ID     int    `json:"id"`
	Label  string `json:"label"`
	Town   string `json:"town,omitempty"`
	OrgIDs []int  `json:"orgIDs"`
}

// flattenLocationOptions flattens top-level locations and their room children
// into one JS option list. Rooms are labelled "RoomName — BuildingName" to
// disambiguate when two buildings share a room name. Rooms inherit the
// parent's orgIDs for org-based filtering unless they carry their own.
func flattenLocationOptions(locs []Location) []locationOption {
	items := make([]locationOption, 0, len(locs))
	for _, l := range locs {
		if l.ParentID != nil {
			continue // rooms appear as children of their building; skip here to avoid duplicates
		}
		label := l.Location
		if l.ShortName != "" {
			label = l.ShortName
		}
		bname := label
		if l.Town != "" {
			label += ", " + l.Town
		}
		orgIDs := l.OrganizationIDs
		if orgIDs == nil {
			orgIDs = []int{}
		}
		items = append(items, locationOption{ID: l.ID, Label: label, Town: l.Town, OrgIDs: orgIDs})
		for _, c := range l.Children {
			clabel := c.Location
			if c.ShortName != "" {
				clabel = c.ShortName
			}
			childOrgIDs := c.OrganizationIDs
			if len(childOrgIDs) == 0 {
				childOrgIDs = orgIDs
			}
			items = append(items, locationOption{ID: c.ID, Label: clabel + " — " + bname, Town: l.Town, OrgIDs: childOrgIDs})
		}
	}
	return items
}

var tmplFuncsLocation = template.FuncMap{
	// roomName looks up which of a building's rooms (children) an event's
	// LocationID refers to, for the Room column on /admin/location/{id} (#883).
	"roomName": func(children []Location, id *int) string {
		if id == nil {
			return ""
		}
		for _, c := range children {
			if c.ID == *id {
				return c.Location
			}
		}
		return ""
	},
	"derefInt": func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	},
	// locName returns the short_name when set, otherwise the full location name.
	// Useful for compact displays where town/address are shown separately.
	"locName": func(l Location) string {
		if l.ShortName != "" {
			return l.ShortName
		}
		return l.Location
	},
	"intVal": func(p *int) string {
		if p == nil {
			return ""
		}
		return strconv.Itoa(*p)
	},
	"floatVal": func(f *float64) string {
		if f == nil {
			return ""
		}
		return strconv.FormatFloat(*f, 'f', -1, 64)
	},
	// pct renders a 0-1 fraction (e.g. Location.PlanX/PlanY) as a percentage
	// number for use in a CSS "%" value — floatVal alone would render 0.6 as
	// "0.6%" instead of "60%" (#880).
	"pct": func(f *float64) string {
		if f == nil {
			return ""
		}
		return strconv.FormatFloat(*f*100, 'f', -1, 64)
	},
	"int64Val": func(n *int64) string {
		if n == nil {
			return ""
		}
		return strconv.FormatInt(*n, 10)
	},
	// unplacedRooms/placedRooms split a building's Children (#877) by whether
	// they've been dragged onto the building's site-plan image yet.
	"unplacedRooms": func(children []Location) []Location {
		var out []Location
		for _, c := range children {
			if c.PlanX == nil || c.PlanY == nil {
				out = append(out, c)
			}
		}
		return out
	},
	"placedRooms": func(children []Location) []Location {
		var out []Location
		for _, c := range children {
			if c.PlanX != nil && c.PlanY != nil {
				out = append(out, c)
			}
		}
		return out
	},
	// topLocationID resolves the top-level (building) location ID for an
	// event whose location may itself be a room (#687): a room is a child
	// Location with ParentID set, but venue pickers only ever offer the
	// top-level building, so callers select against this instead of
	// Event.LocationID directly.
	"topLocationID": func(e Event) int {
		if e.Location == nil {
			return 0
		}
		if e.Location.ParentID != nil {
			return *e.Location.ParentID
		}
		return e.Location.ID
	},
	// locationsJSON flattens top-level locations and their room children into
	// one JS array (see flattenLocationOptions).
	"locationsJSON": func(locs []Location) template.JS {
		b, _ := json.Marshal(flattenLocationOptions(locs))
		return template.JS(b)
	},
	// timetableLocationOptionsJSON flattens every top-level location plus all
	// of their rooms (children) into one searchable option list for the
	// timetable's per-row location autocomplete (#889) — unlike locationsJSON,
	// this isn't restricted to the event's own building.
	"timetableLocationOptionsJSON": func(locs []Location) template.JS {
		b, _ := json.Marshal(flattenLocationOptions(locs))
		return template.JS(b)
	},
	"locAttrs": func(loc *Location) map[string]bool {
		if loc == nil {
			return nil
		}
		return loc.Attributes
	},
	"mergeAttrs": func(loc, evt map[string]bool) map[string]bool {
		merged := make(map[string]bool, len(loc)+len(evt))
		for k, v := range loc {
			merged[k] = v
		}
		for k, v := range evt {
			merged[k] = v
		}
		return merged
	},
	"attrState": func(attrs map[string]bool, key string) string {
		v, ok := attrs[key]
		if !ok {
			return ""
		}
		if v {
			return "1"
		}
		return "0"
	},
}
