package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/redis/go-redis/v9"
)

var authRequestBudget = redis.NewScript(`
local n=redis.call('INCR',KEYS[1])
if n==1 then redis.call('EXPIRE',KEYS[1],300) end
return n
`)

// Account-level code limits are complemented by an IP limit so rotating email
// addresses cannot turn the public send endpoint into an unrestricted mail sender.
func (s *Server) limitInternationalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(60)
		kind := "verify"
		switch r.URL.Path {
		case "/v1/auth/email/send":
			limit = 20
			kind = "send"
		case "/v1/auth/email/verify", "/v1/auth/google":
		default:
			next.ServeHTTP(w, r)
			return
		}
		if s.Auth == nil || s.Auth.Redis == nil {
			s.writeError(w, r, apperr.Unavailable("登录服务暂时不可用，请稍后再试"))
			return
		}
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		hash := sha256.Sum256([]byte(ip))
		key := "auth:ip:" + kind + ":" + hex.EncodeToString(hash[:])
		n, err := authRequestBudget.Run(r.Context(), s.Auth.Redis, []string{key}).Int64()
		if err != nil {
			s.writeError(w, r, apperr.Unavailable("登录服务暂时不可用，请稍后再试"))
			return
		}
		if n > limit {
			s.writeError(w, r, apperr.RateLimited("验证码发送太频繁，请稍后再试"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
