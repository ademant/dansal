package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

type AdminInstructorsData struct {
	Instructors []Instructor
}

type AdminInstructorEditData struct {
	Instructor Instructor
	IsNew      bool
	ErrorKey   string
	From       string
}

func instructorFromForm(r *http.Request) Instructor {
	return Instructor{
		Name:      strings.TrimSpace(r.FormValue("name")),
		Bio:       strings.TrimSpace(r.FormValue("bio")),
		Website:   strings.TrimSpace(r.FormValue("website")),
		Email:     strings.TrimSpace(r.FormValue("email")),
		Mastodon:  strings.TrimSpace(r.FormValue("mastodon")),
		Instagram: strings.TrimSpace(r.FormValue("instagram")),
		Facebook:  strings.TrimSpace(r.FormValue("facebook")),
	}
}

func adminInstructorsHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		instructors, err := client.GetInstructors(r.Context())
		if err != nil {
			http.Error(w, "could not load instructors", http.StatusBadGateway)
			return
		}
		title := i18n.T(r, "admin_instructors_title")
		renderTemplate(w, tmpls.adminInstructors, tmplData(r, cfg, i18n, title, AdminInstructorsData{Instructors: instructors}))
	}
}

func adminInstructorNewPageHandler(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		title := i18n.T(r, "admin_new")
		renderTemplate(w, tmpls.adminInstructorEdit, tmplData(r, cfg, i18n, title, AdminInstructorEditData{IsNew: true}))
	}
}

func adminInstructorCreateHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
		}
		inst := instructorFromForm(r)
		created, err := client.CreateInstructor(r.Context(), inst, getSessionToken(r))
		if err != nil {
			title := i18n.T(r, "admin_new")
			renderTemplate(w, tmpls.adminInstructorEdit, tmplData(r, cfg, i18n, title, AdminInstructorEditData{
				Instructor: inst, IsNew: true, ErrorKey: "admin_save_error",
			}))
			return
		}
		if file, header, ferr := r.FormFile("avatar"); ferr == nil {
			data, _ := io.ReadAll(file)
			file.Close()
			if uerr := client.UploadInstructorAvatar(r.Context(), created.ID, data, header.Filename, getSessionToken(r)); uerr != nil {
				log.Printf("upload instructor avatar error: %v", uerr)
			}
		}
		go notifyIndexNowPaths(cfg.publicBaseURL(), siteCfg.IndexNowKey(), []string{fmt.Sprintf("/instructors/%d", created.ID)})
		http.Redirect(w, r, "/admin/instructors", http.StatusSeeOther)
	}
}

func adminInstructorEditPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		inst, err := client.GetInstructor(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		title := i18n.T(r, "admin_edit")
		renderTemplate(w, tmpls.adminInstructorEdit, tmplData(r, cfg, i18n, title, AdminInstructorEditData{
			Instructor: inst,
			From:       safeReturnPath(r.URL.Query().Get("from")),
		}))
	}
}

func adminInstructorSaveHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
		}
		from := safeReturnPath(r.FormValue("from"))
		inst := instructorFromForm(r)
		if err := client.UpdateInstructor(r.Context(), id, inst, getSessionToken(r)); err != nil {
			title := i18n.T(r, "admin_edit")
			renderTemplate(w, tmpls.adminInstructorEdit, tmplData(r, cfg, i18n, title, AdminInstructorEditData{
				Instructor: inst, ErrorKey: "admin_save_error", From: from,
			}))
			return
		}
		if file, header, ferr := r.FormFile("avatar"); ferr == nil {
			data, _ := io.ReadAll(file)
			file.Close()
			if uerr := client.UploadInstructorAvatar(r.Context(), id, data, header.Filename, getSessionToken(r)); uerr != nil {
				log.Printf("upload instructor avatar error: %v", uerr)
			}
		}
		go notifyIndexNowPaths(cfg.publicBaseURL(), siteCfg.IndexNowKey(), []string{fmt.Sprintf("/instructors/%d", id)})
		target := "/admin/instructors"
		if from != "" {
			target = from
		}
		if p := safeReturnPath(target); p != "" {
			target = p
		} else {
			target = "/admin/instructors"
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
}

func adminInstructorAvatarDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_ = client.DeleteInstructorAvatar(r.Context(), id, getSessionToken(r))
		http.Redirect(w, r, fmt.Sprintf("/admin/instructors/%d/edit", id), http.StatusSeeOther)
	}
}

func adminInstructorDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_ = client.DeleteInstructor(r.Context(), id, getSessionToken(r))
		http.Redirect(w, r, "/admin/instructors", http.StatusSeeOther)
	}
}
