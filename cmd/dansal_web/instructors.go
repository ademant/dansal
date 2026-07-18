package main

import (
	"net/http"
	"strconv"
	"time"
)

type InstructorsPageData struct {
	Instructors []Instructor
}

type InstructorPageData struct {
	Instructor  Instructor
	Events      []Event
	HasPast     bool
	IncludePast bool
}

func instructorsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		instructors, err := client.GetInstructors(r.Context())
		if err != nil {
			http.Error(w, "could not load instructors", http.StatusBadGateway)
			return
		}
		title := i18n.T(r, "instructors_title")
		renderTemplate(w, tmpls.instructors, tmplData(r, cfg, i18n, title, InstructorsPageData{Instructors: instructors}))
	}
}

func instructorHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		instructor, err := client.GetInstructor(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		allEvents, _ := client.GetAllPublicEventsByInstructor(r.Context(), id)

		now := time.Now()
		var futureEvents []Event
		hasPast := false
		for _, e := range allEvents {
			t, err := time.Parse(time.RFC3339, e.EndTime)
			if err != nil || t.After(now) {
				futureEvents = append(futureEvents, e)
			} else {
				hasPast = true
			}
		}

		includePast := r.URL.Query().Get("include_past") == "1"
		displayEvents := futureEvents
		if includePast {
			displayEvents = allEvents
		}

		title := instructor.Name
		td := tmplData(r, cfg, i18n, title, InstructorPageData{
			Instructor:  instructor,
			Events:      displayEvents,
			HasPast:     hasPast,
			IncludePast: includePast,
		})
		td.MetaDescription = metaDesc(instructor.Bio, 155)
		renderTemplate(w, tmpls.instructor, td)
	}
}
