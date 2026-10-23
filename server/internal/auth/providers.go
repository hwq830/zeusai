package auth

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/hwq830/zeusai/server/internal/apperr"
)

// Identity providers. Production implementations call Aliyun SMS / 号码认证, Apple and
// WeChat; the dev implementations below let the app log in without any accounts.

type SMSSender interface {
	SendCode(ctx context.Context, phone, code string) error
}

type AppleVerifier interface {
	// Verify checks an identity token and returns Apple's stable user id (sub).
	Verify(ctx context.Context, identityToken string) (sub string, err error)
}

type WechatIdentity struct {
	UnionID  string
	OpenID   string
	Nickname string
}

type WechatExchanger interface {
	Exchange(ctx context.Context, code string) (WechatIdentity, error)
}

type OneClickVerifier interface {
	// PhoneNumber resolves a carrier one-click token to an E.164 phone number.
	PhoneNumber(ctx context.Context, token string) (string, error)
}

var ErrProviderRejected = errors.New("identity provider rejected credential")

type LogSMSSender struct{ Log *slog.Logger }

func (s LogSMSSender) SendCode(_ context.Context, phone, code string) error {
	s.Log.Info("dev sms code", "phone", phone, "code", code)
	return nil
}

// DevApple accepts identity tokens of the form "dev:<sub>".
type DevApple struct{}

func (DevApple) Verify(_ context.Context, tok string) (string, error) {
	if sub, ok := strings.CutPrefix(tok, "dev:"); ok && sub != "" {
		return sub, nil
	}
	return "", ErrProviderRejected
}

// DevWechat accepts codes of the form "dev:<unionid>".
type DevWechat struct{}

func (DevWechat) Exchange(_ context.Context, code string) (WechatIdentity, error) {
	if id, ok := strings.CutPrefix(code, "dev:"); ok && id != "" {
		return WechatIdentity{UnionID: id, OpenID: "o" + id, Nickname: "微信用户"}, nil
	}
	return WechatIdentity{}, ErrProviderRejected
}

// DevOneClick accepts tokens of the form "dev:+86138...".
type DevOneClick struct{}

func (DevOneClick) PhoneNumber(_ context.Context, tok string) (string, error) {
	if p, ok := strings.CutPrefix(tok, "dev:"); ok && p != "" {
		return p, nil
	}
	return "", ErrProviderRejected
}

// Unconfigured stands in for a provider whose account is not set up yet (outside dev).
type Unconfigured struct{}

func unconfigured() error {
	return apperr.Unavailable("该登录方式暂未开放，请使用其他方式登录")
}

func (Unconfigured) SendCode(context.Context, string, string) error { return unconfigured() }
func (Unconfigured) Verify(context.Context, string) (string, error) { return "", unconfigured() }
func (Unconfigured) Exchange(context.Context, string) (WechatIdentity, error) {
	return WechatIdentity{}, unconfigured()
}
func (Unconfigured) PhoneNumber(context.Context, string) (string, error) { return "", unconfigured() }
