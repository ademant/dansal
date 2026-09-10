package main

import (
	"context"
	"fmt"
	"html/template"
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

	// ImageUploadError/ImageUploadWidget (#1285): a scoped notice for a
	// failed avatar upload — the instructor itself saved fine either way.
	ImageUploadError  string
	ImageUploadWidget string
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

// instructorEntity wires the generic CRUD scaffold to the Instructor client API.
var instructorEntity = adminEntity[Instructor]{
	listPath:     "/admin/instructors",
	editPath:     instructorEditPath,
	listTmpl:     func(t *Templates) *template.Template { return t.adminInstructors },
	editTmpl:     func(t *Templates) *template.Template { return t.adminInstructorEdit },
	listTitleKey: "admin_instructors_title",
	listData: func(items []Instructor) any {
		return AdminInstructorsData{Instructors: items}
	},
	editData: func(i Instructor, isNew bool, errKey, from string, imgFlash editFlash) any {
		return AdminInstructorEditData{
			Instructor: i, IsNew: isNew, ErrorKey: errKey, From: from,
			ImageUploadError: imgFlash.Key, ImageUploadWidget: imgFlash.Widget,
		}
	},
	listFn: func(ctx context.Context, client *DansalClient) ([]Instructor, error) {
		return client.GetInstructors(ctx)
	},
	getFn: func(ctx context.Context, client *DansalClient, id int) (Instructor, error) {
		return client.GetInstructor(ctx, id)
	},
	createFn: func(ctx context.Context, client *DansalClient, i Instructor, token string) (Instructor, error) {
		return client.CreateInstructor(ctx, i, token)
	},
	updateFn: func(ctx context.Context, client *DansalClient, id int, i Instructor, token string) error {
		return client.UpdateInstructor(ctx, id, i, token)
	},
	deleteFn: func(ctx context.Context, client *DansalClient, id int, token string) error {
		return client.DeleteInstructor(ctx, id, token)
	},
	fromForm: instructorFromForm,
	afterCreate: func(cfg *Config, client *DansalClient, r *http.Request, created Instructor) FlashMsg {
		return uploadInstructorAvatar(cfg, client, r, created.ID)
	},
	afterSave: func(cfg *Config, client *DansalClient, r *http.Request, id int) FlashMsg {
		return uploadInstructorAvatar(cfg, client, r, id)
	},
	loadErrMsg: "could not load instructors",
	name:       "instructor",
}

func instructorEditPath(id int) string {
	return "/admin/instructors/" + strconv.Itoa(id) + "/edit"
}

// uploadInstructorAvatar pushes the avatar from the create/save form and pings
// IndexNow for the instructor page. Runs after the entity is saved so the
// backend has an ID to attach the file to. Returns a FlashMsg (#1285) when
// the upload fails, so Create/Save can surface a scoped notice instead of
// silently discarding it.
func uploadInstructorAvatar(cfg *Config, client *DansalClient, r *http.Request, id int) FlashMsg {
	var flash FlashMsg
	if file, header, ferr := r.FormFile("avatar"); ferr == nil {
		data, _ := io.ReadAll(file)
		file.Close()
		if uerr := client.UploadInstructorAvatar(r.Context(), id, data, header.Filename, getSessionToken(r)); uerr != nil {
			log.Printf("upload instructor avatar error: %v", uerr)
			flash = imageUploadErrorFlash("avatar", uerr)
		}
	}
	go notifyIndexNowPaths(cfg.publicBaseURL(), siteCfg.IndexNowKey(), []string{fmt.Sprintf("/instructors/%d", id)})
	return flash
}

func adminInstructorAvatarDeleteHandler(cfg *Config, client *DansalClient) http.HandlerFunc {
	return adminSubResourceDeleteHandler(client, client.DeleteInstructorAvatar, "delete instructor avatar %d: %v", instructorEditPath)
}
