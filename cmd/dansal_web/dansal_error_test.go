package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestApiErrorMessageJSONBody(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(`{"error":"invalid time \"\"–\"\"; use HH:MM","error_id":"abc123"}`))}
	got := apiErrorMessage(resp)
	want := `invalid time ""–""; use HH:MM`
	if got != want {
		t.Errorf("apiErrorMessage() = %q, want %q", got, want)
	}
}

func TestApiErrorMessageFallsBackToRawBody(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(strings.NewReader("not json"))}
	if got := apiErrorMessage(resp); got != "not json" {
		t.Errorf("apiErrorMessage() = %q, want raw body", got)
	}
}

func TestApiErrorMessageEmptyBody(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(""))}
	if got := apiErrorMessage(resp); got != "" {
		t.Errorf("apiErrorMessage() = %q, want empty", got)
	}
}
