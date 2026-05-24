package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// formHMACKey is generated once at startup; used to sign form timestamps.
var formHMACKey []byte

func init() {
	formHMACKey = make([]byte, 32)
	if _, err := rand.Read(formHMACKey); err != nil {
		log.Fatal("formguard: generate HMAC key: ", err)
	}
}

// newFormToken returns a "ts.mac" token to embed as a hidden field in HTML forms.
// On submission, validFormToken checks that the token is genuine and that the
// form was not submitted faster than the configured minimum time.
func newFormToken() string {
	ts := time.Now().Unix()
	return fmt.Sprintf("%d.%s", ts, formMAC(ts))
}

// validFormToken returns true if token has a valid HMAC and is at least minSecs old.
// When minSecs == 0 the age check is skipped (feature disabled).
func validFormToken(token string, minSecs int) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return minSecs == 0
	}
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false
	}
	if !hmac.Equal([]byte(parts[1]), []byte(formMAC(ts))) {
		return false
	}
	if minSecs == 0 {
		return true
	}
	return time.Now().Unix()-ts >= int64(minSecs)
}

func formMAC(ts int64) string {
	mac := hmac.New(sha256.New, formHMACKey)
	fmt.Fprintf(mac, "%d", ts)
	return hex.EncodeToString(mac.Sum(nil))
}
