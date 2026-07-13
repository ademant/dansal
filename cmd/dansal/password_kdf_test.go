package main

import "testing"

func setPasswordKDF(t *testing.T, kdf string) {
	t.Helper()
	prevConfig := config
	config = &Config{}
	config.Server.PasswordKDF = kdf
	t.Cleanup(func() { config = prevConfig })
}

func TestHashPasswordArgon2idRoundTrip(t *testing.T) {
	setPasswordKDF(t, "argon2id")

	stored := hashPassword("s3cret!")
	ok, migrate := checkPassword("s3cret!", stored)
	if !ok {
		t.Fatal("expected password to verify")
	}
	if migrate {
		t.Error("should not need migration when hash already matches configured KDF")
	}
	if ok2, _ := checkPassword("wrong", stored); ok2 {
		t.Error("wrong password should not verify")
	}
}

func TestHashPasswordPBKDF2RoundTrip(t *testing.T) {
	setPasswordKDF(t, "pbkdf2")

	stored := hashPassword("s3cret!")
	ok, migrate := checkPassword("s3cret!", stored)
	if !ok {
		t.Fatal("expected password to verify")
	}
	if migrate {
		t.Error("should not need migration when hash already matches configured KDF")
	}
	if ok2, _ := checkPassword("wrong", stored); ok2 {
		t.Error("wrong password should not verify")
	}
}

func TestCheckPasswordMigratesAcrossKDFs(t *testing.T) {
	setPasswordKDF(t, "pbkdf2")
	stored := hashPassword("s3cret!")

	// Admin flips the configured KDF to argon2id; the pbkdf2 hash should
	// still verify, but now be flagged for migration.
	config.Server.PasswordKDF = "argon2id"
	ok, migrate := checkPassword("s3cret!", stored)
	if !ok {
		t.Fatal("expected pbkdf2 hash to still verify after config change")
	}
	if !migrate {
		t.Error("expected migrate=true when stored hash's KDF no longer matches config")
	}
}

func TestCheckPasswordMigratesLegacyFormats(t *testing.T) {
	setPasswordKDF(t, "argon2id")

	// Legacy raw SHA-256 hex digest (pre-bcrypt format).
	legacySHA256 := "fed1f6d8730f52eb51db1c2f7d6a58efad19e7dcae7dcb60d5a1f4f28a6dc07"
	if ok, _ := checkPassword("anything", legacySHA256); ok {
		t.Fatal("sanity check: this fixture shouldn't accidentally match")
	}

	// bcrypt hash should still verify and be flagged for migration.
	bcryptHash := "$2a$10$m7X3mo0Xh95PfPNBoiOfsOaw/d9xI8qosQNjKe2tSJ70ArsX9O9yi" // "password"
	ok, migrate := checkPassword("password", bcryptHash)
	if !ok {
		t.Fatal("expected legacy bcrypt hash to verify")
	}
	if !migrate {
		t.Error("expected migrate=true for legacy bcrypt hash")
	}
}

func TestPasswordKDFDefaultsToArgon2id(t *testing.T) {
	setPasswordKDF(t, "")
	if got := passwordKDF(); got != "argon2id" {
		t.Errorf("passwordKDF() = %q, want argon2id", got)
	}
	setPasswordKDF(t, "bogus")
	if got := passwordKDF(); got != "argon2id" {
		t.Errorf("passwordKDF() with unrecognized value = %q, want argon2id fallback", got)
	}
}
