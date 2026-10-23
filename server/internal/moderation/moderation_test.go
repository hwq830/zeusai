package moderation

import (
	"context"
	"strings"
	"testing"

	"github.com/hwq830/zeusai/server/internal/apperr"
)

func TestKeywords(t *testing.T) {
	k, err := NewKeywords(strings.NewReader("# comment\n\nBad Word\n赌博\n"))
	if err != nil {
		t.Fatal(err)
	}
	for text, blocked := range map[string]bool{
		"a bad-word here": true,
		"BADWORD":         true,
		"网上 赌·博":          true,
		"badminton":       false,
		"":                false,
	} {
		v, _ := k.Check(context.Background(), Input, text)
		if v.Blocked != blocked {
			t.Errorf("%q: blocked=%v", text, v.Blocked)
		}
	}
	if err := Require(context.Background(), k, "ok", "赌博"); apperr.As(err).Code != "content_blocked" {
		t.Fatalf("require: %v", err)
	}
	if err := Require(context.Background(), nil, "赌博"); err != nil {
		t.Fatal("nil checker allows all")
	}
}
