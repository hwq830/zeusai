package httpapi

import (
	"context"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/briefing"
	"github.com/hwq830/zeusai/server/internal/httpapi/gen"
	"github.com/hwq830/zeusai/server/internal/task"
	"github.com/hwq830/zeusai/server/internal/template"
)

func errInvalid(msg string) error { return apperr.Invalid(msg) }

// ---- templates

func (s *Server) ListTemplates(ctx context.Context, req gen.ListTemplatesRequestObject) (gen.ListTemplatesResponseObject, error) {
	p := req.Params
	lim, off := page(p.Cursor, p.Limit)
	scope := "official"
	if p.Scope != nil {
		scope = string(*p.Scope)
	}
	items, cats, err := s.Templates.List(ctx, userID(ctx), template.ListParams{Scope: scope, Category: deref(p.Category), Q: deref(p.Q), Limit: lim, Offset: off})
	if err != nil {
		return nil, err
	}
	return gen.ListTemplates200JSONResponse{
		Items: recode[[]gen.Template](items), Categories: cats, NextCursor: nextCursor(len(items), lim, off),
	}, nil
}

func (s *Server) GetTemplate(ctx context.Context, req gen.GetTemplateRequestObject) (gen.GetTemplateResponseObject, error) {
	t, err := s.Templates.Get(ctx, userID(ctx), req.TemplateId)
	if err != nil {
		return nil, err
	}
	return gen.GetTemplate200JSONResponse(recode[gen.Template](t)), nil
}

func (s *Server) CreateTemplate(ctx context.Context, req gen.CreateTemplateRequestObject) (gen.CreateTemplateResponseObject, error) {
	t, err := s.Templates.Create(ctx, userID(ctx), recode[template.Input](req.Body))
	if err != nil {
		return nil, err
	}
	return gen.CreateTemplate201JSONResponse(recode[gen.Template](t)), nil
}

func (s *Server) UpdateTemplate(ctx context.Context, req gen.UpdateTemplateRequestObject) (gen.UpdateTemplateResponseObject, error) {
	t, err := s.Templates.Update(ctx, userID(ctx), req.TemplateId, recode[template.Input](req.Body))
	if err != nil {
		return nil, err
	}
	return gen.UpdateTemplate200JSONResponse(recode[gen.Template](t)), nil
}

func (s *Server) DeleteTemplate(ctx context.Context, req gen.DeleteTemplateRequestObject) (gen.DeleteTemplateResponseObject, error) {
	if err := s.Templates.Delete(ctx, userID(ctx), req.TemplateId); err != nil {
		return nil, err
	}
	return gen.DeleteTemplate204Response{}, nil
}

func (s *Server) CopyTemplate(ctx context.Context, req gen.CopyTemplateRequestObject) (gen.CopyTemplateResponseObject, error) {
	t, err := s.Templates.Copy(ctx, userID(ctx), req.TemplateId)
	if err != nil {
		return nil, err
	}
	return gen.CopyTemplate201JSONResponse(recode[gen.Template](t)), nil
}

func (s *Server) DraftTemplate(ctx context.Context, req gen.DraftTemplateRequestObject) (gen.DraftTemplateResponseObject, error) {
	return nil, errNotImplemented
}

// ---- subscriptions

func (s *Server) ListSubscriptions(ctx context.Context, _ gen.ListSubscriptionsRequestObject) (gen.ListSubscriptionsResponseObject, error) {
	subs, err := s.Briefings.List(ctx, userID(ctx))
	if err != nil {
		return nil, err
	}
	return gen.ListSubscriptions200JSONResponse{Items: recode[[]gen.Subscription](subs)}, nil
}

func (s *Server) CreateSubscription(ctx context.Context, req gen.CreateSubscriptionRequestObject) (gen.CreateSubscriptionResponseObject, error) {
	u, err := s.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	sub, err := s.Briefings.Create(ctx, u, recode[briefing.Input](req.Body))
	if err != nil {
		return nil, err
	}
	return gen.CreateSubscription201JSONResponse(recode[gen.Subscription](sub)), nil
}

func (s *Server) DraftSubscription(ctx context.Context, req gen.DraftSubscriptionRequestObject) (gen.DraftSubscriptionResponseObject, error) {
	u, err := s.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	in, err := s.Briefings.Draft(ctx, u, req.Body.Prompt)
	if err != nil {
		return nil, err
	}
	return gen.DraftSubscription200JSONResponse(recode[gen.SubscriptionInput](in)), nil
}

func (s *Server) GetSubscription(ctx context.Context, req gen.GetSubscriptionRequestObject) (gen.GetSubscriptionResponseObject, error) {
	sub, err := s.Briefings.Get(ctx, userID(ctx), req.SubscriptionId)
	if err != nil {
		return nil, err
	}
	return gen.GetSubscription200JSONResponse(recode[gen.Subscription](sub)), nil
}

func (s *Server) UpdateSubscription(ctx context.Context, req gen.UpdateSubscriptionRequestObject) (gen.UpdateSubscriptionResponseObject, error) {
	sub, err := s.Briefings.Update(ctx, userID(ctx), req.SubscriptionId, recode[briefing.Patch](req.Body))
	if err != nil {
		return nil, err
	}
	return gen.UpdateSubscription200JSONResponse(recode[gen.Subscription](sub)), nil
}

func (s *Server) DeleteSubscription(ctx context.Context, req gen.DeleteSubscriptionRequestObject) (gen.DeleteSubscriptionResponseObject, error) {
	if err := s.Briefings.Delete(ctx, userID(ctx), req.SubscriptionId); err != nil {
		return nil, err
	}
	return gen.DeleteSubscription204Response{}, nil
}

func (s *Server) RunSubscription(ctx context.Context, req gen.RunSubscriptionRequestObject) (gen.RunSubscriptionResponseObject, error) {
	t, err := s.Briefings.RunNow(ctx, userID(ctx), req.SubscriptionId)
	if err != nil {
		return nil, err
	}
	return gen.RunSubscription202JSONResponse(recode[gen.Task](task.FromRow(t))), nil
}

// ---- briefings

func (s *Server) ListBriefings(ctx context.Context, req gen.ListBriefingsRequestObject) (gen.ListBriefingsResponseObject, error) {
	lim, off := page(req.Params.Cursor, req.Params.Limit)
	items, err := s.Briefings.ListBriefings(ctx, userID(ctx), briefing.ListParams{SubscriptionID: deref(req.Params.SubscriptionId), Limit: lim, Offset: off})
	if err != nil {
		return nil, err
	}
	return gen.ListBriefings200JSONResponse{Items: recode[[]gen.BriefingSummary](items), NextCursor: nextCursor(len(items), lim, off)}, nil
}

func (s *Server) ListTodayBriefings(ctx context.Context, _ gen.ListTodayBriefingsRequestObject) (gen.ListTodayBriefingsResponseObject, error) {
	u, err := s.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	date, items, upcoming, err := s.Briefings.Today(ctx, u)
	if err != nil {
		return nil, err
	}
	return gen.ListTodayBriefings200JSONResponse(recode[gen.TodayBriefings](map[string]any{
		"date": date, "items": items, "upcoming": upcoming,
	})), nil
}

func (s *Server) GetBriefing(ctx context.Context, req gen.GetBriefingRequestObject) (gen.GetBriefingResponseObject, error) {
	b, err := s.Briefings.GetBriefing(ctx, userID(ctx), req.BriefingId)
	if err != nil {
		return nil, err
	}
	return gen.GetBriefing200JSONResponse(recode[gen.Briefing](b)), nil
}

func (s *Server) PutBriefingFeedback(ctx context.Context, req gen.PutBriefingFeedbackRequestObject) (gen.PutBriefingFeedbackResponseObject, error) {
	b := req.Body
	if err := s.Briefings.Feedback(ctx, userID(ctx), req.BriefingId, deref(b.ItemId), string(b.Rating), deref(b.Comment)); err != nil {
		return nil, err
	}
	return gen.PutBriefingFeedback204Response{}, nil
}
