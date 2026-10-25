package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/httpapi/gen"
	"github.com/hwq830/zeusai/server/internal/remoteconfig"
)

// configTTL is how long the App may use a fetched config before it must refetch on foreground.
const configTTL = time.Hour

func (s *Server) GetRemoteConfig(ctx context.Context, req gen.GetRemoteConfigRequestObject) (gen.GetRemoteConfigResponseObject, error) {
	p := req.Params
	c := remoteconfig.Context{
		InstallID: deref(p.XZeusInstallId), AppVersion: deref(p.XZeusAppVersion), Platform: deref(p.XZeusPlatform),
		OSVersion: deref(p.XZeusOSVersion), Channel: deref(p.XZeusChannel), Locale: primaryLocale(deref(p.AcceptLanguage)),
		Region: s.Region,
	}
	// Optional login: a valid token targets the user; otherwise the install is bucketed.
	if uid := userID(ctx); uid != uuid.Nil {
		if u, err := s.Q.GetUser(ctx, uid); err == nil && u.DisabledAt == nil {
			c = withUser(c, remoteconfig.UserContext(u, s.Region))
		}
	}
	snap := s.RemoteConfig.Current()
	values := map[string]gen.RemoteConfigValue{}
	for k, r := range snap.Evaluate(c, true) {
		out := gen.RemoteConfigValue{ApplyMode: gen.RemoteConfigValueApplyMode(r.ApplyMode)}
		var v any
		if json.Unmarshal(r.Value, &v) == nil && v != nil {
			out.Value = &v
		}
		if r.RuleID != "" {
			id := r.RuleID
			out.RuleId = &id
		}
		if r.Expose {
			t := true
			out.Expose = &t
		}
		values[k] = out
	}
	etag := configETag(snap.Version, values)
	if p.IfNoneMatch != nil && *p.IfNoneMatch == etag {
		return gen.GetRemoteConfig304Response{Headers: gen.GetRemoteConfig304ResponseHeaders{ETag: etag}}, nil
	}
	return gen.GetRemoteConfig200JSONResponse{
		Headers: gen.GetRemoteConfig200ResponseHeaders{ETag: etag},
		Body: gen.RemoteConfig{
			Version: snap.Version, FetchedAt: time.Now().UTC().Truncate(time.Second),
			TtlSeconds: int(configTTL / time.Second), Values: values,
		},
	}, nil
}

func withUser(c, u remoteconfig.Context) remoteconfig.Context {
	c.UserID, c.Plan, c.RegisteredAt = u.UserID, u.Plan, u.RegisteredAt
	return c
}

// configETag changes when the version or any of this caller's values change. A publish that
// does not affect the caller still changes the version, which the App also tracks.
func configETag(version int64, values map[string]gen.RemoteConfigValue) string {
	b, _ := json.Marshal(values) // map keys are sorted
	h := sha256.Sum256(b)
	return `"v` + strconv.FormatInt(version, 10) + "-" + hex.EncodeToString(h[:6]) + `"`
}

// primaryLocale returns the first language tag of an Accept-Language header.
func primaryLocale(h string) string {
	first, _, _ := strings.Cut(h, ",")
	tag, _, _ := strings.Cut(first, ";")
	return strings.TrimSpace(tag)
}

// configVersionHeader tells the App the current config version on every response, so it
// refetches as soon as it talks to us after a publish.
func (s *Server) configVersionHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.RemoteConfig != nil && strings.HasPrefix(r.URL.Path, "/v1/") {
			w.Header().Set("X-Zeus-Config-Version", strconv.FormatInt(s.RemoteConfig.Version(), 10))
		}
		next.ServeHTTP(w, r)
	})
}
