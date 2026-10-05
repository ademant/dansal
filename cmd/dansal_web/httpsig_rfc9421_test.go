package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// #1115 / #1432: RFC 9421 verification. The signatures here are produced
// independently of the verifier: the signature base is written out literally
// in the RFC 9421 §2.5 format and signed with crypto/rsa directly.

type rfc9421Fixture struct {
	srv      *httptest.Server
	actor    string
	keyID    string
	privPEM  string
	priv     *rsa.PrivateKey
	bodyJSON []byte
}

func newRFC9421Fixture(t *testing.T, name string) *rfc9421Fixture {
	t.Helper()
	pub, priv, err := generateRSAKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	f := &rfc9421Fixture{privPEM: priv}
	f.priv, _ = parsePrivateKey(priv)
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"publicKey": map[string]string{"owner": f.actor, "publicKeyPem": pub}})
	}))
	t.Cleanup(f.srv.Close)
	f.actor = f.srv.URL + "/ap/users/" + name
	f.keyID = f.actor + "#main-key"
	f.bodyJSON = []byte(`{"type":"Follow","actor":"` + f.actor + `","object":"https://example.test/org/folk"}`)
	return f
}

// signRSA signs base the way the given alg prescribes.
func (f *rfc9421Fixture) signRSA(t *testing.T, alg, base string) string {
	t.Helper()
	var sig []byte
	var err error
	if alg == "rsa-pss-sha512" {
		h := sha512.Sum512([]byte(base))
		sig, err = rsa.SignPSS(rand.Reader, f.priv, crypto.SHA512, h[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	} else {
		h := sha256.Sum256([]byte(base))
		sig, err = rsa.SignPKCS1v15(rand.Reader, f.priv, crypto.SHA256, h[:])
	}
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// mastodonPost builds an inbox POST signed like Mastodon's RFC 9421 sender:
// ("@method" "@target-uri" "content-digest");created=…;keyid="…".
func (f *rfc9421Fixture) mastodonPost(t *testing.T, created time.Time, extraParams string) *http.Request {
	t.Helper()
	digest := sha256.Sum256(f.bodyJSON)
	contentDigest := "sha-256=:" + base64.StdEncoding.EncodeToString(digest[:]) + ":"
	params := fmt.Sprintf(`("@method" "@target-uri" "content-digest");created=%d;keyid="%s"%s`, created.Unix(), f.keyID, extraParams)
	base := "\"@method\": POST\n" +
		"\"@target-uri\": https://example.test/inbox\n" +
		"\"content-digest\": " + contentDigest + "\n" +
		"\"@signature-params\": " + params
	alg := ""
	if strings.Contains(extraParams, "rsa-pss-sha512") {
		alg = "rsa-pss-sha512"
	}
	r := httptest.NewRequest(http.MethodPost, "https://example.test/inbox", strings.NewReader(string(f.bodyJSON)))
	r.Header.Set("Content-Type", "application/activity+json")
	r.Header.Set("Content-Digest", contentDigest)
	r.Header.Set("Signature-Input", "sig1="+params)
	r.Header.Set("Signature", "sig1=:"+f.signRSA(t, alg, base)+":")
	return r
}

func TestVerifyInboxRFC9421MastodonShape(t *testing.T) {
	f := newRFC9421Fixture(t, "alice")
	ctx := context.Background()

	if err := verifyInboxRequest(ctx, f.srv.Client(), f.mastodonPost(t, time.Now(), ""), f.bodyJSON, f.actor); err != nil {
		t.Fatalf("valid RFC 9421 delivery rejected: %v", err)
	}
	if err := verifyInboxRequest(ctx, f.srv.Client(), f.mastodonPost(t, time.Now(), `;alg="rsa-v1_5-sha256"`), f.bodyJSON, f.actor); err != nil {
		t.Fatalf("explicit rsa-v1_5-sha256 rejected: %v", err)
	}
	if err := verifyInboxRequest(ctx, f.srv.Client(), f.mastodonPost(t, time.Now(), `;alg="rsa-pss-sha512"`), f.bodyJSON, f.actor); err != nil {
		t.Fatalf("rsa-pss-sha512 rejected: %v", err)
	}
}

func TestVerifyInboxRFC9421Rejections(t *testing.T) {
	f := newRFC9421Fixture(t, "mallory")
	ctx := context.Background()
	cases := map[string]func() (*http.Request, []byte, string){
		"tampered body": func() (*http.Request, []byte, string) {
			return f.mastodonPost(t, time.Now(), ""), []byte(`{"type":"Delete"}`), f.actor
		},
		"wrong actor": func() (*http.Request, []byte, string) {
			return f.mastodonPost(t, time.Now(), ""), f.bodyJSON, "https://evil.example/users/x"
		},
		"stale created": func() (*http.Request, []byte, string) {
			return f.mastodonPost(t, time.Now().Add(-13*time.Hour), ""), f.bodyJSON, f.actor
		},
		"expired": func() (*http.Request, []byte, string) {
			return f.mastodonPost(t, time.Now(), fmt.Sprintf(";expires=%d", time.Now().Add(-time.Minute).Unix())), f.bodyJSON, f.actor
		},
		"tampered target": func() (*http.Request, []byte, string) {
			r := f.mastodonPost(t, time.Now(), "")
			r.URL.Path = "/org/other/inbox"
			return r, f.bodyJSON, f.actor
		},
		"bad signature bytes": func() (*http.Request, []byte, string) {
			r := f.mastodonPost(t, time.Now(), "")
			r.Header.Set("Signature", "sig1=:"+base64.StdEncoding.EncodeToString([]byte("nope"))+":")
			return r, f.bodyJSON, f.actor
		},
		"unknown alg": func() (*http.Request, []byte, string) {
			return f.mastodonPost(t, time.Now(), `;alg="hmac-sha256"`), f.bodyJSON, f.actor
		},
		"digest not covered": func() (*http.Request, []byte, string) {
			params := fmt.Sprintf(`("@method" "@target-uri");created=%d;keyid="%s"`, time.Now().Unix(), f.keyID)
			base := "\"@method\": POST\n\"@target-uri\": https://example.test/inbox\n\"@signature-params\": " + params
			r := httptest.NewRequest(http.MethodPost, "https://example.test/inbox", strings.NewReader(string(f.bodyJSON)))
			r.Header.Set("Signature-Input", "sig1="+params)
			r.Header.Set("Signature", "sig1=:"+f.signRSA(t, "", base)+":")
			return r, f.bodyJSON, f.actor
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			r, body, actor := mk()
			err := verifyInboxRequest(ctx, f.srv.Client(), r, body, actor)
			if err == nil {
				t.Fatal("expected rejection")
			}
			// Failures name the scheme, alg and covered components for the log.
			if !strings.Contains(err.Error(), "rfc9421 (label=sig1") {
				t.Errorf("error lacks signature details: %v", err)
			}
		})
	}
}

// Authorized fetch (GET) signed with RFC 9421.
func TestVerifyGETRFC9421(t *testing.T) {
	f := newRFC9421Fixture(t, "bob")
	params := fmt.Sprintf(`("@method" "@authority" "@path");created=%d;keyid="%s"`, time.Now().Unix(), f.keyID)
	base := "\"@method\": GET\n\"@authority\": example.test\n\"@path\": /org/folk\n\"@signature-params\": " + params
	r := httptest.NewRequest(http.MethodGet, "https://example.test/org/folk", nil)
	r.Header.Set("Signature-Input", "sig1="+params)
	r.Header.Set("Signature", "sig1=:"+f.signRSA(t, "", base)+":")
	if err := verifyGETRequest(context.Background(), f.srv.Client(), r); err != nil {
		t.Fatalf("valid RFC 9421 GET rejected: %v", err)
	}
}

func TestParseSFDictionary(t *testing.T) {
	in := `sig1=("@method" "@target-uri" "@query-param";name="a b");created=1618884473;keyid="test-key\"x";nonce=abc, sig2=();alg="ed25519"`
	m, err := parseSFDictionary(in)
	if err != nil || len(m) != 2 {
		t.Fatalf("parse: %v %d", err, len(m))
	}
	if m[0].raw != `("@method" "@target-uri" "@query-param";name="a b");created=1618884473;keyid="test-key\"x";nonce=abc` {
		t.Errorf("raw = %s", m[0].raw)
	}
	if len(m[0].items) != 3 || m[0].items[2].raw != `"@query-param";name="a b"` || m[0].items[2].params["name"] != "a b" {
		t.Errorf("items = %+v", m[0].items)
	}
	if m[0].params["keyid"] != `test-key"x` || m[0].params["created"] != "1618884473" || m[0].params["nonce"] != "abc" {
		t.Errorf("params = %v", m[0].params)
	}
	if m[1].raw != `();alg="ed25519"` || len(m[1].items) != 0 {
		t.Errorf("second member = %+v", m[1])
	}
	d, err := parseSFDictionary(`sha-256=:` + base64.StdEncoding.EncodeToString([]byte("hello")) + `:, md5=:AAAA:`)
	if err != nil || string(d[0].bytes) != "hello" || d[1].key != "md5" {
		t.Fatalf("byte sequences: %v %+v", err, d)
	}
	for _, bad := range []string{`sig1=("@method"`, `sig1`, `Sig1=()`, `sig1=:abc`} {
		if _, err := parseSFDictionary(bad); err == nil {
			t.Errorf("parse(%q) should fail", bad)
		}
	}
}
