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

func TestInternationalIdentityProviders(t *testing.T) {
	dev := Identities(config.Config{Env: "dev"}, nil)
	if _, ok := dev.Email.(auth.DevEmail); !ok {
		t.Fatalf("dev email: %T", dev.Email)
	}
	if _, ok := dev.Google.(auth.DevGoogle); !ok {
		t.Fatalf("dev Google: %T", dev.Google)
	}
	prod := Identities(config.Config{Env: "prod"}, nil)
	if _, err := prod.Google.VerifyGoogle(context.Background(), "dev:person@example.com"); err == nil {
		t.Fatal("production accepted dev credential")
	}
	if err := prod.Email.SendCode(context.Background(), "person@example.com", "123456"); err == nil {
		t.Fatal("production silently fake-sent email")
	}
	full := Identities(config.Config{Env: "prod", GoogleClientIDs: "web1, web2", SMTPHost: "smtp.example.com", SMTPFrom: "Zeus <noreply@example.com>"}, nil)
	if google, ok := full.Google.(*auth.Google); !ok || len(google.ClientIDs) != 2 {
		t.Fatalf("Google: %#v", full.Google)
	}
	if sender, ok := full.Email.(auth.SMTPEmail); !ok || sender.Port != 587 {
		t.Fatalf("SMTP: %#v", full.Email)
	}
}

func TestMockSearchHasMockBrowseReader(t *testing.T) {
	reader, err := Browse(config.Config{SearchProvider: "mock"})
	if err != nil || reader == nil {
		t.Fatalf("mock reader: %v", err)
	}
	page, err := reader.Read(context.Background(), "https://news.example.com/example")
	if err != nil || page.Text == "" || page.Title != "Mock source" {
		t.Fatalf("mock page is not available: %+v, %v", page, err)
	}
}
