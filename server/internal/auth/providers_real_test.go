package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAliyunSignature(t *testing.T) {
	// Example from Aliyun's RPC signature documentation (testsecret).
	c := AliyunRPC{AccessKeySecret: "testsecret"}
	got := c.Sign(map[string]string{
		"AccessKeyId": "testid", "Action": "DescribeRegions", "Format": "XML",
		"SignatureMethod": "HMAC-SHA1", "SignatureNonce": "3ee8c1b8-83d3-44af-a94f-4e0ad82fd6cf",
		"SignatureVersion": "1.0", "Timestamp": "2016-02-23T12:46:24Z", "Version": "2014-05-26",
	})
	if got != "OLeaidS1JvxuMvnyHOwuJ+uX5qY=" {
		t.Fatalf("signature %s", got)
	}
}

func TestAliyunSMSAndOneClick(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sig := q.Get("Signature")
		p := map[string]string{}
		for k := range q {
			if k != "Signature" {
				p[k] = q.Get(k)
			}
		}
		if (AliyunRPC{AccessKeySecret: "sk"}).Sign(p) != sig {
			_, _ = w.Write([]byte(`{"Code":"SignatureDoesNotMatch"}`))
			return
		}
		switch q.Get("Action") {
		case "SendSms":
			if q.Get("PhoneNumbers") != "13800000000" || q.Get("TemplateParam") != `{"code":"123456"}` {
				_, _ = w.Write([]byte(`{"Code":"isv.BAD"}`))
				return
			}
			_, _ = w.Write([]byte(`{"Code":"OK"}`))
		case "GetMobile":
			if q.Get("AccessToken") == "bad" {
				_, _ = w.Write([]byte(`{"Code":"isv.MOBILE_NUMBER_ILLEGAL"}`))
				return
			}
			_, _ = w.Write([]byte(`{"Code":"OK","GetMobileResultDTO":{"Mobile":"13900000000"}}`))
		}
	}))
	defer srv.Close()
	rpc := AliyunRPC{AccessKeyID: "ak", AccessKeySecret: "sk"}
	sms := AliyunSMS{RPC: rpc, Endpoint: srv.URL, SignName: "Zeus", TemplateCode: "SMS_1"}
	if err := sms.SendCode(context.Background(), "+8613800000000", "123456"); err != nil {
		t.Fatal(err)
	}
	if err := sms.SendCode(context.Background(), "+8613800000001", "123456"); err == nil {
		t.Fatal("error code not surfaced")
	}
	oc := AliyunOneClick{RPC: rpc, Endpoint: srv.URL}
	if p, err := oc.PhoneNumber(context.Background(), "tok"); err != nil || p != "+8613900000000" {
		t.Fatal(p, err)
	}
	if _, err := oc.PhoneNumber(context.Background(), "bad"); !errors.Is(err, ErrProviderRejected) {
		t.Fatal(err)
	}
}

func TestWechatExchange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/sns/oauth2/access_token":
			switch {
			case q.Get("appid") != "wx1" || q.Get("secret") != "s":
				_, _ = w.Write([]byte(`{"errcode":40013,"errmsg":"invalid appid"}`))
			case q.Get("code") == "used":
				_, _ = w.Write([]byte(`{"errcode":40163,"errmsg":"code been used"}`))
			case q.Get("code") == "nounion":
				_, _ = w.Write([]byte(`{"access_token":"at","openid":"o1"}`))
			default:
				_, _ = w.Write([]byte(`{"access_token":"at","openid":"o1","unionid":"u1"}`))
			}
		case "/sns/userinfo":
			_, _ = w.Write([]byte(`{"openid":"o1","nickname":"小明"}`))
		}
	}))
	defer srv.Close()
	wx := Wechat{AppID: "wx1", Secret: "s", BaseURL: srv.URL}
	id, err := wx.Exchange(context.Background(), "c")
	if err != nil || id.UnionID != "u1" || id.OpenID != "o1" || id.Nickname != "小明" {
		t.Fatal(id, err)
	}
	if _, err := wx.Exchange(context.Background(), "used"); !errors.Is(err, ErrProviderRejected) {
		t.Fatal(err)
	}
	if _, err := wx.Exchange(context.Background(), "nounion"); err == nil {
		t.Fatal("missing unionid accepted")
	}
	if _, err := (Wechat{AppID: "x", Secret: "s", BaseURL: srv.URL}).Exchange(context.Background(), "c"); err == nil || errors.Is(err, ErrProviderRejected) {
		t.Fatal("config error must not look like a bad code:", err)
	}
}
