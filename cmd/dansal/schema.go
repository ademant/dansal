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
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
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
	if t.Kind() == reflect.Ptr {
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
