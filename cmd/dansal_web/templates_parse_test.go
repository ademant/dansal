package main

import (
	"html/template"
	"io/fs"
	"strings"
	"testing"
)

// Every page template is parsed lazily at request time from the embedded FS
// (see frontend.go), so a syntax error in any of them — including templates
// only reachable behind admin auth, which no other test renders — would
// otherwise surface as a broken page in production rather than in CI.
func TestAllTemplatesParse(t *testing.T) {
	sub, err := fs.Sub(templateFS, "templates")
	if err != nil {
		t.Fatalf("sub FS: %v", err)
	}

	count := 0
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(p, ".html") {
			return nil
		}
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		if _, err := template.New(p).Funcs(tmplFuncMap).Parse(string(b)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if count == 0 {
		t.Fatal("no templates found — the embedded FS path changed?")
	}
	t.Logf("parsed %d templates", count)
}
