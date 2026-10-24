package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/db"
)

const (
	APNsProduction = "https://api.push.apple.com"
	APNsSandbox    = "https://api.sandbox.push.apple.com"
)

// APNs sends to every iOS device of a user with token-based auth (.p8 key). Devices that
// APNs reports as gone are deleted.
type APNs struct {
	Q      *db.Queries
	KeyID  string
	TeamID string
	Topic  string // the app's bundle id
	Key    *ecdsa.PrivateKey
	// Hosts maps a device's apns_env (production | sandbox) to the APNs base URL.
	Hosts map[string]string
	HTTP  *http.Client
	Log   *slog.Logger
	Now   func() time.Time

	mu       sync.Mutex
	token    string
	issuedAt time.Time
}

// ParseP8 reads the PKCS#8 .p8 key downloaded from the Apple developer portal.
func ParseP8(b []byte) (*ecdsa.PrivateKey, error) {
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("apns: key is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apns: key is not ECDSA")
	}
	return ek, nil
}

func (a *APNs) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// bearer returns a provider token, reused for 50 minutes (Apple rejects tokens older than an
// hour and throttles regenerating them more than every 20 minutes).
func (a *APNs) bearer() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && a.now().Sub(a.issuedAt) < 50*time.Minute {
		return a.token, nil
	}
	t := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"iss": a.TeamID, "iat": a.now().Unix()})
	t.Header["kid"] = a.KeyID
	s, err := t.SignedString(a.Key)
	if err != nil {
		return "", err
	}
	a.token, a.issuedAt = s, a.now()
	return s, nil
}

func (a *APNs) Notify(ctx context.Context, userID uuid.UUID, n Notification) error {
	devices, err := a.Q.ListDevices(ctx, userID)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(APNsPayload(n))
	var errs []error
	reached := 0
	for _, d := range devices {
		if d.Platform != "ios" {
			continue
		}
		gone, err := a.send(ctx, d, body)
		if gone {
			_ = a.Q.DeleteDevice(ctx, db.DeleteDeviceParams{PushToken: d.PushToken, UserID: userID})
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		reached++
	}
	// One failing device must not hide success on the others; report only if all failed.
	if reached == 0 {
		if len(errs) > 0 {
			return errors.Join(errs...)
		}
		return ErrNoDevice
	}
	for _, e := range errs {
		if a.Log != nil {
			a.Log.Warn("apns", "user", userID, "err", e)
		}
	}
	return nil
}

func (a *APNs) send(ctx context.Context, d db.Device, body []byte) (gone bool, err error) {
	host := a.Hosts[d.ApnsEnv]
	if host == "" {
		host = APNsProduction
	}
	tok, err := a.bearer()
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/3/device/"+d.PushToken, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("authorization", "bearer "+tok)
	req.Header.Set("apns-topic", a.Topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	hc := a.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return false, nil
	}
	var r struct{ Reason string }
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = json.Unmarshal(raw, &r)
	if resp.StatusCode == http.StatusGone || r.Reason == "BadDeviceToken" || r.Reason == "Unregistered" {
		return true, nil
	}
	return false, fmt.Errorf("apns: HTTP %d %s", resp.StatusCode, r.Reason)
}
