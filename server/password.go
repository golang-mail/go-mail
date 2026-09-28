package server

import (
	"crypto/subtle"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is a variable so tests can lower it.
var bcryptCost = bcrypt.DefaultCost

// HashPassword returns a bcrypt hash suitable for server.yml's password field.
func HashPassword(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	return string(h), err
}

func isBcryptHash(s string) bool {
	return strings.HasPrefix(s, "$2a$") || strings.HasPrefix(s, "$2b$") || strings.HasPrefix(s, "$2y$")
}

// passwordMatches accepts either a bcrypt hash or a plain password in the
// configuration. An empty configured password never matches.
func passwordMatches(configured, supplied string) bool {
	if configured == "" {
		return false
	}
	if isBcryptHash(configured) {
		return bcrypt.CompareHashAndPassword([]byte(configured), []byte(supplied)) == nil
	}
	return subtle.ConstantTimeCompare([]byte(configured), []byte(supplied)) == 1
}
