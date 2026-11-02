package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"unicode"
	"unicode/utf8"
)

// Password rules (#1739). One validator serves every path that sets a password:
// registration, change password, invite acceptance, and the admin CLI.

// passwordRuleError names the rule a password broke. Code is stable and is
// sent to clients as error_code.
type passwordRuleError struct {
	Code string
	Msg  string
}

func (e *passwordRuleError) Error() string { return e.Msg }

// activePasswordPolicy returns the configured rules, or the defaults when no
// config is loaded.
func activePasswordPolicy() PasswordConfig {
	if config == nil {
		return defaultPasswordConfig()
	}
	return config.Password
}

// passwordClasses counts how many of lower case, upper case, digit and symbol
// the password contains. Symbol means any other non-space character.
func passwordClasses(pw string) int {
	var lower, upper, digit, symbol bool
	for _, r := range pw {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		case !unicode.IsSpace(r):
			symbol = true
		}
	}
	n := 0
	for _, ok := range []bool{lower, upper, digit, symbol} {
		if ok {
			n++
		}
	}
	return n
}

// validatePassword applies the configured rules. Length counts characters,
// not bytes, so "äöüß" is four characters. The breach check runs last and
// fails open on a network error (see isPasswordPwned).
func validatePassword(ctx context.Context, pw string) error {
	p := activePasswordPolicy()
	n := utf8.RuneCountInString(pw)
	switch {
	case n < p.MinLength:
		return &passwordRuleError{"too_short", fmt.Sprintf("Password must be at least %d characters", p.MinLength)}
	case p.MaxLength > 0 && n > p.MaxLength:
		return &passwordRuleError{"too_long", fmt.Sprintf("Password must be at most %d characters", p.MaxLength)}
	case passwordClasses(pw) < p.RequireClasses:
		return &passwordRuleError{"classes", fmt.Sprintf("Password must use at least %d of: lowercase, uppercase, digit, symbol", p.RequireClasses)}
	}
	if p.CheckBreached && isPasswordPwned(ctx, pw) {
		return &passwordRuleError{"breached", "This password has appeared in a data breach. Please choose a different password."}
	}
	return nil
}

// writePasswordError sends a password rule failure with its error_code. Other
// errors are not expected here; they are sent as a generic 500.
func writePasswordError(w http.ResponseWriter, err error, status int) {
	var pe *passwordRuleError
	if errors.As(err, &pe) {
		writeErrorCode(w, pe.Code, pe.Msg, status)
		return
	}
	writeInternalError(w, err)
}
