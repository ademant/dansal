package main

import (
	"fmt"
	"net/http"
	"time"
)

// healthHandler serves GET /health — returns 200 OK with basic JSON.
func healthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, `{"status":"ok","version":%q,"time":%q}`+"\n", Version, time.Now().UTC().Format(time.RFC3339))
	}
}

// securityTxtHandler serves /.well-known/security.txt per RFC 9116.
// Only active when SecurityContact is set in web.yaml.
func securityTxtHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.SecurityContact == "" {
			http.NotFound(w, r)
			return
		}
		expires := time.Now().AddDate(1, 0, 0).UTC().Format(time.RFC3339)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fmt.Fprintf(w, "Contact: %s\nExpires: %s\n", cfg.SecurityContact, expires)
		if cfg.SecurityPolicy != "" {
			fmt.Fprintf(w, "Policy: %s\n", cfg.SecurityPolicy)
		}
	}
}

// hostMetaHandler serves /.well-known/host-meta in XML format per RFC 6415.
func hostMetaHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := cfg.publicBaseURL()
		w.Header().Set("Content-Type", `application/xrd+xml`)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<XRD xmlns="http://docs.oasis-open.org/ns/xri/xrd-1.0">
  <Link rel="lrdd" template="%s/.well-known/webfinger?resource={uri}"/>
</XRD>
`, base)
	}
}

// dntPolicyHandler serves /.well-known/dnt-policy.txt per EFF DNT spec.
func dntPolicyHandler() http.HandlerFunc {
	const policy = `DNT Policy for this service

This service respects the Do Not Track (DNT) signal and the
Global Privacy Control (Sec-GPC) signal.

When your browser sends DNT: 1 or Sec-GPC: 1:
- No analytics cookies are set
- No third-party tracking scripts are loaded
- Session data is not retained beyond the session

This service does not use third-party analytics, advertising
networks, or tracking pixels under any circumstances.

Session cookies (authentication, language preference) are
essential for service operation and are always minimal.

Contact the instance administrator for privacy inquiries.
`
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fmt.Fprint(w, policy)
	}
}

// robotsTxtHandler serves /robots.txt.
func robotsTxtHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := cfg.publicBaseURL()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fmt.Fprintf(w, "User-agent: *\nDisallow: /admin/\nDisallow: /api/\n\nContent-Signal: search=yes, ai-train=yes, use=full\n\nSitemap: %s/sitemap.xml\n", base)
	}
}

// dntStatusHandler serves /.well-known/dnt with machine-readable compliance status.
func dntStatusHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := cfg.publicBaseURL()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		// "N" = does not track per W3C Tracking Preference Expression spec.
		fmt.Fprintf(w, `{"tracking":"N","compliance":["https://www.w3.org/TR/tracking-dnt/"],"policy":"%s/.well-known/dnt-policy.txt"}`+"\n", base)
	}
}

// llmsTxtHandler serves /llms.txt — a Markdown summary of the site for LLM
// tools, per the community llms.txt convention.
func llmsTxtHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := cfg.publicBaseURL()
		siteName := cfg.SiteName
		if siteName == "" {
			siteName = "dansal"
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fmt.Fprintf(w, `# %s

> A community calendar for bal-folk, fest-noz, and folk dance/music events,
> locations, and organizations.

## Pages

- [Events and locations](%s/): upcoming events with map and weekly/daily calendar views
- [Site map](%s/sitemap.xml): full list of indexable pages

## Feeds

- [All upcoming events (iCal)](%s/feed/events.ics)
- [All upcoming events (RSS)](%s/feed/events.rss)
`, siteName, base, base, base, base)
	}
}

// manifestHandler serves /manifest.json, a minimal PWA web app manifest.
func manifestHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		siteName := cfg.SiteName
		if siteName == "" {
			siteName = "dansal"
		}
		w.Header().Set("Content-Type", "application/manifest+json")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fmt.Fprintf(w, `{
  "name": %q,
  "short_name": %q,
  "start_url": "/",
  "display": "standalone",
  "theme_color": "#1a6eb5",
  "background_color": "#fafafa",
  "icons": [
    {
      "src": "/favicon.svg",
      "type": "image/svg+xml",
      "sizes": "any",
      "purpose": "any"
    }
  ]
}
`, siteName, siteName)
	}
}

// opensearchHandler serves /opensearch.xml, an OpenSearch description
// document letting browsers register the site's event search as a custom
// address-bar search provider.
func opensearchHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := cfg.publicBaseURL()
		siteName := cfg.SiteName
		if siteName == "" {
			siteName = "dansal"
		}
		w.Header().Set("Content-Type", "application/opensearchdescription+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<OpenSearchDescription xmlns="http://a9.com/-/spec/opensearch/1.1/">
  <ShortName>%s</ShortName>
  <Description>Search %s events by town</Description>
  <InputEncoding>UTF-8</InputEncoding>
  <Url type="text/html" template="%s/?town={searchTerms}"/>
  <Image height="16" width="16" type="image/svg+xml">%s/favicon.svg</Image>
</OpenSearchDescription>
`, siteName, siteName, base, base)
	}
}
