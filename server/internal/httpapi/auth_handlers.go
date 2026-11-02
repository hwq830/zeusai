package httpapi

import (
	"context"
	"time"

	"github.com/hwq830/zeusai/server/internal/auth"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/httpapi/gen"
	"github.com/hwq830/zeusai/server/internal/i18n"
	"github.com/hwq830/zeusai/server/internal/market"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

func (s *Server) session(ctx context.Context, ss auth.Session, err error) (gen.Session, error) {
	if err != nil {
		return gen.Session{}, err
	}
	// A new account starts in the language the App is showing.
	if l := i18n.FromContext(ctx); ss.IsNewUser && string(l) != ss.User.UiLanguage {
		lang := string(l)
		if u, err := s.Q.UpdateUserProfile(ctx, db.UpdateUserProfileParams{ID: ss.User.ID, UiLanguage: &lang}); err == nil {
			ss.User = u
		}
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
	if u.Email != nil {
		methods = append(methods, gen.Email)
	}
	if u.GoogleSub != nil {
		methods = append(methods, gen.Google)
	}
	if u.Phone != nil {
		methods = append(methods, gen.Phone)
	}
	if u.WechatUnionid != nil {
		methods = append(methods, gen.Wechat)
	}
	if u.AppleSub != nil {
		methods = append(methods, gen.Apple)
	}
	user := gen.User{Id: u.ID.String(), Nickname: u.Nickname, Timezone: u.Timezone, CreatedAt: u.CreatedAt, LoginMethods: methods,
		UiLanguage: gen.Language(i18n.Parse(u.UiLanguage)), ReportLanguage: gen.ReportLanguage(u.ReportLanguage),
		Market: gen.Market(market.Normalize(u.Market))}
	if u.Email != nil {
		email := openapi_types.Email(*u.Email)
		user.Email = &email
	} else if u.GoogleEmail != nil {
		email := openapi_types.Email(*u.GoogleEmail)
		user.Email = &email
	}
	if u.Phone != nil {
		m := auth.MaskPhone(*u.Phone)
		user.PhoneMasked = &m
	}
	planName := map[string]string{"free": i18n.T(ctx, "免费版", "Free")}[u.PlanCode]
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
	p := db.UpdateUserProfileParams{ID: userID(ctx), Nickname: req.Body.Nickname, Timezone: req.Body.Timezone}
	if l := req.Body.UiLanguage; l != nil {
		if !i18n.Valid(string(*l)) {
			return nil, errInvalid("参数不正确")
		}
		p.UiLanguage = (*string)(l)
	}
	if l := req.Body.ReportLanguage; l != nil {
		if *l != i18n.Auto && !i18n.Valid(string(*l)) {
			return nil, errInvalid("参数不正确")
		}
		p.ReportLanguage = (*string)(l)
	}
	if m := req.Body.Market; m != nil {
		if !market.Valid(string(*m)) {
			return nil, errInvalid("地区无效")
		}
		p.Market = (*string)(m)
	}
	u, err := s.Q.UpdateUserProfile(ctx, p)
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

func (s *Server) SendEmailCode(ctx context.Context, req gen.SendEmailCodeRequestObject) (gen.SendEmailCodeResponseObject, error) {
	if err := s.Auth.SendEmailCode(ctx, string(req.Body.Email)); err != nil {
		return nil, err
	}
	return gen.SendEmailCode204Response{}, nil
}
func (s *Server) VerifyEmailCode(ctx context.Context, req gen.VerifyEmailCodeRequestObject) (gen.VerifyEmailCodeResponseObject, error) {
	ss, err := s.Auth.VerifyEmailCode(ctx, string(req.Body.Email), req.Body.Code)
	out, err := s.session(ctx, ss, err)
	if err != nil {
		return nil, err
	}
	return gen.VerifyEmailCode200JSONResponse{SessionJSONResponse: gen.SessionJSONResponse(out)}, nil
}
func (s *Server) LoginGoogle(ctx context.Context, req gen.LoginGoogleRequestObject) (gen.LoginGoogleResponseObject, error) {
	ss, err := s.Auth.LoginGoogle(ctx, req.Body.IdentityToken)
	out, err := s.session(ctx, ss, err)
	if err != nil {
		return nil, err
	}
	return gen.LoginGoogle200JSONResponse{SessionJSONResponse: gen.SessionJSONResponse(out)}, nil
}
