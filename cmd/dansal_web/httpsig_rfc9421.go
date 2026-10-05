package main

// RFC 9421 HTTP Message Signatures (#1115, #1432), next to the draft-cavage
// scheme in httpsig.go. Newer Mastodon versions send RFC 9421 only (a
// Signature header without keyId, the key id moved to Signature-Input), so
// those deliveries were rejected as "missing keyId". Dispatch happens inside
// verifyInboxRequest / verifyGETRequest on the presence of Signature-Input
// (CLAUDE.md, httpsig rules 1–7); key fetching and caches are shared.

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// sfMember is one member of an RFC 8941 dictionary as used by the signature
// headers: either an inner list (Signature-Input) or a byte sequence
// (Signature, Content-Digest), plus its parameters. raw is the member value
// exactly as it appeared — for Signature-Input that is the @signature-params
// value of the signature base.
type sfMember struct {
	key    string
	raw    string
	items  []sfItem // inner list items
	bytes  []byte   // byte-sequence value
	params map[string]string
}

// sfItem is one component identifier of an inner list: its serialized form
// (e.g. `"@method"`, `"@query-param";name="a"`) and its bare name.
type sfItem struct {
	raw    string
	name   string
	params map[string]string
}

type sfParser struct {
	s string
	i int
}

func (p *sfParser) eof() bool  { return p.i >= len(p.s) }
func (p *sfParser) peek() byte { return p.s[p.i] }
func (p *sfParser) skipSP() {
	for !p.eof() && p.s[p.i] == ' ' {
		p.i++
	}
}
func (p *sfParser) skipOWS() {
	for !p.eof() && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

func isKeyChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == '*'
}

func (p *sfParser) key() (string, error) {
	start := p.i
	if p.eof() || !(p.s[p.i] >= 'a' && p.s[p.i] <= 'z' || p.s[p.i] == '*') {
		return "", fmt.Errorf("structured field: expected key at %d", p.i)
	}
	for !p.eof() && isKeyChar(p.s[p.i]) {
		p.i++
	}
	return p.s[start:p.i], nil
}

func (p *sfParser) str() (string, error) {
	if p.eof() || p.peek() != '"' {
		return "", fmt.Errorf("structured field: expected string at %d", p.i)
	}
	p.i++
	var b strings.Builder
	for !p.eof() {
		c := p.s[p.i]
		p.i++
		switch c {
		case '\\':
			if p.eof() {
				return "", fmt.Errorf("structured field: bad escape")
			}
			b.WriteByte(p.s[p.i])
			p.i++
		case '"':
			return b.String(), nil
		default:
			b.WriteByte(c)
		}
	}
	return "", fmt.Errorf("structured field: unterminated string")
}

// bareItem reads a string, integer/decimal, token, boolean or byte sequence
// and returns it as text (byte sequences still base64-encoded).
func (p *sfParser) bareItem() (string, error) {
	if p.eof() {
		return "", fmt.Errorf("structured field: missing value")
	}
	switch c := p.peek(); {
	case c == '"':
		return p.str()
	case c == ':':
		p.i++
		end := strings.IndexByte(p.s[p.i:], ':')
		if end < 0 {
			return "", fmt.Errorf("structured field: unterminated byte sequence")
		}
		v := p.s[p.i : p.i+end]
		p.i += end + 1
		return v, nil
	case c == '?':
		if p.i+1 >= len(p.s) {
			return "", fmt.Errorf("structured field: bad boolean")
		}
		v := p.s[p.i+1 : p.i+2]
		p.i += 2
		return v, nil
	default:
		start := p.i
		for !p.eof() && !strings.ContainsRune(" ;,()\t", rune(p.s[p.i])) {
			p.i++
		}
		if start == p.i {
			return "", fmt.Errorf("structured field: bad item at %d", p.i)
		}
		return p.s[start:p.i], nil
	}
}

func (p *sfParser) params() (map[string]string, error) {
	m := map[string]string{}
	for !p.eof() && p.peek() == ';' {
		p.i++
		p.skipSP()
		k, err := p.key()
		if err != nil {
			return nil, err
		}
		v := "1" // bare parameter = boolean true
		if !p.eof() && p.peek() == '=' {
			p.i++
			if v, err = p.bareItem(); err != nil {
				return nil, err
			}
		}
		m[k] = v
	}
	return m, nil
}

// parseSFDictionary parses the dictionaries of Signature-Input, Signature
// and Content-Digest.
func parseSFDictionary(s string) ([]sfMember, error) {
	p := &sfParser{s: s}
	var out []sfMember
	p.skipSP()
	for !p.eof() {
		k, err := p.key()
		if err != nil {
			return nil, err
		}
		if p.eof() || p.peek() != '=' {
			return nil, fmt.Errorf("structured field: %q has no value", k)
		}
		p.i++
		m := sfMember{key: k}
		start := p.i
		if p.peek() == '(' {
			p.i++
			for {
				p.skipSP()
				if p.eof() {
					return nil, fmt.Errorf("structured field: unterminated inner list")
				}
				if p.peek() == ')' {
					p.i++
					break
				}
				itemStart := p.i
				name, err := p.str()
				if err != nil {
					return nil, err
				}
				ip, err := p.params()
				if err != nil {
					return nil, err
				}
				m.items = append(m.items, sfItem{raw: p.s[itemStart:p.i], name: name, params: ip})
			}
		} else {
			v, err := p.bareItem()
			if err != nil {
				return nil, err
			}
			if strings.HasPrefix(s[start:], ":") {
				if m.bytes, err = base64.StdEncoding.DecodeString(v); err != nil {
					return nil, fmt.Errorf("structured field: %q: %w", k, err)
				}
			}
		}
		if m.params, err = p.params(); err != nil {
			return nil, err
		}
		m.raw = p.s[start:p.i]
		out = append(out, m)
		p.skipOWS()
		if p.eof() {
			break
		}
		if p.peek() != ',' {
			return nil, fmt.Errorf("structured field: expected ',' at %d", p.i)
		}
		p.i++
		p.skipOWS()
	}
	return out, nil
}

// rfc9421Signature is the first signature of a request whose label appears
// in both Signature-Input and Signature.
type rfc9421Signature struct {
	label  string
	input  sfMember
	sig    []byte
	keyID  string
	alg    string
	covers map[string]bool
}

func (s *rfc9421Signature) describe() string {
	var names []string
	for _, it := range s.input.items {
		names = append(names, it.name)
	}
	return fmt.Sprintf("label=%s alg=%q components=%v", s.label, s.alg, names)
}

func parseRFC9421Signature(r *http.Request) (*rfc9421Signature, error) {
	inputs, err := parseSFDictionary(r.Header.Get("Signature-Input"))
	if err != nil {
		return nil, fmt.Errorf("Signature-Input: %w", err)
	}
	sigs, err := parseSFDictionary(r.Header.Get("Signature"))
	if err != nil {
		return nil, fmt.Errorf("Signature: %w", err)
	}
	for _, in := range inputs {
		for _, sg := range sigs {
			if sg.key != in.key || len(sg.bytes) == 0 {
				continue
			}
			s := &rfc9421Signature{label: in.key, input: in, sig: sg.bytes,
				keyID: in.params["keyid"], alg: in.params["alg"], covers: map[string]bool{}}
			for _, it := range in.items {
				s.covers[it.name] = true
			}
			return s, nil
		}
	}
	return nil, fmt.Errorf("no signature label present in both Signature-Input and Signature")
}

// componentValue derives one covered component's value (RFC 9421 §2).
func componentValue(r *http.Request, it sfItem) (string, error) {
	switch it.name {
	case "@method":
		return r.Method, nil
	case "@target-uri":
		// TLS ends at the reverse proxy; the public URL is https.
		return "https://" + strings.ToLower(r.Host) + r.URL.RequestURI(), nil
	case "@authority":
		return strings.ToLower(r.Host), nil
	case "@scheme":
		return "https", nil
	case "@request-target":
		return r.URL.RequestURI(), nil
	case "@path":
		if p := r.URL.EscapedPath(); p != "" {
			return p, nil
		}
		return "/", nil
	case "@query":
		return "?" + r.URL.RawQuery, nil
	}
	if strings.HasPrefix(it.name, "@") || len(it.params) > 0 {
		return "", fmt.Errorf("unsupported component %s", it.raw)
	}
	var vals []string
	if it.name == "host" {
		vals = []string{r.Host}
	} else {
		vals = r.Header.Values(http.CanonicalHeaderKey(it.name))
	}
	if len(vals) == 0 {
		return "", fmt.Errorf("covered header %q missing", it.name)
	}
	for i, v := range vals {
		vals[i] = strings.TrimSpace(v)
	}
	return strings.Join(vals, ", "), nil
}

// rfc9421SignatureBase builds the signature base (RFC 9421 §2.5).
func rfc9421SignatureBase(r *http.Request, input sfMember) (string, error) {
	var b strings.Builder
	seen := map[string]bool{}
	for _, it := range input.items {
		if seen[it.raw] {
			return "", fmt.Errorf("component %s covered twice", it.raw)
		}
		seen[it.raw] = true
		v, err := componentValue(r, it)
		if err != nil {
			return "", err
		}
		if strings.ContainsAny(v, "\r\n") {
			return "", fmt.Errorf("component %s contains a newline", it.raw)
		}
		b.WriteString(it.raw + ": " + v + "\n")
	}
	b.WriteString(`"@signature-params": ` + input.raw)
	return b.String(), nil
}

// VerifyRequestRFC9421 checks an RFC 9421 signature over r. sigInput is the
// Signature-Input member value, sigBytes the decoded signature. RSA keys
// only (rsa-v1_5-sha256, which is what Mastodon uses, or rsa-pss-sha512);
// an absent alg means rsa-v1_5-sha256 for an RSA key.
func VerifyRequestRFC9421(r *http.Request, pubKeyPEM, sigInput, sigBytes string) error {
	members, err := parseSFDictionary("sig=" + sigInput)
	if err != nil || len(members) != 1 {
		return fmt.Errorf("Signature-Input: %v", err)
	}
	base, err := rfc9421SignatureBase(r, members[0])
	if err != nil {
		return err
	}
	pub, err := parsePublicKey(pubKeyPEM)
	if err != nil {
		return fmt.Errorf("parse public key: %w", err)
	}
	switch alg := members[0].params["alg"]; alg {
	case "", "rsa-v1_5-sha256":
		h := sha256.Sum256([]byte(base))
		return rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], []byte(sigBytes))
	case "rsa-pss-sha512":
		h := sha512.Sum512([]byte(base))
		return rsa.VerifyPSS(pub, crypto.SHA512, h[:], []byte(sigBytes), &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	default:
		return fmt.Errorf("unsupported alg %q", alg)
	}
}

// checkContentDigest verifies the body against Content-Digest (RFC 9530):
// every sha-256 / sha-512 entry must match and at least one must be present.
func checkContentDigest(r *http.Request, body []byte) error {
	hdr := r.Header.Get("Content-Digest")
	if hdr == "" {
		return fmt.Errorf("missing Content-Digest header")
	}
	members, err := parseSFDictionary(hdr)
	if err != nil {
		return fmt.Errorf("Content-Digest: %w", err)
	}
	checked := false
	for _, m := range members {
		var sum []byte
		switch m.key {
		case "sha-256":
			s := sha256.Sum256(body)
			sum = s[:]
		case "sha-512":
			s := sha512.Sum512(body)
			sum = s[:]
		default:
			continue
		}
		if subtle.ConstantTimeCompare(sum, m.bytes) != 1 {
			return fmt.Errorf("Content-Digest %s mismatch", m.key)
		}
		checked = true
	}
	if !checked {
		return fmt.Errorf("Content-Digest has no sha-256 or sha-512 entry")
	}
	return nil
}

// verifyRFC9421 is the RFC 9421 branch of verifyInboxRequest (body != nil)
// and verifyGETRequest (body == nil): required coverage, Content-Digest,
// created/expires window, key fetch, key-owner ↔ actor match, signature.
func verifyRFC9421(ctx context.Context, httpClient *http.Client, r *http.Request, body []byte, actorField string) error {
	s, err := parseRFC9421Signature(r)
	if err != nil {
		return err
	}
	fail := func(err error) error { return fmt.Errorf("rfc9421 (%s): %w", s.describe(), err) }
	if s.keyID == "" {
		return fail(fmt.Errorf("missing keyid parameter"))
	}

	// Required coverage: the method, the target, and for a POST the body.
	if !s.covers["@method"] {
		return fail(fmt.Errorf("@method not covered"))
	}
	if !s.covers["@target-uri"] && !(s.covers["@authority"] && (s.covers["@path"] || s.covers["@request-target"])) {
		return fail(fmt.Errorf("request target not covered"))
	}
	if body != nil {
		if !s.covers["content-digest"] {
			return fail(fmt.Errorf("content-digest not covered"))
		}
		if err := checkContentDigest(r, body); err != nil {
			return fail(err)
		}
	}

	// Freshness from the signature's own created/expires parameters.
	created, err := strconv.ParseInt(s.input.params["created"], 10, 64)
	if err != nil {
		return fail(fmt.Errorf("missing or invalid created parameter"))
	}
	if age := time.Since(time.Unix(created, 0)); age < -inboxDateTolerance || age > inboxDateTolerance {
		return fail(fmt.Errorf("created outside ±12 h window: %v", time.Unix(created, 0)))
	}
	if exp := s.input.params["expires"]; exp != "" {
		if e, err := strconv.ParseInt(exp, 10, 64); err != nil || time.Now().Unix() > e {
			return fail(fmt.Errorf("signature expired"))
		}
	}

	pubKeyPEM, owner, err := fetchActorPublicKey(ctx, httpClient, s.keyID)
	if err != nil {
		return fail(fmt.Errorf("fetch public key %q: %w", s.keyID, err))
	}
	if actorField != "" {
		effectiveOwner := owner
		if effectiveOwner == "" {
			effectiveOwner, _, _ = strings.Cut(s.keyID, "#")
		}
		if effectiveOwner != actorField {
			return fail(fmt.Errorf("key owner %q does not match activity actor %q", effectiveOwner, actorField))
		}
	}
	if err := VerifyRequestRFC9421(r, pubKeyPEM, s.input.raw, string(s.sig)); err != nil {
		return fail(err)
	}
	return nil
}
