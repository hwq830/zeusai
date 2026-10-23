package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Wechat exchanges an OpenSDK login code for the user's identity (mobile app OAuth).
type Wechat struct {
	AppID, Secret string
	BaseURL       string // default https://api.weixin.qq.com
	HTTP          *http.Client
}

type wxError struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

func (w Wechat) get(ctx context.Context, path string, q url.Values, out any) error {
	base := w.BaseURL
	if base == "" {
		base = "https://api.weixin.qq.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	hc := w.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wechat %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (w Wechat) Exchange(ctx context.Context, code string) (WechatIdentity, error) {
	var tok struct {
		wxError
		AccessToken string `json:"access_token"`
		OpenID      string `json:"openid"`
		UnionID     string `json:"unionid"`
	}
	if err := w.get(ctx, "/sns/oauth2/access_token", url.Values{
		"appid": {w.AppID}, "secret": {w.Secret}, "code": {code}, "grant_type": {"authorization_code"},
	}, &tok); err != nil {
		return WechatIdentity{}, err
	}
	switch {
	case tok.ErrCode == 40029 || tok.ErrCode == 40163: // invalid or already used code
		return WechatIdentity{}, ErrProviderRejected
	case tok.ErrCode != 0:
		return WechatIdentity{}, fmt.Errorf("wechat: %d %s", tok.ErrCode, tok.ErrMsg)
	case tok.UnionID == "":
		// Without an Open Platform account binding there is no unionid, and we key users by it.
		return WechatIdentity{}, fmt.Errorf("wechat: no unionid; bind the app to an Open Platform account")
	}
	id := WechatIdentity{UnionID: tok.UnionID, OpenID: tok.OpenID}
	var info struct {
		wxError
		Nickname string `json:"nickname"`
	}
	if err := w.get(ctx, "/sns/userinfo", url.Values{"access_token": {tok.AccessToken}, "openid": {tok.OpenID}}, &info); err == nil && info.ErrCode == 0 {
		id.Nickname = info.Nickname
	}
	return id, nil
}
