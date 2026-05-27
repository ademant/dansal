package main

import (
	"net/url"
	"strconv"
)

// parseFormIDs parses a list of integer IDs from the named form field.
func parseFormIDs(form url.Values, field string) []int {
	var ids []int
	for _, s := range form[field] {
		if n, err := strconv.Atoi(s); err == nil {
			ids = append(ids, n)
		}
	}
	return ids
}

// parseFormOptionalInt returns a pointer to the integer value of the named
// form field, or nil when the field is absent or not a valid integer.
func parseFormOptionalInt(form url.Values, field string) *int {
	if v := form.Get(field); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return &n
		}
	}
	return nil
}
