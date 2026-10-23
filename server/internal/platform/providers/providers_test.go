package providers

import (
	"context"
	"testing"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/auth"
	"github.com/hwq830/zeusai/server/internal/platform/config"
)

func TestIdentities(t *testing.T) {
	dev := Identities(config.Config{Env: "dev"}, nil)
	if _, ok := dev.Apple.(auth.DevApple); !ok {
		t.Fatalf("dev without settings should use fakes: %T", dev.Apple)
	}
	prod := Identities(config.Config{Env: "prod"}, nil)
	if _, err := prod.Apple.Verify(context.Background(), "dev:1"); apperr.As(err).Code != "unavailable" {
		t.Fatalf("prod must never accept dev tokens: %v", err)
	}
	full := Identities(config.Config{Env: "prod", AliyunAccessKeyID: "a", AliyunAccessKeySecret: "b", SMSSignName: "Zeus",
		SMSTemplateCode: "SMS_1", OneClickEnabled: true, AppleClientIDs: "app.zeus, web.zeus", WechatAppID: "wx", WechatSecret: "s"}, nil)
	if _, ok := full.SMS.(auth.AliyunSMS); !ok {
		t.Errorf("sms %T", full.SMS)
	}
	if _, ok := full.OneClick.(auth.AliyunOneClick); !ok {
		t.Errorf("oneclick %T", full.OneClick)
	}
	if a, ok := full.Apple.(*auth.Apple); !ok || len(a.ClientIDs) != 2 || a.ClientIDs[1] != "web.zeus" {
		t.Errorf("apple %#v", full.Apple)
	}
	if _, ok := full.Wechat.(auth.Wechat); !ok {
		t.Errorf("wechat %T", full.Wechat)
	}
}
