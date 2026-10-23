package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/hwq830/zeusai/server/internal/db"
)

func TestParseP8(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	got, err := ParseP8(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil || !got.Equal(k) {
		t.Fatal(err)
	}
	if _, err := ParseP8([]byte("nope")); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestAPNsSend(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("APNs needs HTTP/2, got %s", r.Proto)
		}
		tok, err := jwt.Parse(strings.TrimPrefix(r.Header.Get("authorization"), "bearer "), func(t *jwt.Token) (any, error) { return &k.PublicKey, nil },
			jwt.WithValidMethods([]string{"ES256"}))
		if err != nil || tok.Header["kid"] != "KID" || r.Header.Get("apns-topic") != "app.zeus" {
			http.Error(w, `{"reason":"InvalidProviderToken"}`, http.StatusForbidden)
			return
		}
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/dead") {
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(`{"reason":"Unregistered"}`))
			return
		}
		if p["deeplink"] != "zeus://report/1" {
			t.Errorf("payload %v", p)
		}
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	now := time.Now()
	a := &APNs{KeyID: "KID", TeamID: "TEAM", Topic: "app.zeus", Key: k, HTTP: srv.Client(), Now: func() time.Time { return now },
		Hosts: map[string]string{"sandbox": srv.URL}}
	body, _ := json.Marshal(APNsPayload(Notification{Title: "t", DeepLink: "zeus://report/1"}))
	if gone, err := a.send(context.Background(), deviceFor("ok"), body); gone || err != nil {
		t.Fatal(gone, err)
	}
	if gone, err := a.send(context.Background(), deviceFor("dead"), body); !gone || err != nil {
		t.Fatal("410 must report the device as gone", gone, err)
	}
	first, _ := a.bearer()
	now = now.Add(51 * time.Minute)
	if second, _ := a.bearer(); second == first {
		t.Fatal("provider token not refreshed after 50 minutes")
	}
	a.Topic = "wrong"
	if _, err := a.send(context.Background(), deviceFor("ok"), body); err == nil || !strings.Contains(err.Error(), "InvalidProviderToken") {
		t.Fatalf("err %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths %v", paths)
	}
}

func deviceFor(token string) db.Device {
	return db.Device{PushToken: token, Platform: "ios", ApnsEnv: "sandbox"}
}
