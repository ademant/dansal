package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// jCal (RFC 7265) <-> iCalendar text transcoding (#1153).
//
// dansal already has a real RFC 5545 parser/serializer
// (github.com/arran4/golang-ical), used for the existing text/calendar
// GET/POST branches on the events endpoints. Rather than build a second,
// parallel jCal-aware calendar engine, the functions below transcode
// between jCal's JSON tree and iCalendar's content-line text, so
// ics.ParseCalendar / Calendar.Serialize() stay the single source of
// truth for calendar semantics — jCal support is "just" a text
// representation swapped in front of them.
//
// Scope: covers the property set dansal's own calendars actually use
// (UID, SUMMARY, DESCRIPTION, DTSTART/DTEND/DURATION, LOCATION, GEO, URL,
// CATEGORIES, STATUS, RRULE, ...) plus a generic text/date-time/date/
// integer/boolean/uri fallback per RFC 7265 §3.4's default value-type
// table for anything else. RRULE/EXRULE round-trip through jCal's
// "recur" object type with best-effort FREQ/INTERVAL/COUNT/UNTIL/BYxxx
// handling rather than full RFC 5545 recurrence-rule coverage.

// jcalDateTimeProps/jcalDurationProps/... give the RFC 7265 §3.4 default
// jCal value type for iCalendar properties whose type isn't "text" (the
// fallback). Property names are the lowercase form used inside jCal.
var (
	jcalDateTimeProps = map[string]bool{
		"dtstart": true, "dtend": true, "dtstamp": true, "created": true,
		"last-modified": true, "recurrence-id": true, "exdate": true, "rdate": true,
	}
	jcalDurationProps = map[string]bool{"duration": true}
	jcalIntegerProps  = map[string]bool{"sequence": true, "priority": true, "percent-complete": true, "repeat": true}
	jcalURIProps      = map[string]bool{"url": true, "tzurl": true, "attach": true}
	jcalRecurProps    = map[string]bool{"rrule": true, "exrule": true}
	jcalTextListProps = map[string]bool{"categories": true, "resources": true}
)

// ── encode: iCalendar text -> jCal JSON (used by GET responses) ───────────

// icalTextToJCal converts RFC 5545 calendar text (as produced by
// Calendar.Serialize()) into a jCal JSON document.
func icalTextToJCal(icsText string) ([]byte, error) {
	type frame struct {
		name  string
		props []any
		subs  []any
	}
	newFrame := func(name string) *frame {
		// props/subs must start non-nil: json.Marshal renders a nil slice as
		// `null`, but jCal requires `[]` even when empty (jcalToICalText's
		// decode side type-asserts node[1]/node[2] as []any, which a `null`
		// fails).
		return &frame{name: name, props: []any{}, subs: []any{}}
	}
	var stack []*frame
	var root any

	for _, line := range unfoldICalLines(icsText) {
		head, value, ok := splitFirstUnquotedColon(line)
		if !ok {
			return nil, fmt.Errorf("malformed content line (no ':'): %q", line)
		}
		segs := splitOutsideQuotes(head, ';')
		if len(segs) == 0 || segs[0] == "" {
			return nil, fmt.Errorf("malformed content line (no property name): %q", line)
		}
		name := strings.ToUpper(segs[0])

		switch name {
		case "BEGIN":
			stack = append(stack, newFrame(strings.ToLower(value)))
			continue
		case "END":
			if len(stack) == 0 {
				return nil, fmt.Errorf("unexpected END:%s with no open component", value)
			}
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			node := []any{f.name, f.props, f.subs}
			if len(stack) == 0 {
				root = node
			} else {
				parent := stack[len(stack)-1]
				parent.subs = append(parent.subs, node)
			}
			continue
		}

		if len(stack) == 0 {
			return nil, fmt.Errorf("property %s outside any component", name)
		}
		params := map[string][]string{}
		for _, seg := range segs[1:] {
			eq := strings.IndexByte(seg, '=')
			if eq < 0 {
				continue
			}
			pname := strings.ToUpper(seg[:eq])
			for _, pv := range splitOutsideQuotes(seg[eq+1:], ',') {
				params[pname] = append(params[pname], strings.Trim(pv, `"`))
			}
		}
		f := stack[len(stack)-1]
		f.props = append(f.props, icalPropToJCal(name, params, value))
	}

	if root == nil {
		return nil, fmt.Errorf("no top-level component found")
	}
	return json.Marshal(root)
}

func icalPropToJCal(name string, params map[string][]string, rawValue string) []any {
	lname := strings.ToLower(name)
	jtype := jcalDefaultType(lname, params["VALUE"])

	pobj := map[string]any{}
	for k, vals := range params {
		if k == "VALUE" {
			continue // consumed above to pick jtype; jCal encodes the type via the 'type' field itself.
		}
		lk := strings.ToLower(k)
		if len(vals) == 1 {
			pobj[lk] = vals[0]
		} else {
			pobj[lk] = vals
		}
	}

	out := []any{lname, pobj, jtype}
	return append(out, jcalEncodeValue(lname, jtype, rawValue)...)
}

func jcalDefaultType(lname string, valueParam []string) string {
	if len(valueParam) == 1 {
		switch strings.ToUpper(valueParam[0]) {
		case "DATE":
			return "date"
		case "BOOLEAN":
			return "boolean"
		case "INTEGER":
			return "integer"
		case "URI":
			return "uri"
		case "TEXT":
			return "text"
		}
	}
	switch {
	case lname == "geo":
		return "float"
	case jcalDateTimeProps[lname]:
		return "date-time"
	case jcalDurationProps[lname]:
		return "duration"
	case jcalIntegerProps[lname]:
		return "integer"
	case jcalURIProps[lname]:
		return "uri"
	case jcalRecurProps[lname]:
		return "recur"
	default:
		return "text"
	}
}

func jcalEncodeValue(lname, jtype, rawValue string) []any {
	switch jtype {
	case "date-time":
		return []any{icalDateTimeToJCal(rawValue)}
	case "date":
		return []any{icalDateToJCal(rawValue)}
	case "duration", "uri":
		return []any{rawValue}
	case "integer":
		if n, err := strconv.Atoi(rawValue); err == nil {
			return []any{n}
		}
		return []any{rawValue}
	case "boolean":
		return []any{strings.EqualFold(rawValue, "TRUE")}
	case "float":
		if lname == "geo" {
			parts := strings.SplitN(rawValue, ";", 2)
			if len(parts) == 2 {
				if lat, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64); err1 == nil {
					if lon, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64); err2 == nil {
						return []any{[]any{lat, lon}}
					}
				}
			}
		}
		return []any{rawValue}
	case "recur":
		return []any{icalRecurToJCal(rawValue)}
	default: // text and text-list (CATEGORIES, RESOURCES)
		if jcalTextListProps[lname] {
			parts := splitICalTextList(rawValue)
			out := make([]any, len(parts))
			for i, p := range parts {
				out[i] = icalUnescapeText(p)
			}
			return out
		}
		return []any{icalUnescapeText(rawValue)}
	}
}

func icalDateTimeToJCal(v string) string {
	z := ""
	if strings.HasSuffix(v, "Z") {
		z = "Z"
		v = v[:len(v)-1]
	}
	if len(v) < 15 || v[8] != 'T' {
		return v // unexpected shape: pass through rather than guess
	}
	return fmt.Sprintf("%s-%s-%sT%s:%s:%s%s", v[0:4], v[4:6], v[6:8], v[9:11], v[11:13], v[13:15], z)
}

func icalDateToJCal(v string) string {
	if len(v) != 8 {
		return v
	}
	return fmt.Sprintf("%s-%s-%s", v[0:4], v[4:6], v[6:8])
}

// icalRecurToJCal converts an RFC 5545 RRULE/EXRULE value ("FREQ=WEEKLY;
// BYDAY=MO,TU;COUNT=5") into jCal's "recur" object shape. UNTIL is kept as
// its raw iCal date/date-time text rather than further converted — a
// deliberate simplification, documented above.
func icalRecurToJCal(v string) map[string]any {
	out := map[string]any{}
	for _, pair := range strings.Split(v, ";") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 || kv[0] == "" {
			continue
		}
		key := strings.ToLower(kv[0])
		items := strings.Split(kv[1], ",")
		if len(items) > 1 {
			arr := make([]any, len(items))
			for i, it := range items {
				arr[i] = recurItemValue(key, it)
			}
			out[key] = arr
		} else {
			out[key] = recurItemValue(key, kv[1])
		}
	}
	return out
}

func recurItemValue(key, s string) any {
	if key == "until" {
		return s
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return s
}

// ── decode: jCal JSON -> iCalendar text (used by POST bodies) ─────────────

// jcalToICalText converts a jCal JSON document into RFC 5545 calendar text
// suitable for ics.ParseCalendar, so a jCal request body can be fed through
// the exact same event-extraction logic as a text/calendar body.
func jcalToICalText(body []byte) (string, error) {
	var root []any
	if err := json.Unmarshal(body, &root); err != nil {
		return "", fmt.Errorf("invalid jCal JSON: %w", err)
	}
	var b strings.Builder
	if err := writeJCalComponent(&b, root); err != nil {
		return "", err
	}
	return b.String(), nil
}

func writeJCalComponent(b *strings.Builder, node []any) error {
	if len(node) != 3 {
		return fmt.Errorf("jCal component must be a 3-element array [name, properties, components], got %d elements", len(node))
	}
	name, ok := node[0].(string)
	if !ok || name == "" {
		return fmt.Errorf("jCal component name must be a non-empty string")
	}
	props, ok := node[1].([]any)
	if !ok {
		return fmt.Errorf("jCal component %q: properties must be an array", name)
	}
	subs, ok := node[2].([]any)
	if !ok {
		return fmt.Errorf("jCal component %q: sub-components must be an array", name)
	}

	upper := strings.ToUpper(name)
	fmt.Fprintf(b, "BEGIN:%s\r\n", upper)
	for _, p := range props {
		line, err := jcalPropToICalLine(p)
		if err != nil {
			return fmt.Errorf("jCal component %q: %w", name, err)
		}
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	for _, s := range subs {
		sub, ok := s.([]any)
		if !ok {
			return fmt.Errorf("jCal component %q: each sub-component must be an array", name)
		}
		if err := writeJCalComponent(b, sub); err != nil {
			return err
		}
	}
	fmt.Fprintf(b, "END:%s\r\n", upper)
	return nil
}

func jcalPropToICalLine(p any) (string, error) {
	arr, ok := p.([]any)
	if !ok || len(arr) < 4 {
		return "", fmt.Errorf("jCal property must be an array of at least 4 elements [name, parameters, type, value...]")
	}
	name, ok := arr[0].(string)
	if !ok || name == "" {
		return "", fmt.Errorf("jCal property name must be a non-empty string")
	}
	paramsObj, _ := arr[1].(map[string]any)
	jtype, _ := arr[2].(string)
	values := arr[3:]

	var head strings.Builder
	head.WriteString(strings.ToUpper(name))
	keys := make([]string, 0, len(paramsObj))
	for k := range paramsObj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var vals []string
		switch tv := paramsObj[k].(type) {
		case string:
			vals = []string{tv}
		case []any:
			for _, item := range tv {
				vals = append(vals, fmt.Sprint(item))
			}
		default:
			vals = []string{fmt.Sprint(tv)}
		}
		for i, v := range vals {
			vals[i] = icalQuoteParamValueIfNeeded(v)
		}
		fmt.Fprintf(&head, ";%s=%s", strings.ToUpper(k), strings.Join(vals, ","))
	}

	value, err := jcalDecodeValue(strings.ToLower(name), jtype, values)
	if err != nil {
		return "", fmt.Errorf("property %q: %w", name, err)
	}
	return head.String() + ":" + value, nil
}

func icalQuoteParamValueIfNeeded(v string) string {
	if strings.ContainsAny(v, ":;,") {
		return `"` + v + `"`
	}
	return v
}

func jcalDecodeValue(lname, jtype string, values []any) (string, error) {
	switch jtype {
	case "date-time":
		s, ok := scalarString(values)
		if !ok {
			return "", fmt.Errorf("expected a single date-time string value")
		}
		return jcalDateTimeToICal(s), nil
	case "date":
		s, ok := scalarString(values)
		if !ok {
			return "", fmt.Errorf("expected a single date string value")
		}
		return jcalDateToICal(s), nil
	case "duration", "uri":
		s, ok := scalarString(values)
		if !ok {
			return "", fmt.Errorf("expected a single string value")
		}
		return s, nil
	case "integer":
		if len(values) != 1 {
			return "", fmt.Errorf("expected a single integer value")
		}
		switch n := values[0].(type) {
		case float64:
			return strconv.FormatInt(int64(n), 10), nil
		case string:
			return n, nil
		}
		return "", fmt.Errorf("expected a numeric value")
	case "boolean":
		if len(values) != 1 {
			return "", fmt.Errorf("expected a single boolean value")
		}
		b, ok := values[0].(bool)
		if !ok {
			return "", fmt.Errorf("expected a boolean value")
		}
		if b {
			return "TRUE", nil
		}
		return "FALSE", nil
	case "float":
		if lname == "geo" {
			if len(values) == 1 {
				if pair, ok := values[0].([]any); ok && len(pair) == 2 {
					lat, ok1 := pair[0].(float64)
					lon, ok2 := pair[1].(float64)
					if ok1 && ok2 {
						return fmt.Sprintf("%v;%v", lat, lon), nil
					}
				}
			}
			return "", fmt.Errorf("geo value must be a [latitude, longitude] pair")
		}
		s, ok := scalarString(values)
		if !ok {
			return "", fmt.Errorf("expected a single value")
		}
		return s, nil
	case "recur":
		if len(values) != 1 {
			return "", fmt.Errorf("expected a single recur object value")
		}
		obj, ok := values[0].(map[string]any)
		if !ok {
			return "", fmt.Errorf("recur value must be a JSON object")
		}
		return jcalRecurToICal(obj), nil
	default: // text and text-list
		parts := make([]string, len(values))
		for i, v := range values {
			s, ok := v.(string)
			if !ok {
				return "", fmt.Errorf("expected a string value")
			}
			parts[i] = icalEscapeText(s)
		}
		return strings.Join(parts, ","), nil
	}
}

func scalarString(values []any) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	s, ok := values[0].(string)
	return s, ok
}

func jcalDateTimeToICal(s string) string {
	z := ""
	if strings.HasSuffix(s, "Z") {
		z = "Z"
		s = s[:len(s)-1]
	}
	return strings.NewReplacer("-", "", ":", "").Replace(s) + z
}

func jcalDateToICal(s string) string {
	return strings.ReplaceAll(s, "-", "")
}

// jcalRecurToICal converts a jCal "recur" object back into an RFC 5545
// RRULE/EXRULE value string. FREQ is emitted first (customary, not
// required) with remaining keys in alphabetical order for determinism.
func jcalRecurToICal(obj map[string]any) string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sort.SliceStable(keys, func(i, j int) bool { return keys[i] == "freq" && keys[j] != "freq" })

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		var val string
		switch tv := obj[k].(type) {
		case string:
			val = tv
		case float64:
			val = strconv.FormatInt(int64(tv), 10)
		case []any:
			items := make([]string, len(tv))
			for i, it := range tv {
				if f, ok := it.(float64); ok {
					items[i] = strconv.FormatInt(int64(f), 10)
				} else {
					items[i] = fmt.Sprint(it)
				}
			}
			val = strings.Join(items, ",")
		default:
			val = fmt.Sprint(tv)
		}
		parts = append(parts, strings.ToUpper(k)+"="+val)
	}
	return strings.Join(parts, ";")
}

// ── shared low-level content-line helpers ──────────────────────────────────

// unfoldICalLines reverses RFC 5545 line folding (a continuation line
// starts with a single space or tab) and drops blank lines.
func unfoldICalLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l == "" {
			continue
		}
		if (l[0] == ' ' || l[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] += l[1:]
		} else {
			lines = append(lines, l)
		}
	}
	return lines
}

// splitFirstUnquotedColon splits a content line into its "NAME;PARAMS" head
// and value at the first colon that isn't inside a quoted parameter value.
func splitFirstUnquotedColon(line string) (head, value string, ok bool) {
	inQuotes := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inQuotes = !inQuotes
		case ':':
			if !inQuotes {
				return line[:i], line[i+1:], true
			}
		}
	}
	return "", "", false
}

// splitOutsideQuotes splits s on sep, ignoring occurrences inside double
// quotes (RFC 5545 allows quoted parameter values to contain ':', ';', ',').
func splitOutsideQuotes(s string, sep byte) []string {
	var parts []string
	inQuotes := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuotes = !inQuotes
		case sep:
			if !inQuotes {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

// splitICalTextList splits an RFC 5545 comma-separated text-list value
// (e.g. CATEGORIES) on unescaped commas, leaving each part's own \, \; \\
// \n escapes intact for icalUnescapeText to resolve.
func splitICalTextList(v string) []string {
	var parts []string
	var b strings.Builder
	esc := false
	for _, r := range v {
		switch {
		case esc:
			b.WriteRune(r)
			esc = false
		case r == '\\':
			b.WriteRune(r)
			esc = true
		case r == ',':
			parts = append(parts, b.String())
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	return append(parts, b.String())
}

// icalEscapeText escapes a plain string for use as an RFC 5545 TEXT value —
// the inverse of icalUnescapeText (fetchurl.go).
func icalEscapeText(s string) string {
	return strings.NewReplacer(`\`, `\\`, `,`, `\,`, `;`, `\;`, "\n", `\n`).Replace(s)
}
