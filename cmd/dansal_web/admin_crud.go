package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"strconv"
)

// adminEntity[E] drives the six standard admin CRUD handlers (list, new page,
// create, edit page, save, delete) from a per-entity definition table, cutting
// the ~90 lines of boilerplate each entity previously duplicated. Entities with
// uniform flows (musicians, instructors) share these handlers wholesale;
// entities with role gates, org membership or uploads that re-render the form
// keep their bespoke handlers.
type adminEntity[E any] struct {
	// Routes.
	listPath string              // e.g. "/admin/musicians"
	editPath func(id int) string // e.g. "/admin/musicians/%d/edit"

	// Templates (accessors so *Templates stays a plain struct).
	listTmpl func(*Templates) *template.Template
	editTmpl func(*Templates) *template.Template

	// i18n key for the list page title.
	listTitleKey string

	// Data builders → the concrete per-entity page structs.
	listData func(items []E) any
	editData func(e E, isNew bool, errKey, from string, imgFlash editFlash) any

	// Client operations.
	listFn   func(ctx context.Context, client *DansalClient) ([]E, error)
	getFn    func(ctx context.Context, client *DansalClient, id int) (E, error)
	createFn func(ctx context.Context, client *DansalClient, e E, token string) (E, error)
	updateFn func(ctx context.Context, client *DansalClient, id int, e E, token string) error
	deleteFn func(ctx context.Context, client *DansalClient, id int, token string) error
	fromForm func(r *http.Request) E

	// Per-entity behaviour. afterCreate/afterSave return an optional FlashMsg
	// (#1285) — used when they upload a file that can fail independently of
	// the entity save that already succeeded (e.g. an oversized avatar);
	// Create/Save attach it to their redirect instead of the bare redirect,
	// so the failure is a small scoped notice rather than a whole-page error.
	afterCreate  func(cfg *Config, client *DansalClient, r *http.Request, created E) FlashMsg
	afterSave    func(cfg *Config, client *DansalClient, r *http.Request, id int) FlashMsg
	needDeadline bool   // extend write deadline (slow AVIF re-encode on photo upload)
	loadErrMsg   string // e.g. "could not load musicians"
	name         string // for delete logs
}

// editFlash carries an optional image-upload notice (#1285) into editData,
// read back from the one-time ?msg= flash on EditPage — separate from errKey,
// which marks the whole save as failed.
type editFlash struct {
	Key    string
	Widget string
}

// List renders the entity's admin index page.
func (e *adminEntity[E]) List(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		items, err := e.listFn(r.Context(), client)
		if err != nil {
			http.Error(w, e.loadErrMsg, http.StatusBadGateway)
			return
		}
		title := i18n.T(r, e.listTitleKey)
		renderTemplate(w, e.listTmpl(tmpls), tmplData(r, cfg, i18n, title, e.listData(items)))
	}
}

// NewPage renders the "create" form.
func (e *adminEntity[E]) NewPage(cfg *Config, tmpls *Templates, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		var zero E
		title := i18n.T(r, "admin_new")
		renderTemplate(w, e.editTmpl(tmpls), tmplData(r, cfg, i18n, title, e.editData(zero, true, "", "", editFlash{})))
	}
}

// Create handles the "create" POST.
func (e *adminEntity[E]) Create(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if err := parseAdminMultipart(r); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		item := e.fromForm(r)
		created, err := e.createFn(r.Context(), client, item, getSessionToken(r))
		if err != nil {
			title := i18n.T(r, "admin_new")
			renderTemplate(w, e.editTmpl(tmpls), tmplData(r, cfg, i18n, title, e.editData(item, true, "admin_save_error", "", editFlash{})))
			return
		}
		var flash FlashMsg
		if e.afterCreate != nil {
			flash = e.afterCreate(cfg, client, r, created)
		}
		if flash.ImageUploadError != "" {
			// #1285: the entity itself saved fine, so this rides along as a
			// scoped notice on the normal redirect rather than a save error.
			flashRedirect(w, r, e.listPath, newErrorID(), flash)
			return
		}
		http.Redirect(w, r, e.listPath, http.StatusSeeOther)
	}
}

// EditPage renders the entity's edit form.
func (e *adminEntity[E]) EditPage(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := parseAdminID(r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		item, err := e.getFn(r.Context(), client, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		title := i18n.T(r, "admin_edit")
		flash := flashTake(r.URL.Query().Get("msg"))
		renderTemplate(w, e.editTmpl(tmpls), tmplData(r, cfg, i18n, title, e.editData(item, false, "", safeReturnPath(r.URL.Query().Get("from")), editFlash{Key: flash.ImageUploadError, Widget: flash.ImageUploadWidget})))
	}
}

// Save handles the entity's edit POST.
func (e *adminEntity[E]) Save(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		if e.needDeadline {
			adminWriteDeadline(w)
		}
		id, err := parseAdminID(r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := parseAdminMultipart(r); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		from := safeReturnPath(r.FormValue("from"))
		item := e.fromForm(r)
		if err := e.updateFn(r.Context(), client, id, item, getSessionToken(r)); err != nil {
			title := i18n.T(r, "admin_edit")
			renderTemplate(w, e.editTmpl(tmpls), tmplData(r, cfg, i18n, title, e.editData(item, false, "admin_save_error", from, editFlash{})))
			return
		}
		var flash FlashMsg
		if e.afterSave != nil {
			flash = e.afterSave(cfg, client, r, id)
		}
		if flash.ImageUploadError != "" {
			// #1285: land back on this entity's own edit page (not the usual
			// listPath/from target) — that's where the failed widget and a
			// retry actually are. The entity itself saved fine either way.
			flashRedirect(w, r, e.editPath(id), newErrorID(), flash)
			return
		}
		target := e.listPath
		if from != "" {
			target = from
		}
		if p := safeReturnPath(target); p != "" {
			target = p
		} else {
			target = e.listPath
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
}

// Delete handles the entity's delete POST.
func (e *adminEntity[E]) Delete(cfg *Config, client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := parseAdminID(r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := e.deleteFn(r.Context(), client, id, getSessionToken(r)); err != nil {
			log.Printf("delete %s %d: %v", e.name, id, err)
		}
		http.Redirect(w, r, e.listPath, http.StatusSeeOther)
	}
}

// adminSubResourceDeleteHandler handles POST deletes of an entity's sub-resource
// (image, avatar, logo…): login → parse id → delete → redirect back to the
// entity's edit page.
func adminSubResourceDeleteHandler(
	client *DansalClient,
	deleteFn func(ctx context.Context, id int, token string) error,
	logFmt string,
	editPath func(id int) string,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := parseAdminID(r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if err := deleteFn(r.Context(), id, getSessionToken(r)); err != nil {
			log.Printf(logFmt, id, err)
		}
		http.Redirect(w, r, editPath(id), http.StatusSeeOther)
	}
}

// parseAdminID reads the {id} path value shared by every admin {id} route.
func parseAdminID(r *http.Request) (int, error) {
	return strconv.Atoi(r.PathValue("id"))
}
