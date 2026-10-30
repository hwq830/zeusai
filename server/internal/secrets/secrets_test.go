package secrets

import (
	"context"
	"testing"
)

func TestBoxRoundTrip(t *testing.T) {
	b, err := NewBox("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	sealed := b.Seal("qwen", "sk-secret-a1b2")
	got, err := b.Open("qwen", sealed)
	if err != nil || got != "sk-secret-a1b2" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := b.Open("deepseek", sealed); err == nil {
		t.Fatal("a key sealed for one provider must not open as another")
	}
	other, _ := NewBox("ffffffffffffffffffffffffffffffff")
	if _, err := other.Open("qwen", sealed); err == nil {
		t.Fatal("a different master key must not open the value")
	}
}

func TestNewBox(t *testing.T) {
	if b, err := NewBox(""); b != nil || err != nil {
		t.Fatalf("empty secret = %v, %v; want disabled", b, err)
	}
	if _, err := NewBox("short"); err == nil {
		t.Fatal("short secret accepted")
	}
}

func TestLast4(t *testing.T) {
	if got := Last4("sk-abcdef"); got != "cdef" {
		t.Fatalf("Last4 = %q", got)
	}
	if got := Last4("ab"); got != "ab" {
		t.Fatalf("Last4 = %q", got)
	}
}

func TestKeysFallsBackToEnv(t *testing.T) {
	t.Setenv("ZEUS_TEST_KEY", "from-env")
	var k *Keys
	if got := k.Key(context.Background(), "qwen", "ZEUS_TEST_KEY"); got != "from-env" {
		t.Fatalf("Key = %q", got)
	}
	k = &Keys{}
	if got := k.Source(context.Background(), "qwen", "ZEUS_TEST_KEY"); got != "env" {
		t.Fatalf("Source = %q", got)
	}
	if got := k.Source(context.Background(), "qwen", ""); got != "" {
		t.Fatalf("Source = %q", got)
	}
}
