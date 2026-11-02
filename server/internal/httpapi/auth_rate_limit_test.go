package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/hwq830/zeusai/server/internal/auth"
	"github.com/redis/go-redis/v9"
)

func TestEmailSendHasIPBudgetIndependentOfAccountAddress(t *testing.T) {
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	defer client.Close()
	s := Server{Auth: &auth.Service{Redis: client}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.limitInternationalAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	send := func(ip string) int {
		req := httptest.NewRequest("POST", "/v1/auth/email/send", nil)
		req.RemoteAddr = ip
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	for i := 0; i < 20; i++ {
		if code := send("192.0.2.1:1000"); code != 204 {
			t.Fatal(code)
		}
	}
	if code := send("192.0.2.1:2000"); code != 429 {
		t.Fatal("port change bypassed IP budget", code)
	}
	if code := send("192.0.2.2:1000"); code != 204 {
		t.Fatal("different IP shared budget", code)
	}
}
