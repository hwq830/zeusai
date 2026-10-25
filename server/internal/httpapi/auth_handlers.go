package httpapi

import (
	"context"
	"time"

	"github.com/hwq830/zeusai/server/internal/auth"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/httpapi/gen"
)

func (s *Server) session(ctx context.Context, ss auth.Session, err error) (gen.Session, error) {
	if err != nil {
		return gen.Session{}, err
	}
	me, err := s.me(ctx, ss.User)
	if err != nil {
		return gen.Session{}, err
	}
	return gen.Session{
		AccessToken: ss.AccessToken, RefreshToken: ss.RefreshToken, ExpiresIn: int(ss.ExpiresIn / time.Second),
		IsNewUser: ss.IsNewUser, Me: me,
	}, nil
}

func (s *Server) me(ctx context.Context, u db.User) (gen.Me, error) {
	usage, err := s.Quota.Usage(ctx, u)
	if err != nil {
		return gen.Me{}, err
	}
	methods := []gen.UserLoginMethods{}
	if u.Phone != nil {
		methods = append(methods, gen.Phone)
	}
	if u.WechatUnionid != nil {
		methods = append(methods, gen.Wechat)
	}
	if u.AppleSub != nil {
		methods = append(methods, gen.Apple)
	}
	user := gen.User{Id: u.ID.String(), Nickname: u.Nickname, Timezone: u.Timezone, CreatedAt: u.CreatedAt, LoginMethods: methods}
	if u.Phone != nil {
		m := auth.MaskPhone(*u.Phone)
		user.PhoneMasked = &m
	}
	planName := map[string]string{"free": "免费版"}[u.PlanCode]
	if planName == "" {
		planName = u.PlanCode
	}
	return gen.Me{
		User: user,
		Plan: gen.Plan{Code: u.PlanCode, Name: planName, ExpiresAt: u.PlanExpiresAt},
		Quota: gen.Quota{
			BriefingSubscriptions: gen.QuotaItem{Used: usage.Subscriptions.Used, Limit: usage.Subscriptions.Limit},
			Tokens:                s.tokenQuota(u, usage.Tokens),
		},
	}, nil
}

func (s *Server) SendSmsCode(ctx context.Context, req gen.SendSmsCodeRequestObject) (gen.SendSmsCodeResponseObject, error) {
	if err := s.Auth.SendSMSCode(ctx, req.Body.Phone); err != nil {
		return nil, err
	}
	return gen.SendSmsCode204Response{}, nil
}

func (s *Server) VerifySmsCode(ctx context.Context, req gen.VerifySmsCodeRequestObject) (gen.VerifySmsCodeResponseObject, error) {
	ss, err := s.Auth.VerifySMSCode(ctx, req.Body.Phone, req.Body.Code)
	out, err := s.session(ctx, ss, err)
	if err != nil {
		return nil, err
	}
	return gen.VerifySmsCode200JSONResponse{SessionJSONResponse: gen.SessionJSONResponse(out)}, nil
}

func (s *Server) LoginPhoneOneClick(ctx context.Context, req gen.LoginPhoneOneClickRequestObject) (gen.LoginPhoneOneClickResponseObject, error) {
	ss, err := s.Auth.LoginOneClick(ctx, req.Body.Token)
	out, err := s.session(ctx, ss, err)
	if err != nil {
		return nil, err
	}
	return gen.LoginPhoneOneClick200JSONResponse{SessionJSONResponse: gen.SessionJSONResponse(out)}, nil
}

func (s *Server) LoginApple(ctx context.Context, req gen.LoginAppleRequestObject) (gen.LoginAppleResponseObject, error) {
	ss, err := s.Auth.LoginApple(ctx, req.Body.IdentityToken, deref(req.Body.Nickname))
	out, err := s.session(ctx, ss, err)
	if err != nil {
		return nil, err
	}
	return gen.LoginApple200JSONResponse{SessionJSONResponse: gen.SessionJSONResponse(out)}, nil
}

func (s *Server) LoginWechat(ctx context.Context, req gen.LoginWechatRequestObject) (gen.LoginWechatResponseObject, error) {
	ss, err := s.Auth.LoginWechat(ctx, req.Body.Code)
	out, err := s.session(ctx, ss, err)
	if err != nil {
		return nil, err
	}
	return gen.LoginWechat200JSONResponse{SessionJSONResponse: gen.SessionJSONResponse(out)}, nil
}

func (s *Server) RefreshSession(ctx context.Context, req gen.RefreshSessionRequestObject) (gen.RefreshSessionResponseObject, error) {
	ss, err := s.Auth.Refresh(ctx, req.Body.RefreshToken)
	out, err := s.session(ctx, ss, err)
	if err != nil {
		return nil, err
	}
	return gen.RefreshSession200JSONResponse{SessionJSONResponse: gen.SessionJSONResponse(out)}, nil
}

func (s *Server) Logout(ctx context.Context, req gen.LogoutRequestObject) (gen.LogoutResponseObject, error) {
	if err := s.Auth.Logout(ctx, userID(ctx), req.Body.RefreshToken); err != nil {
		return nil, err
	}
	return gen.Logout204Response{}, nil
}

func (s *Server) GetMe(ctx context.Context, _ gen.GetMeRequestObject) (gen.GetMeResponseObject, error) {
	u, err := s.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	me, err := s.me(ctx, u)
	if err != nil {
		return nil, err
	}
	return gen.GetMe200JSONResponse(me), nil
}

func (s *Server) UpdateMe(ctx context.Context, req gen.UpdateMeRequestObject) (gen.UpdateMeResponseObject, error) {
	if tz := req.Body.Timezone; tz != nil {
		if _, err := time.LoadLocation(*tz); err != nil || *tz == "" {
			return nil, errInvalid("时区无效")
		}
	}
	u, err := s.Q.UpdateUserProfile(ctx, db.UpdateUserProfileParams{ID: userID(ctx), Nickname: req.Body.Nickname, Timezone: req.Body.Timezone})
	if err != nil {
		return nil, err
	}
	me, err := s.me(ctx, u)
	if err != nil {
		return nil, err
	}
	return gen.UpdateMe200JSONResponse(me), nil
}

// DeleteAccount hard-deletes the user; every table cascades from users. Briefing schedules
// are deleted first so nothing fires for a deleted account.
func (s *Server) DeleteAccount(ctx context.Context, _ gen.DeleteAccountRequestObject) (gen.DeleteAccountResponseObject, error) {
	uid := userID(ctx)
	subs, err := s.Briefings.List(ctx, uid)
	if err != nil {
		return nil, err
	}
	for _, sub := range subs {
		if err := s.Briefings.Delete(ctx, uid, sub.ID); err != nil && !isNotFound(err) {
			return nil, err
		}
	}
	if err := s.Q.DeleteUser(ctx, uid); err != nil {
		return nil, err
	}
	return gen.DeleteAccount204Response{}, nil
}

func (s *Server) RegisterDevice(ctx context.Context, req gen.RegisterDeviceRequestObject) (gen.RegisterDeviceResponseObject, error) {
	b := req.Body
	env := "production"
	if b.ApnsEnv != nil {
		env = string(*b.ApnsEnv)
	}
	if b.PushToken == "" {
		return nil, errInvalid("pushToken 不能为空")
	}
	if err := s.Q.UpsertDevice(ctx, db.UpsertDeviceParams{
		PushToken: b.PushToken, UserID: userID(ctx), Platform: string(b.Platform), ApnsEnv: env,
		AppVersion: deref(b.AppVersion), Locale: deref(b.Locale),
	}); err != nil {
		return nil, err
	}
	return gen.RegisterDevice204Response{}, nil
}

func (s *Server) UnregisterDevice(ctx context.Context, req gen.UnregisterDeviceRequestObject) (gen.UnregisterDeviceResponseObject, error) {
	if err := s.Q.DeleteDevice(ctx, db.DeleteDeviceParams{PushToken: req.PushToken, UserID: userID(ctx)}); err != nil {
		return nil, err
	}
	return gen.UnregisterDevice204Response{}, nil
}
