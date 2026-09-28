package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func init() { bcryptCost = bcrypt.MinCost }

func TestPasswordMatches(t *testing.T) {
	hash, err := HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$2") {
		t.Fatalf("want a bcrypt hash, got %q", hash)
	}
	tests := []struct {
		name       string
		configured string
		supplied   string
		want       bool
	}{
		{"plain match", "s3cret", "s3cret", true},
		{"plain mismatch", "s3cret", "S3cret", false},
		{"hash match", hash, "s3cret", true},
		{"hash mismatch", hash, "nope", false},
		{"hash is not accepted literally", hash, hash, false},
		{"empty configured never matches", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := passwordMatches(tt.configured, tt.supplied); got != tt.want {
				t.Fatalf("passwordMatches(%q, %q) = %v want %v", tt.configured, tt.supplied, got, tt.want)
			}
		})
	}
}

func TestLoginAcceptsBcryptHash(t *testing.T) {
	hash, _ := HashPassword("secret")
	h := apiLoginHandler("admin", hash, false)
	try := func(pw string) int {
		r := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader("username=admin&password="+pw))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.RemoteAddr = "10.9.9.9:1234"
		rw := httptest.NewRecorder()
		h(rw, r)
		return rw.Code
	}
	if got := try("secret"); got != http.StatusFound {
		t.Fatalf("correct password against hash: want 302 got %d", got)
	}
	if got := try("wrong"); got != http.StatusUnauthorized {
		t.Fatalf("wrong password against hash: want 401 got %d", got)
	}
}
