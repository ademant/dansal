package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ics "github.com/arran4/golang-ical"
)

// Feed images (#1578): the IMAGE property is tried first, the content type is
// checked before the download, and a non-image is refused.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		img.Set(x, 4, color.RGBA{255, 0, 0, 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func imageTestEnv(t *testing.T) func() {
	t.Helper()
	oldConfig := config
	config = &Config{}
	config.Server.ImagesDir = t.TempDir()
	config.Server.ImageXMax = 1024
	config.Server.ImageYMax = 1024
	config.Server.ImageFormat = "jpeg"
	withPlainSafeClient(t)
	return func() { config = oldConfig }
}

func veventWith(t *testing.T, property string) *ics.VEvent {
	t.Helper()
	cal, err := ics.ParseCalendar(strings.NewReader("BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//dansal//test//EN\nBEGIN:VEVENT\nUID:img-1\nSUMMARY:Image\nDTSTART:20301101T200000Z\n" + property + "\nEND:VEVENT\nEND:VCALENDAR"))
	if err != nil {
		t.Fatal(err)
	}
	return cal.Events()[0]
}

func TestIcalImagePropertyFetchesAnImage(t *testing.T) {
	defer imageTestEnv(t)()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes(t))
	}))
	defer srv.Close()

	const eventID = 9101
	attachImagesFromICalEvent(context.Background(), eventID, veventWith(t, "IMAGE;VALUE=URI:"+srv.URL+"/poster.png"))
	if !hasImage(eventID) {
		t.Error("IMAGE URL to a PNG was not stored")
	}
}

func TestIcalImageRefusesNonImageContentType(t *testing.T) {
	defer imageTestEnv(t)()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>not an image</html>"))
	}))
	defer srv.Close()

	const eventID = 9102
	attachImagesFromICalEvent(context.Background(), eventID, veventWith(t, "IMAGE;VALUE=URI:"+srv.URL+"/page.html"))
	if hasImage(eventID) {
		t.Error("a text/html response was stored as an image")
	}
}
