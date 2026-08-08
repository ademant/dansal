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
	editData func(e E, isNew bool, errKey, from string) any

	// Client operations.
	listFn   func(ctx context.Context, client *DansalClient) ([]E, error)
	getFn    func(ctx context.Context, client *DansalClient, id int) (E, error)
	createFn func(ctx context.Context, client *DansalClient, e E, token string) (E, error)
	updateFn func(ctx context.Context, client *DansalClient, id int, e E, token string) error
	deleteFn func(ctx context.Context, client *DansalClient, id int, token string) error
	fromForm func(r *http.Request) E

	// Per-entity behaviour.
	afterCreate  func(cfg *Config, client *DansalClient, r *http.Request, created E)
	afterSave    func(cfg *Config, client *DansalClient, r *http.Request, id int)
	needDeadline bool   // extend write deadline (slow AVIF re-encode on photo upload)
	loadErrMsg   string // e.g. "could not load musicians"
	name         string // for delete logs
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
		renderTemplate(w, e.editTmpl(tmpls), tmplData(r, cfg, i18n, title, e.editData(zero, true, "", "")))
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
			renderTemplate(w, e.editTmpl(tmpls), tmplData(r, cfg, i18n, title, e.editData(item, true, "admin_save_error", "")))
			return
		}
		if e.afterCreate != nil {
			e.afterCreate(cfg, client, r, created)
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
		renderTemplate(w, e.editTmpl(tmpls), tmplData(r, cfg, i18n, title, e.editData(item, false, "", safeReturnPath(r.URL.Query().Get("from")))))
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
			renderTemplate(w, e.editTmpl(tmpls), tmplData(r, cfg, i18n, title, e.editData(item, false, "admin_save_error", from)))
			return
		}
		if e.afterSave != nil {
			e.afterSave(cfg, client, r, id)
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
