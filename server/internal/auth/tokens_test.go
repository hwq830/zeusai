package auth

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAccessToken(t *testing.T) {
	tk := NewTokens("secret", time.Minute)
	id := uuid.New()
	s, ttl, err := tk.SignAccess(id)
	if err != nil || ttl != time.Minute {
		t.Fatal(err)
	}
	got, err := tk.VerifyAccess(s)
	if err != nil || got != id {
		t.Fatalf("verify: %v %v", got, err)
	}
	if _, err := NewTokens("other", time.Minute).VerifyAccess(s); err == nil {
		t.Fatal("token signed with another secret must fail")
	}
	tk.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := tk.VerifyAccess(s); err == nil {
		t.Fatal("expired token must fail")
	}
}

func TestMaskPhone(t *testing.T) {
	if got := MaskPhone("+8613800138000"); got != "138****8000" {
		t.Fatal(got)
	}
}
