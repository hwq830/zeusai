package httpapi

import (
	"context"

	"github.com/hwq830/zeusai/server/internal/httpapi/gen"
	"github.com/hwq830/zeusai/server/internal/notify"
)

// ---- message center

func categoryStrings(p *[]gen.MessageCategory) []string {
	if p == nil {
		return nil
	}
	out := make([]string, len(*p))
	for i, c := range *p {
		out[i] = string(c)
	}
	return out
}

func (s *Server) ListMessages(ctx context.Context, req gen.ListMessagesRequestObject) (gen.ListMessagesResponseObject, error) {
	lim, off := page(req.Params.Cursor, req.Params.Limit)
	items, err := s.Notify.List(ctx, userID(ctx), categoryStrings(req.Params.Category), lim, off)
	if err != nil {
		return nil, err
	}
	unread, err := s.Notify.UnreadCount(ctx, userID(ctx))
	if err != nil {
		return nil, err
	}
	return gen.ListMessages200JSONResponse{Items: recode[[]gen.Message](items), UnreadCount: unread, NextCursor: nextCursor(len(items), lim, off)}, nil
}

func (s *Server) GetUnreadMessageCount(ctx context.Context, _ gen.GetUnreadMessageCountRequestObject) (gen.GetUnreadMessageCountResponseObject, error) {
	n, err := s.Notify.UnreadCount(ctx, userID(ctx))
	if err != nil {
		return nil, err
	}
	return gen.GetUnreadMessageCount200JSONResponse{Count: n}, nil
}

func (s *Server) ReadAllMessages(ctx context.Context, req gen.ReadAllMessagesRequestObject) (gen.ReadAllMessagesResponseObject, error) {
	if err := s.Notify.MarkAllRead(ctx, userID(ctx), categoryStrings(req.Params.Category)); err != nil {
		return nil, err
	}
	return gen.ReadAllMessages204Response{}, nil
}

func (s *Server) DeleteMessage(ctx context.Context, req gen.DeleteMessageRequestObject) (gen.DeleteMessageResponseObject, error) {
	if err := s.Notify.Delete(ctx, userID(ctx), req.MessageId); err != nil {
		return nil, err
	}
	return gen.DeleteMessage204Response{}, nil
}

func (s *Server) ReadMessage(ctx context.Context, req gen.ReadMessageRequestObject) (gen.ReadMessageResponseObject, error) {
	if err := s.Notify.MarkRead(ctx, userID(ctx), req.MessageId); err != nil {
		return nil, err
	}
	return gen.ReadMessage204Response{}, nil
}

func (s *Server) OpenedMessage(ctx context.Context, req gen.OpenedMessageRequestObject) (gen.OpenedMessageResponseObject, error) {
	if err := s.Notify.MarkOpened(ctx, userID(ctx), req.MessageId); err != nil {
		return nil, err
	}
	return gen.OpenedMessage204Response{}, nil
}

func (s *Server) GetNotificationPreferences(ctx context.Context, _ gen.GetNotificationPreferencesRequestObject) (gen.GetNotificationPreferencesResponseObject, error) {
	p, err := s.Notify.Prefs(ctx, userID(ctx))
	if err != nil {
		return nil, err
	}
	return gen.GetNotificationPreferences200JSONResponse(recode[gen.NotificationPreferences](p)), nil
}

func (s *Server) PutNotificationPreferences(ctx context.Context, req gen.PutNotificationPreferencesRequestObject) (gen.PutNotificationPreferencesResponseObject, error) {
	p, err := s.Notify.UpdatePrefs(ctx, userID(ctx), recode[notify.Prefs](req.Body))
	if err != nil {
		return nil, err
	}
	return gen.PutNotificationPreferences200JSONResponse(recode[gen.NotificationPreferences](p)), nil
}
