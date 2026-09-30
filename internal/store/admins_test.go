package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPasswordHashIsSaltedAndVerifies(t *testing.T) {
	h1, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	h2, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if h1 == h2 {
		t.Fatal("two hashes of the same password are identical — salt is missing")
	}
	if strings.Contains(h1, "correct horse") {
		t.Fatal("hash leaks the password")
	}
	if !VerifyPassword("correct horse battery", h1) {
		t.Error("correct password rejected")
	}
	if VerifyPassword("wrong password!!", h1) {
		t.Error("wrong password accepted")
	}
	if VerifyPassword("correct horse battery", "not-a-hash") {
		t.Error("garbage hash accepted")
	}
}

func TestShortPasswordRejected(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("a 5 character password must be rejected")
	}
}

func TestAdminCreateAndAuthenticate(t *testing.T) {
	s := openMem(t)

	if n, err := s.CountAdmins(); err != nil || n != 0 {
		t.Fatalf("fresh db: got %d admins, err %v", n, err)
	}

	if _, err := s.CreateAdmin("hami", "super-secret-1"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if n, _ := s.CountAdmins(); n != 1 {
		t.Fatalf("want 1 admin, got %d", n)
	}
	if _, err := s.CreateAdmin("hami", "another-secret"); err == nil {
		t.Fatal("duplicate username must be rejected")
	}

	sess, admin, err := s.Authenticate("hami", "super-secret-1", time.Hour)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if admin.Username != "hami" || sess.Token == "" {
		t.Fatalf("unexpected session %+v admin %+v", sess, admin)
	}

	got, err := s.SessionAdmin(sess.Token)
	if err != nil || got == nil || got.Username != "hami" {
		t.Fatalf("session lookup: %v %+v", err, got)
	}

	if _, _, err := s.Authenticate("hami", "wrong", time.Hour); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("wrong password: want ErrBadCredentials, got %v", err)
	}
	if _, _, err := s.Authenticate("ghost", "whatever-long", time.Hour); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("unknown user: want ErrBadCredentials, got %v", err)
	}
}

func TestLastLoginIsRecorded(t *testing.T) {
	s := openMem(t)
	if _, err := s.CreateAdmin("hami", "super-secret-1"); err != nil {
		t.Fatal(err)
	}
	a, _ := s.GetAdminByUsername("hami")
	if a.LastLogin != nil {
		t.Fatal("a brand new admin must not have a last login")
	}
	if _, _, err := s.Authenticate("hami", "super-secret-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	a, _ = s.GetAdminByUsername("hami")
	if a.LastLogin == nil {
		t.Fatal("last login was not recorded")
	}
}

func TestExpiredSessionIsRejectedAndPurged(t *testing.T) {
	s := openMem(t)
	if _, err := s.CreateAdmin("hami", "super-secret-1"); err != nil {
		t.Fatal(err)
	}
	sess, _, err := s.Authenticate("hami", "super-secret-1", -time.Minute) // already expired
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionAdmin(sess.Token); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("expired session must be rejected, got %v", err)
	}
	// the rejected token must also be gone from the table
	if _, err := s.SessionAdmin(sess.Token); !errors.Is(err, ErrBadCredentials) {
		t.Fatal("second lookup must still fail")
	}
}

func TestLogoutDropsTheSession(t *testing.T) {
	s := openMem(t)
	if _, err := s.CreateAdmin("hami", "super-secret-1"); err != nil {
		t.Fatal(err)
	}
	sess, _, _ := s.Authenticate("hami", "super-secret-1", time.Hour)
	if err := s.DeleteSession(sess.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionAdmin(sess.Token); !errors.Is(err, ErrBadCredentials) {
		t.Fatal("session survived logout")
	}
}

func TestPasswordChangeRevokesSessions(t *testing.T) {
	s := openMem(t)
	if _, err := s.CreateAdmin("hami", "super-secret-1"); err != nil {
		t.Fatal(err)
	}
	sess, _, _ := s.Authenticate("hami", "super-secret-1", time.Hour)

	if err := s.SetAdminPassword("hami", "a-brand-new-one"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if _, err := s.SessionAdmin(sess.Token); !errors.Is(err, ErrBadCredentials) {
		t.Fatal("an open session survived a password change")
	}
	if _, _, err := s.Authenticate("hami", "super-secret-1", time.Hour); !errors.Is(err, ErrBadCredentials) {
		t.Error("the old password still works")
	}
	if _, _, err := s.Authenticate("hami", "a-brand-new-one", time.Hour); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}
}

func TestLastAdminCannotBeDeleted(t *testing.T) {
	s := openMem(t)
	if _, err := s.CreateAdmin("hami", "super-secret-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAdmin("hami"); err == nil {
		t.Fatal("deleting the only admin would lock everyone out; it must fail")
	}
	if _, err := s.CreateAdmin("second", "super-secret-2"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAdmin("hami"); err != nil {
		t.Fatalf("deleting one of two admins must work: %v", err)
	}
	if n, _ := s.CountAdmins(); n != 1 {
		t.Fatalf("want 1 admin left, got %d", n)
	}
}

func TestSessionTokensAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok := NewSessionToken()
		if len(tok) < 40 {
			t.Fatalf("token too short: %q", tok)
		}
		if seen[tok] {
			t.Fatal("duplicate session token")
		}
		seen[tok] = true
	}
}
