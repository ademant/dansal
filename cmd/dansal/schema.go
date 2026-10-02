package main

import (
	"net/http"
	"reflect"
	"strings"
)

// FieldSchema describes one field of a request body, as returned by the
// OPTIONS schema responders so clients can discover the expected shape
// without reading Go source. Derived by reflection — see writeSchema.
type FieldSchema struct {
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Items    string   `json:"items,omitempty"`
}

// writeSchema reflects over v (a struct value) and writes a
// {"fields": {...}} JSON description of its JSON-tagged fields. A struct tag
// `enum:"a,b,c"` marks a closed set of allowed values for that field.
func writeSchema(w http.ResponseWriter, v any) {
	fields := map[string]FieldSchema{}
	collectFields(reflect.TypeOf(v), fields)
	writeJSON(w, map[string]any{"fields": fields})
}

func collectFields(t reflect.Type, fields map[string]FieldSchema) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for f := range t.Fields() {
		if f.Anonymous {
			collectFields(f.Type, fields)
			continue
		}
		jsonTag := f.Tag.Get("json")
		if jsonTag == "-" || jsonTag == "" {
			continue
		}
		parts := strings.Split(jsonTag, ",")
		name := parts[0]
		if name == "" {
			continue
		}
		required := true
		for _, p := range parts[1:] {
			if p == "omitempty" {
				required = false
			}
		}
		fs := FieldSchema{Type: jsonKind(f.Type), Required: required}
		if fs.Type == "array" {
			fs.Items = jsonKind(f.Type.Elem())
		}
		if enumTag := f.Tag.Get("enum"); enumTag != "" {
			fs.Enum = strings.Split(enumTag, ",")
		}
		fields[name] = fs
	}
}

func jsonKind(t reflect.Type) string {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Struct, reflect.Map:
		return "object"
	default:
		return "string"
	}
}

// optionsSchema returns a public handler responding to OPTIONS with the JSON
// schema of a zero value of T. Public and read-only: it describes the
// request shape, not any data, so no auth is required.
func optionsSchema[T any](w http.ResponseWriter, r *http.Request) {
	var zero T
	writeSchema(w, zero)
}

// SchemaRoute is one entry of the schema registry (#1383): a write route
// path, the write methods it accepts, and the Go type of its JSON request
// body. registerSchemaRoutes serves an OPTIONS responder for every entry.
// The registry — not per-route registration lines — is the single source of
// "which write routes are schema-discoverable": the coverage test reads it
// (Go's ServeMux can't enumerate its patterns), and an OpenAPI emitter could
// read it later without rework, though none exists (#1383 scope decision).
type SchemaRoute struct {
	Path    string
	Methods []string
	Request reflect.Type
}

// writeSchemaRoutes is the schema registry. A new write route either gets an
// entry here or a documented exemption (writeSchemaExemptions in
// schema_coverage_test.go) — the coverage test fails otherwise.
var writeSchemaRoutes = []SchemaRoute{
	{"/api/v1/events", []string{http.MethodPost}, reflect.TypeFor[EventWriteRequest]()},
	{"/api/v1/events/{id}", []string{http.MethodPut, http.MethodPatch}, reflect.TypeFor[EventWriteRequest]()},
	{"/api/v1/events/{id}/contact-posts", []string{http.MethodPost}, reflect.TypeFor[ContactPostCreateRequest]()},
	{"/api/v1/events/{id}/duplicate-resolve", []string{http.MethodPost}, reflect.TypeFor[DuplicateResolveRequest]()},
	{"/api/v1/events/{id}/location", []string{http.MethodPut}, reflect.TypeFor[EventLocationRefRequest]()},
	{"/api/v1/events/{id}/organization", []string{http.MethodPut}, reflect.TypeFor[EventOrganizationRefRequest]()},
	{"/api/v1/contact-posts/{id}", []string{http.MethodPut, http.MethodPatch}, reflect.TypeFor[ContactPostWriteRequest]()},
	{"/api/v1/locations", []string{http.MethodPost}, reflect.TypeFor[LocationCreateRequest]()},
	{"/api/v1/locations/{id}", []string{http.MethodPut, http.MethodPatch}, reflect.TypeFor[LocationCreateRequest]()},
	{"/api/v1/musicians", []string{http.MethodPost}, reflect.TypeFor[MusicianCreateRequest]()},
	{"/api/v1/musicians/{id}", []string{http.MethodPut, http.MethodPatch}, reflect.TypeFor[MusicianCreateRequest]()},
	{"/api/v1/instructors", []string{http.MethodPost}, reflect.TypeFor[InstructorRequest]()},
	{"/api/v1/instructors/{id}", []string{http.MethodPut, http.MethodPatch}, reflect.TypeFor[InstructorRequest]()},
	{"/api/v1/fetchurl", []string{http.MethodPost}, reflect.TypeFor[FetchURLRequest]()},
	{"/api/v1/fetchurl/{id}", []string{http.MethodPatch}, reflect.TypeFor[FetchSourcePatchRequest]()},
}

// registerSchemaRoutes serves the OPTIONS schema responder for every
// registry entry.
func registerSchemaRoutes(mux *http.ServeMux) {
	for _, sr := range writeSchemaRoutes {
		zero := reflect.Zero(sr.Request).Interface()
		mux.HandleFunc("OPTIONS "+sr.Path, func(w http.ResponseWriter, r *http.Request) {
			writeSchema(w, zero)
		})
	}
}
