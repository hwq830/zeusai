package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AliyunRPC signs Aliyun RPC-style API calls (signature version 1.0, HMAC-SHA1).
type AliyunRPC struct {
	AccessKeyID, AccessKeySecret string
	HTTP                         *http.Client
	Now                          func() time.Time
}

func percentEncode(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "*", "%2A")
	return strings.ReplaceAll(e, "%7E", "~")
}

// Sign returns the signature for a GET request with these parameters.
func (c AliyunRPC) Sign(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = percentEncode(k) + "=" + percentEncode(params[k])
	}
	sts := "GET&%2F&" + percentEncode(strings.Join(parts, "&"))
	m := hmac.New(sha1.New, []byte(c.AccessKeySecret+"&"))
	m.Write([]byte(sts))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// Call invokes action on endpoint (https://dysmsapi.aliyuncs.com) and decodes the JSON reply.
func (c AliyunRPC) Call(ctx context.Context, endpoint, action, version string, args map[string]string, out any) error {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	nonce := make([]byte, 12)
	_, _ = rand.Read(nonce)
	p := map[string]string{
		"Action": action, "Version": version, "Format": "JSON", "AccessKeyId": c.AccessKeyID,
		"SignatureMethod": "HMAC-SHA1", "SignatureVersion": "1.0", "SignatureNonce": hex.EncodeToString(nonce),
		"Timestamp": now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	for k, v := range args {
		p[k] = v
	}
	q := url.Values{}
	for k, v := range p {
		q.Set(k, v)
	}
	q.Set("Signature", c.Sign(p))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("aliyun %s: HTTP %d: %w", action, resp.StatusCode, err)
	}
	return nil
}

// domestic turns +86 numbers into the 11-digit form Aliyun expects.
func domestic(phone string) string { return strings.TrimPrefix(phone, "+86") }

// AliyunSMS sends login codes through Aliyun SMS (dysmsapi SendSms).
type AliyunSMS struct {
	RPC          AliyunRPC
	Endpoint     string // default https://dysmsapi.aliyuncs.com
	SignName     string
	TemplateCode string // template with a ${code} variable
}

func (s AliyunSMS) SendCode(ctx context.Context, phone, code string) error {
	ep := s.Endpoint
	if ep == "" {
		ep = "https://dysmsapi.aliyuncs.com"
	}
	param, _ := json.Marshal(map[string]string{"code": code})
	var r struct{ Code, Message string }
	if err := s.RPC.Call(ctx, ep, "SendSms", "2017-05-25", map[string]string{
		"PhoneNumbers": domestic(phone), "SignName": s.SignName, "TemplateCode": s.TemplateCode, "TemplateParam": string(param),
	}, &r); err != nil {
		return err
	}
	if r.Code != "OK" {
		return fmt.Errorf("aliyun sms: %s %s", r.Code, r.Message)
	}
	return nil
}

// AliyunOneClick resolves the carrier one-click token from the app (号码认证 GetMobile).
type AliyunOneClick struct {
	RPC      AliyunRPC
	Endpoint string // default https://dypnsapi.aliyuncs.com
}

func (o AliyunOneClick) PhoneNumber(ctx context.Context, token string) (string, error) {
	ep := o.Endpoint
	if ep == "" {
		ep = "https://dypnsapi.aliyuncs.com"
	}
	var r struct {
		Code, Message      string
		GetMobileResultDTO struct{ Mobile string }
	}
	if err := o.RPC.Call(ctx, ep, "GetMobile", "2017-05-25", map[string]string{"AccessToken": token}, &r); err != nil {
		return "", err
	}
	if r.Code != "OK" || r.GetMobileResultDTO.Mobile == "" {
		if strings.HasPrefix(r.Code, "isv.") {
			return "", ErrProviderRejected
		}
		return "", fmt.Errorf("aliyun one-click: %s %s", r.Code, r.Message)
	}
	return "+86" + r.GetMobileResultDTO.Mobile, nil
}
