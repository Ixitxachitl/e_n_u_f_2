package config

import (
	"os"
	"strings"
	"testing"

	"twitchbot/internal/database"
)

// TestMain points the database at an isolated temp directory (via
// TWITCHBOT_DATA_DIR) before any test runs, so these tests never touch a
// real ~/.twitchbot install.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "twitchbot-config-test-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	if err := os.Setenv("TWITCHBOT_DATA_DIR", dir); err != nil {
		panic(err)
	}
	if err := database.Init(); err != nil {
		panic(err)
	}

	os.Exit(m.Run())
}

// resetAdminPassword clears any admin password state left over from a
// previous test so each test starts from a clean slate in the shared DB.
func resetAdminPassword(t *testing.T) *Config {
	t.Helper()
	cfg := New()
	if err := cfg.setValue("admin_password_hash", ""); err != nil {
		t.Fatalf("failed to reset admin_password_hash: %v", err)
	}
	if err := cfg.setValue("admin_password_salt", ""); err != nil {
		t.Fatalf("failed to reset admin_password_salt: %v", err)
	}
	return cfg
}

func TestSetAndVerifyAdminPassword(t *testing.T) {
	cfg := resetAdminPassword(t)

	if err := cfg.SetAdminPassword("correct horse battery staple"); err != nil {
		t.Fatalf("SetAdminPassword: %v", err)
	}

	if !cfg.VerifyAdminPassword("correct horse battery staple") {
		t.Error("expected correct password to verify")
	}
	if cfg.VerifyAdminPassword("wrong password") {
		t.Error("expected wrong password to fail verification")
	}

	hash := cfg.getValue("admin_password_hash")
	if !strings.HasPrefix(hash, "$2") {
		t.Errorf("expected a bcrypt hash ($2...), got %q", hash)
	}
}

func TestVerifyAdminPassword_LegacyMigration(t *testing.T) {
	cfg := resetAdminPassword(t)

	// Simulate a pre-bcrypt install: a salted SHA-256 hash stored directly,
	// bypassing SetAdminPassword (which now only ever writes bcrypt hashes).
	salt := "test-salt"
	legacyHash := legacyHashPassword("hunter2", salt)
	if err := cfg.setValue("admin_password_salt", salt); err != nil {
		t.Fatalf("setValue salt: %v", err)
	}
	if err := cfg.setValue("admin_password_hash", legacyHash); err != nil {
		t.Fatalf("setValue hash: %v", err)
	}

	if !cfg.VerifyAdminPassword("hunter2") {
		t.Fatal("expected legacy-hashed password to verify")
	}

	// A successful legacy match should transparently upgrade the stored hash.
	upgraded := cfg.getValue("admin_password_hash")
	if upgraded == legacyHash {
		t.Error("expected hash to be upgraded to bcrypt, but it is still the legacy hash")
	}
	if !strings.HasPrefix(upgraded, "$2") {
		t.Errorf("expected a bcrypt hash after upgrade, got %q", upgraded)
	}
	if salt := cfg.getValue("admin_password_salt"); salt != "" {
		t.Errorf("expected legacy salt to be cleared after upgrade, got %q", salt)
	}

	// The upgraded hash should keep working on subsequent logins.
	if !cfg.VerifyAdminPassword("hunter2") {
		t.Error("expected password to still verify after the bcrypt upgrade")
	}
}

func TestVerifyAdminPassword_WrongLegacyPassword(t *testing.T) {
	cfg := resetAdminPassword(t)

	salt := "test-salt-2"
	legacyHash := legacyHashPassword("hunter2", salt)
	if err := cfg.setValue("admin_password_salt", salt); err != nil {
		t.Fatalf("setValue salt: %v", err)
	}
	if err := cfg.setValue("admin_password_hash", legacyHash); err != nil {
		t.Fatalf("setValue hash: %v", err)
	}

	if cfg.VerifyAdminPassword("wrong guess") {
		t.Error("expected wrong password against a legacy hash to fail")
	}
	// Failed verification must not mutate stored state.
	if got := cfg.getValue("admin_password_hash"); got != legacyHash {
		t.Errorf("hash should be unchanged after a failed attempt, got %q", got)
	}
}

func TestVerifyAdminPassword_NoPasswordSet(t *testing.T) {
	cfg := resetAdminPassword(t)

	if cfg.HasAdminPassword() {
		t.Error("expected HasAdminPassword to be false with no password set")
	}
	if cfg.VerifyAdminPassword("anything") {
		t.Error("expected verification to fail when no password is set")
	}
}
