package main

import (
	"encoding/xml"
	"net/http"
	"time"
)

type apiFeed struct {
	XMLName xml.Name       `xml:"feed"`
	XMLNS   string         `xml:"xmlns,attr"`
	Title   string         `xml:"title"`
	ID      string         `xml:"id"`
	Updated string         `xml:"updated"`
	Entries []apiFeedEntry `xml:"entry"`
}

type apiFeedEntry struct {
	Title   string        `xml:"title"`
	ID      string        `xml:"id"`
	Updated string        `xml:"updated"`
	Summary string        `xml:"summary,omitempty"`
	Links   []apiFeedLink `xml:"link"`
}

type apiFeedLink struct {
	Rel  string `xml:"rel,attr,omitempty"`
	Href string `xml:"href,attr"`
}

func writeAtom(w http.ResponseWriter, feed apiFeed) {
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(feed)
}

func atomTime(epoch int64) string {
	if epoch == 0 {
		return time.Now().UTC().Format(time.RFC3339)
	}
	return time.Unix(epoch, 0).UTC().Format(time.RFC3339)
}

func atomTimeStr(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return time.Now().UTC().Format(time.RFC3339)
}
