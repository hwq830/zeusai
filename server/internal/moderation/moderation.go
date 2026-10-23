// Package moderation screens user input and model output. Production uses the cloud vendor's
// text moderation API (Aliyun Content Security, wired in once keys exist); Keywords is the
// local first line that also runs in dev and tests.
package moderation

import (
	"bufio"
	"context"
	"io"
	"strings"
	"unicode"

	"github.com/hwq830/zeusai/server/internal/apperr"
)

type Kind string

const (
	Input  Kind = "input"  // text a user typed: topics, questions, template instructions
	Output Kind = "output" // text a model produced
)

type Verdict struct {
	Blocked bool
	Reason  string
}

type Checker interface {
	Check(ctx context.Context, kind Kind, text string) (Verdict, error)
}

// Keywords blocks text containing any listed term, ignoring case, spaces and punctuation
// so that "a b-c" still matches "abc".
type Keywords struct {
	terms []string
}

// NewKeywords reads one term per line; blank lines and lines starting with # are skipped.
func NewKeywords(r io.Reader) (*Keywords, error) {
	k := &Keywords{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if t := normalize(line); t != "" {
			k.terms = append(k.terms, t)
		}
	}
	return k, sc.Err()
}

func (k *Keywords) Check(_ context.Context, _ Kind, text string) (Verdict, error) {
	n := normalize(text)
	for _, t := range k.terms {
		if strings.Contains(n, t) {
			return Verdict{Blocked: true, Reason: "keyword"}, nil
		}
	}
	return Verdict{}, nil
}

func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Chain runs checkers in order and stops at the first block.
type Chain []Checker

func (c Chain) Check(ctx context.Context, kind Kind, text string) (Verdict, error) {
	for _, ch := range c {
		v, err := ch.Check(ctx, kind, text)
		if err != nil || v.Blocked {
			return v, err
		}
	}
	return Verdict{}, nil
}

// Require returns content_blocked if any of texts is blocked. A nil checker allows all.
func Require(ctx context.Context, c Checker, texts ...string) error {
	if c == nil {
		return nil
	}
	for _, t := range texts {
		if strings.TrimSpace(t) == "" {
			continue
		}
		v, err := c.Check(ctx, Input, t)
		if err != nil {
			return apperr.Unavailable("内容审核服务暂时不可用，请稍后再试").WithCause(err)
		}
		if v.Blocked {
			return apperr.ContentBlocked()
		}
	}
	return nil
}
