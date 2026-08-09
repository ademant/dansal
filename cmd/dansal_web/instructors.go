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

		allEvents, err := client.GetAllPublicEventsByInstructor(r.Context(), id)
		if err != nil {
			logHTTPError(w, r, "could not load instructor events", http.StatusBadGateway)
			return
		}

		upcoming, past := splitUpcomingPast(allEvents, time.Now())

		includePast := r.URL.Query().Get("include_past") == "1"
		displayEvents := upcoming
		if includePast {
			displayEvents = allEvents
		}

		title := instructor.Name
		td := tmplData(r, cfg, i18n, title, InstructorPageData{
			Instructor:  instructor,
			Events:      displayEvents,
			HasPast:     len(past) > 0,
			IncludePast: includePast,
		})
		td.MetaDescription = metaDesc(instructor.Bio, metaDescMaxLen)
		renderTemplate(w, tmpls.instructor, td)
	}
}
