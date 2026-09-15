package main

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// The printed hash is the value BACKEND_ADMIN_PASSWORD_HASH wants, so it has
// to verify the password it was made from, trailing newline stripped: an
// operator pipes `echo`, not `echo -n`.
func TestHashPassword_printsAVerifiableBcryptHash(t *testing.T) {
	var out strings.Builder

	if err := hashPassword(strings.NewReader("hunter2\n"), &out); err != nil {
		t.Fatalf("hashPassword() err = %v", err)
	}

	hash := strings.TrimSpace(out.String())
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("hunter2")); err != nil {
		t.Fatalf("the printed hash %q does not verify hunter2: %v", hash, err)
	}
}

func TestHashPassword_refusesAnEmptyPassword(t *testing.T) {
	var out strings.Builder

	err := hashPassword(strings.NewReader("\n"), &out)

	if err == nil || out.Len() != 0 {
		t.Fatalf("err = %v, out = %q; want an error and no hash", err, out.String())
	}
}
