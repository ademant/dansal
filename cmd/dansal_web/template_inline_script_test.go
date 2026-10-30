package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// #1727: base.js is loaded with defer, so an inline script that calls one of its
// functions while the page is still being parsed throws "is not defined". Two
// regressions had this cause (#1712, and events_suggest.html). This test scans
// the inline scripts of every template and fails on such a call.

var (
	baseJSFuncRe     = regexp.MustCompile(`(?m)^function\s+([A-Za-z_$][\w$]*)\s*\(`)
	baseJSVarFuncRe  = regexp.MustCompile(`(?m)^(?:var|let|const)\s+([A-Za-z_$][\w$]*)\s*=\s*function\b`)
	inlineScriptRe   = regexp.MustCompile(`(?s)<script\b([^>]*)>(.*?)</script>`)
	templateActionRe = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
)

func isJSIdentChar(c byte) bool {
	return c == '_' || c == '$' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// blankExceptNewlines replaces every byte of s but newlines with a space.
func blankExceptNewlines(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c != '\n' {
			b[i] = ' '
		}
	}
	return string(b)
}

// sanitizeJS blanks template actions, comments and string literals, so their
// braces and identifiers do not count. Newlines are kept, so line numbers hold.
func sanitizeJS(src string) string {
	b := []byte(templateActionRe.ReplaceAllStringFunc(src, blankExceptNewlines))
	blank := func(i int) {
		if b[i] != '\n' {
			b[i] = ' '
		}
	}
	for i := 0; i < len(b); i++ {
		switch {
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				blank(i)
				i++
			}
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			blank(i)
			blank(i + 1)
			i += 2
			for i < len(b) && !(b[i] == '*' && i+1 < len(b) && b[i+1] == '/') {
				blank(i)
				i++
			}
			if i+1 < len(b) {
				blank(i)
				blank(i + 1)
				i++
			}
		case b[i] == '\'' || b[i] == '"' || b[i] == '`':
			q := b[i]
			i++
			for i < len(b) && b[i] != q {
				// A ' or " string cannot span lines. Ending at the newline stops a
				// stray quote (in a regex, say) from blanking the rest of the script.
				if b[i] == '\n' && q != '`' {
					break
				}
				if b[i] == '\\' && i+1 < len(b) {
					blank(i)
					i++
				}
				blank(i)
				i++
			}
		}
	}
	return string(b)
}

// Scope kinds for the function a brace belongs to.
const (
	scopeBlock = "block" // a plain { } block
	scopeIIFE  = "iife"  // (function(){ ... })(): runs at once
	scopeArg   = "arg"   // a callback or argument: runs later
	scopeDecl  = "decl"  // a named declaration or assignment: runs when called
)

// skipSpaceBack returns the index of the last non-space byte before i, or -1.
func skipSpaceBack(s string, i int) int {
	for i >= 0 && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i--
	}
	return i
}

// functionScope classifies a function keyword at [start, end) by what precedes it.
func functionScope(s string, start, end int) string {
	rest := end
	for rest < len(s) && (s[rest] == ' ' || s[rest] == '\t' || s[rest] == '\n') {
		rest++
	}
	if rest < len(s) && (isJSIdentChar(s[rest]) && !(s[rest] >= '0' && s[rest] <= '9')) {
		return scopeDecl // function name(...)
	}
	p := skipSpaceBack(s, start-1)
	if p < 0 {
		return scopeIIFE
	}
	switch s[p] {
	case '(':
		q := skipSpaceBack(s, p-1)
		if q >= 0 && (isJSIdentChar(s[q]) || s[q] == ')' || s[q] == ']') {
			return scopeArg // foo(function(){ ... })
		}
		return scopeIIFE // (function(){ ... })()
	case ',', ':', '?':
		return scopeArg
	case '=':
		return scopeDecl // var f = function(){ ... }
	case '!', '+', '-', '~':
		return scopeIIFE // !function(){ ... }()
	}
	// return function(){ ... }, and anything else that hands the function on.
	if q := skipSpaceBack(s, p); q >= 0 && isJSIdentChar(s[q]) {
		j := q
		for j > 0 && isJSIdentChar(s[j-1]) {
			j--
		}
		if s[j:q+1] == "return" {
			return scopeArg
		}
	}
	return scopeIIFE
}

// parseTimeCalls returns the lines of calls to names that run while the page is
// still parsing: the call is not inside a callback, a declared function, or
// an arrow function, so only plain blocks and IIFEs enclose it.
func parseTimeCalls(src string, names map[string]bool, firstLine int) []string {
	s := sanitizeJS(src)
	var stack []string
	pending := ""
	prevWord := ""
	var out []string
	line := firstLine
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\n' {
			line++
			continue
		}
		// prevWord means "the word just before this token": any punctuation breaks it.
		if c != ' ' && c != '\t' && c != '\r' && !isJSIdentChar(c) {
			prevWord = ""
		}
		switch {
		case isJSIdentChar(c) && !(c >= '0' && c <= '9') && (i == 0 || !isJSIdentChar(s[i-1])):
			j := i
			for j < len(s) && isJSIdentChar(s[j]) {
				j++
			}
			word := s[i:j]
			if word == "function" {
				pending = functionScope(s, i, j)
				prevWord = word
				i = j - 1
				continue
			}
			if names[word] && prevWord != "function" {
				k := skipSpaceBack(s, i-1)
				next := j
				for next < len(s) && (s[next] == ' ' || s[next] == '\t') {
					next++
				}
				if (k < 0 || s[k] != '.') && next < len(s) && s[next] == '(' && !scopeInCallback(stack) {
					out = append(out, fmt.Sprintf("line %d: %s() runs while the page is parsing", line, word))
				}
			}
			prevWord = word
			i = j - 1
		case c == '{':
			if pending != "" {
				stack = append(stack, pending)
				pending = ""
			} else {
				stack = append(stack, scopeBlock)
			}
		case c == '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case c == '=' && i+1 < len(s) && s[i+1] == '>':
			pending = scopeArg
			i++
		case c == ';':
			pending = ""
		}
	}
	return out
}

// scopeInCallback reports whether an enclosing function is a callback or a
// declaration, so its body does not run during parsing.
func scopeInCallback(stack []string) bool {
	for _, k := range stack {
		if k == scopeArg || k == scopeDecl {
			return true
		}
	}
	return false
}

// baseJSNames lists the top-level functions defined in static/base.js.
func baseJSNames(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("static", "base.js"))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, re := range []*regexp.Regexp{baseJSFuncRe, baseJSVarFuncRe} {
		for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
			names[m[1]] = true
		}
	}
	if len(names) == 0 {
		t.Fatal("found no functions in static/base.js")
	}
	return names
}

func TestInlineScriptsDoNotCallBaseJSAtParseTime(t *testing.T) {
	names := baseJSNames(t)
	entries, err := os.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}
	var problems []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("templates", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range inlineScriptRe.FindAllStringSubmatchIndex(string(raw), -1) {
			attrs, body := string(raw[m[2]:m[3]]), string(raw[m[4]:m[5]])
			if strings.Contains(attrs, "src=") {
				continue
			}
			if strings.Contains(attrs, "type=") && !strings.Contains(attrs, "javascript") && !strings.Contains(attrs, "module") {
				continue // JSON-LD and other data blocks
			}
			// Lines are reported from the top of the template file.
			firstLine := 1 + strings.Count(string(raw[:m[4]]), "\n")
			for _, p := range parseTimeCalls(body, names, firstLine) {
				problems = append(problems, fmt.Sprintf("templates/%s %s", e.Name(), p))
			}
		}
	}
	if len(problems) > 0 {
		t.Fatalf("inline scripts call static/base.js functions before base.js has run (base.js is deferred). Move each call into a DOMContentLoaded listener:\n  %s",
			strings.Join(problems, "\n  "))
	}
}

func TestParseTimeCallsClassifiesScopes(t *testing.T) {
	names := map[string]bool{"dansalX": true}
	cases := []struct {
		name string
		src  string
		want int // number of parse-time calls expected
	}{
		{"top level", "dansalX(1);", 1},
		{"iife", "(function(){\n  dansalX(1);\n})();", 1},
		{"iife after a statement", "var a = 1;\n(function(){ if (a) { dansalX(1); } })();", 1},
		{"DOMContentLoaded callback", "document.addEventListener('DOMContentLoaded', function(){ dansalX(1); });", 0},
		{"iife inside DOMContentLoaded", "document.addEventListener('DOMContentLoaded', function(){\n(function(){ dansalX(1); })();\n});", 0},
		{"setTimeout callback", "setTimeout(function(){ dansalX(1); }, 0);", 0},
		{"named function body", "function init(){ dansalX(1); }", 0},
		{"arrow callback", "addEventListener('x', () => { dansalX(1); });", 0},
		{"method call is not the global", "obj.dansalX(1);", 0},
		{"definition is not a call", "function dansalX(bar){ return bar; }", 0},
		{"string and comment are ignored", "// dansalX(1)\nvar s = 'dansalX(1)';", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTimeCalls(tc.src, names, 1)
			if len(got) != tc.want {
				t.Errorf("parseTimeCalls = %v, want %d call(s)", got, tc.want)
			}
		})
	}
}
