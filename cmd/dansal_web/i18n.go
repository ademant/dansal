package main

import (
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	"gopkg.in/yaml.v2"
)

//go:embed i18n.yaml
var defaultI18nYAML []byte

const cookieLang = "dsw_lang"

// I18nStrings is a translation map accessible in templates via .T and .TF methods.
type I18nStrings map[string]string

func (s I18nStrings) T(key string) string {
	if v, ok := s[key]; ok {
		return v
	}
	return key
}

func (s I18nStrings) TF(key string, args ...any) string {
	return fmt.Sprintf(s.T(key), args...)
}

type LangOption struct {
	Code   string
	Flag   string
	Name   string
	Active bool
}

type i18nLangDef struct {
	Flag    string            `yaml:"flag"`
	Name    string            `yaml:"name"`
	Strings map[string]string `yaml:"strings"`
}

type i18nFile struct {
	Default   string                 `yaml:"default"`
	Languages map[string]i18nLangDef `yaml:"languages"`
}

type I18n struct {
	DefaultLang string
	langs       map[string]i18nLangDef
}

const defaultI18nSystemPath = "/etc/dansal/i18n.yaml"

func loadI18n(externalPath string) *I18n {
	var f i18nFile
	if err := yaml.Unmarshal(defaultI18nYAML, &f); err != nil || f.Languages == nil {
		f = i18nFile{Default: "de", Languages: map[string]i18nLangDef{}}
	}
	path := externalPath
	if path == "" {
		path = defaultI18nSystemPath
	}
	if path != "" {
		var ext i18nFile
		if raw, err := os.ReadFile(path); err == nil {
			if yaml.Unmarshal(raw, &ext) == nil && ext.Languages != nil {
				f = ext
			}
		}
	}
	if f.Default == "" {
		f.Default = "de"
	}
	return &I18n{DefaultLang: f.Default, langs: f.Languages}
}

func (i *I18n) HasLang(code string) bool {
	_, ok := i.langs[code]
	return ok
}

func (i *I18n) detectLang(r *http.Request) string {
	if c, err := r.Cookie(cookieLang); err == nil {
		if i.HasLang(c.Value) {
			return c.Value
		}
	}
	return i.DefaultLang
}

func (i *I18n) Strings(lang string) I18nStrings {
	if l, ok := i.langs[lang]; ok {
		return I18nStrings(l.Strings)
	}
	if l, ok := i.langs[i.DefaultLang]; ok {
		return I18nStrings(l.Strings)
	}
	return I18nStrings{}
}

func (i *I18n) T(r *http.Request, key string) string {
	return i.Strings(i.detectLang(r)).T(key)
}

func (i *I18n) Options(activeLang string) []LangOption {
	codes := make([]string, 0, len(i.langs))
	for code := range i.langs {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	opts := make([]LangOption, 0, len(codes))
	for _, code := range codes {
		l := i.langs[code]
		opts = append(opts, LangOption{
			Code:   code,
			Flag:   l.Flag,
			Name:   l.Name,
			Active: code == activeLang,
		})
	}
	return opts
}

func setLangCookie(w http.ResponseWriter, lang string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieLang,
		Value:    lang,
		Path:     "/",
		MaxAge:   int((365 * 24 * time.Hour).Seconds()),
		SameSite: http.SameSiteLaxMode,
		Secure:   true,
	})
}
