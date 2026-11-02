package httpapi_test

import (
	"github.com/google/uuid"
	"testing"
)

func TestInternationalAuthFlows(t *testing.T) {
	h := setup(t)
	email := "signup-" + uuid.NewString() + "@example.com"
	var first struct {
		AccessToken string `json:"accessToken"`
		IsNewUser   bool   `json:"isNewUser"`
		Me          struct {
			User struct {
				ID, Nickname, Email string
				LoginMethods        []string
			} `json:"user"`
		} `json:"me"`
	}
	h.do("POST", "/v1/auth/email/send", map[string]string{"email": email}, 204, nil)
	h.do("POST", "/v1/auth/email/verify", map[string]string{"email": email, "code": "000000"}, 400, nil)
	h.do("POST", "/v1/auth/email/verify", map[string]string{"email": email, "code": "123456"}, 200, &first)
	if !first.IsNewUser || first.Me.User.Email != email || first.AccessToken == "" {
		t.Fatalf("email signup: %+v", first)
	}
	if len(first.Me.User.LoginMethods) != 1 || first.Me.User.LoginMethods[0] != "email" {
		t.Fatal(first.Me.User.LoginMethods)
	}
	h.do("POST", "/v1/auth/email/verify", map[string]string{"email": email, "code": "123456"}, 400, nil)
	// The same email claim must never implicitly merge an independently verified identity.
	emailID := first.Me.User.ID
	h.do("POST", "/v1/auth/google", map[string]string{"identityToken": "dev:" + email}, 200, &first)
	if !first.IsNewUser || first.Me.User.ID == emailID || first.Me.User.LoginMethods[0] != "google" {
		t.Fatalf("Google signup: %+v", first)
	}
	googleID := first.Me.User.ID
	h.do("POST", "/v1/auth/google", map[string]string{"identityToken": "dev:" + email}, 200, &first)
	if first.IsNewUser || first.Me.User.ID != googleID {
		t.Fatalf("Google returning user: %+v", first)
	}
	h.token = first.AccessToken
	h.do("GET", "/v1/me", nil, 200, nil)
	h.do("POST", "/v1/auth/google", map[string]string{"identityToken": "invalid"}, 401, nil)
}
