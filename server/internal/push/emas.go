package push

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/db"
)

// EMASEndpoint is Aliyun Mobile Push (cloudpush) OpenAPI, RPC style, version 2016-08-01.
const EMASEndpoint = "https://cloudpush.aliyuncs.com"

// EMAS sends to a user's Android devices through Aliyun Mobile Push. The device token is the
// EMAS deviceId; EMAS picks the vendor channel (Huawei/Honor/Xiaomi/OPPO/vivo) itself and falls
// back to its own long connection. When the app is killed, vendor channels open PopupActivity
// (辅助弹窗), which reads the same extra parameters as a normal tap.
type EMAS struct {
	Q               *db.Queries
	AppKey          string
	AccessKeyID     string
	AccessKeySecret string
	// PopupActivity is the app's Activity for vendor-channel notifications, e.g.
	// com.jasonhu.zeus.app.push.PushPopupActivity.
	PopupActivity string
	Endpoint      string // defaults to EMASEndpoint; tests point it at httptest
	HTTP          *http.Client
	Log           *slog.Logger
	Now           func() time.Time
}

// EMASExtras is the custom key-value payload the Android app reads on tap: the same keys as
// the APNs payload.
func EMASExtras(n Notification) map[string]string {
	ext := map[string]string{}
	if n.MessageID != "" {
		ext["message_id"] = n.MessageID
	}
	if n.DeepLink != "" {
		ext["deeplink"] = n.DeepLink
	}
	if n.Image != "" {
		ext["image"] = n.Image
	}
	if n.Category != "" {
		ext["category"] = n.Category
	}
	return ext
}

// emasParams builds the Push action's parameters for one batch of device ids (at most 1000).
func (e *EMAS) emasParams(deviceIDs []string, n Notification) map[string]string {
	category := n.Category
	if category == "" {
		category = CategoryService
	}
	ext, _ := json.Marshal(EMASExtras(n))
	p := map[string]string{
		"Action":      "Push",
		"AppKey":      e.AppKey,
		"Target":      "DEVICE",
		"TargetValue": strings.Join(deviceIDs, ","),
		"DeviceType":  "ANDROID",
		"PushType":    "NOTICE",
		"Title":       n.Title,
		"Body":        n.Body,
		// Tapping opens the app; the app routes by the extra parameters.
		"AndroidOpenType":      "APPLICATION",
		"AndroidExtParameters": string(ext),
		// Android 8+ notification channel; the app creates one channel per category.
		"AndroidNotificationChannel": category,
		// Keep for offline devices and resend through vendor channels with the popup activity.
		"StoreOffline":         "true",
		"AndroidRemind":        "true",
		"AndroidPopupTitle":    n.Title,
		"AndroidPopupBody":     n.Body,
		"AndroidPopupActivity": e.PopupActivity,
	}
	// Vendors throttle "operations" messages; everything except marketing is a service/system message.
	if category == CategoryMarketing {
		p["AndroidNotificationVivoChannel"] = "0"
	} else {
		p["AndroidNotificationVivoChannel"] = "1"
		p["AndroidMessageHuaweiCategory"] = "WORK"
	}
	if n.Badge != nil {
		p["AndroidBadgeSetNum"] = fmt.Sprint(*n.Badge)
	}
	return p
}

func (e *EMAS) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *EMAS) Notify(ctx context.Context, userID uuid.UUID, n Notification) error {
	devices, err := e.Q.ListDevices(ctx, userID)
	if err != nil {
		return err
	}
	var ids []string
	for _, d := range devices {
		if d.Platform == "android" {
			ids = append(ids, d.PushToken)
		}
	}
	if len(ids) == 0 {
		return ErrNoDevice
	}
	// A user has a handful of devices; one call covers them (EMAS takes up to 1000 per call).
	return e.call(ctx, e.emasParams(ids, n))
}

// call signs and sends one RPC request (signature version 1.0, HMAC-SHA1).
func (e *EMAS) call(ctx context.Context, params map[string]string) error {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	all := map[string]string{
		"Format":           "JSON",
		"Version":          "2016-08-01",
		"AccessKeyId":      e.AccessKeyID,
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureVersion": "1.0",
		"SignatureNonce":   hex.EncodeToString(nonce),
		"Timestamp":        e.now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	for k, v := range params {
		all[k] = v
	}
	all["Signature"] = SignRPC(http.MethodPost, all, e.AccessKeySecret)

	form := url.Values{}
	for k, v := range all {
		form.Set(k, v)
	}
	endpoint := e.Endpoint
	if endpoint == "" {
		endpoint = EMASEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	hc := e.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var r struct {
		Code      string
		Message   string
		MessageID string `json:"MessageId"`
	}
	_ = json.Unmarshal(raw, &r)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("emas: HTTP %d %s %s", resp.StatusCode, r.Code, r.Message)
	}
	if r.MessageID == "" {
		return errors.New("emas: no MessageId in response")
	}
	if e.Log != nil {
		e.Log.Debug("emas", "message_id", r.MessageID)
	}
	return nil
}

// SignRPC computes an Aliyun RPC-style signature (version 1.0) over all parameters except
// Signature itself.
func SignRPC(method string, params map[string]string, secret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k != "Signature" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = percentEncode(k) + "=" + percentEncode(params[k])
	}
	toSign := method + "&" + percentEncode("/") + "&" + percentEncode(strings.Join(pairs, "&"))
	mac := hmac.New(sha1.New, []byte(secret+"&"))
	mac.Write([]byte(toSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// percentEncode is RFC 3986 encoding as Aliyun specifies: space → %20, * → %2A, ~ unchanged.
func percentEncode(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "*", "%2A")
	return strings.ReplaceAll(e, "%7E", "~")
}

// Multi fans a notification out to every platform sender. It succeeds when any sender reached
// a device and reports ErrNoDevice only when none had one.
type Multi []Notifier

func (m Multi) Notify(ctx context.Context, userID uuid.UUID, n Notification) error {
	var errs []error
	reached := false
	for _, s := range m {
		err := s.Notify(ctx, userID, n)
		switch {
		case err == nil:
			reached = true
		case errors.Is(err, ErrNoDevice):
		default:
			errs = append(errs, err)
		}
	}
	if reached {
		return nil
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return ErrNoDevice
}
