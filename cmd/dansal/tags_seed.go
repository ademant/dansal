package main

import (
	"database/sql"
	_ "embed"
	"log"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v2"
)

//go:embed tags.yaml
var defaultTagsYAML []byte

// tagSeedEntry mirrors one entry of tags.yaml — see that file's header
// comment for the meaning of each field.
type tagSeedEntry struct {
	Slug      string `yaml:"slug"`
	Name      string `yaml:"name"`
	Category  string `yaml:"category"`
	Emoji     string `yaml:"emoji,omitempty"`
	HomeGroup string `yaml:"home_group,omitempty"`
	Color     string `yaml:"color,omitempty"`
}

type tagSeedFile struct {
	Tags []tagSeedEntry `yaml:"tags"`
}

// tagSeedSlugRe restricts slugs and home_group keys to characters safe to
// use as an HTML data-* attribute name on the dansal_web side (home_group
// becomes e.g. data-ball="1" — see homeGroups in cmd/dansal_web). A
// malformed custom tags_file shouldn't be able to break page rendering.
var tagSeedSlugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// validTagCategories mirrors the tags.category CHECK constraint.
var validTagCategories = map[string]bool{"format": true, "level": true, "type": true}

// seedDefaultTags upserts the event-format tag vocabulary from tags.yaml
// (#1173): the embedded default (the balfolk vocabulary dansal has always
// shipped with, preserving today's exact slugs/names/categories/home-page
// grouping) or, when overridePath is set and readable, that file instead —
// letting an instance serving a different dance/event community ship its
// own vocabulary at install time without a code fork. Declaration order in
// the YAML becomes each tag's sort_order, which in turn is what makes the
// home-page button row order and the map-marker type priority (see
// homeGroups in cmd/dansal_web) deterministic.
//
// Tags are entirely seed-derived — there's still no admin UI to create or
// edit one; validateTags rejects anything not already in this table — so
// this is a plain upsert of every seed entry, safe to call on every
// startup: it creates missing rows (fresh install, a newly added seed
// entry) and backfills name/category/emoji/home_group/sort_order on
// existing ones. That backfill matters once, for any row seeded before
// #1173 added the emoji/home_group/sort_order columns — INSERT OR IGNORE
// alone would never touch an already-existing row.
func seedDefaultTags(db *sql.DB, overridePath string) {
	data := defaultTagsYAML
	if overridePath != "" {
		if raw, err := os.ReadFile(overridePath); err == nil {
			data = raw
		} else {
			log.Printf("seedDefaultTags: could not read tags_file %q, using built-in default: %v", overridePath, err)
		}
	}
	var f tagSeedFile
	if err := yaml.Unmarshal(data, &f); err != nil || len(f.Tags) == 0 {
		log.Printf("seedDefaultTags: invalid or empty tags seed (%v), falling back to the built-in default", err)
		f = tagSeedFile{}
		if err := yaml.Unmarshal(defaultTagsYAML, &f); err != nil {
			log.Printf("seedDefaultTags: embedded default tags.yaml is invalid: %v", err)
			return
		}
	}
	for i, t := range f.Tags {
		slug := strings.TrimSpace(t.Slug)
		if slug == "" || !tagSeedSlugRe.MatchString(slug) {
			log.Printf("seedDefaultTags: skipping tag with invalid slug %q", t.Slug)
			continue
		}
		if !validTagCategories[t.Category] {
			log.Printf("seedDefaultTags: skipping tag %q with unknown category %q", slug, t.Category)
			continue
		}
		homeGroup := strings.TrimSpace(t.HomeGroup)
		if homeGroup != "" && !tagSeedSlugRe.MatchString(homeGroup) {
			log.Printf("seedDefaultTags: skipping tag %q with invalid home_group %q", slug, t.HomeGroup)
			continue
		}
		if _, err := db.Exec(`INSERT INTO tags (slug, name, category, emoji, home_group, color, sort_order)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(slug) DO UPDATE SET
				name=excluded.name, category=excluded.category,
				emoji=excluded.emoji, home_group=excluded.home_group,
				color=excluded.color, sort_order=excluded.sort_order`,
			slug, t.Name, t.Category, t.Emoji, homeGroup, t.Color, i,
		); err != nil {
			log.Printf("seedDefaultTags: upserting tag %q: %v", slug, err)
		}
	}
}
