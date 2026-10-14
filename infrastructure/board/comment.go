package board

import (
	"context"
	"encoding/json"

	"ai/domain"
)

// Типы комментариев задач. Канонические значения — в domain, здесь алиасы.
const (
	CommentTypeQA     = domain.CommentTypeQA
	CommentTypeUser   = domain.CommentTypeUser
	CommentTypeSystem = domain.CommentTypeSystem
)

// Comment — комментарий задачи (author = qa/user/system). Тип принадлежит
// домену (domain.Comment), здесь — алиас.
type Comment = domain.Comment

// Comments — список комментариев (domain.Comments).
type Comments = domain.Comments

// DecodeComments decodes JSON comments.
func DecodeComments(data []byte) (Comments, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var cs Comments
	if err := json.Unmarshal(data, &cs); err != nil {
		return nil, err
	}
	for i := range cs {
		cs[i].Normalize()
	}
	return cs, nil
}

// ---- Контекст комментариев ----
// Реализация принадлежит домену (domain); здесь — обёртки/алиасы для обратной
// совместимости.

// CommentSource — живой поставщик комментариев (domain.CommentSource).
type CommentSource = domain.CommentSource

// NewCommentContext кладёт снапшот комментариев в контекст.
func NewCommentContext(ctx context.Context, cms Comments) context.Context {
	return domain.NewCommentContext(ctx, cms)
}

// CommentsFromContext достаёт снапшот комментариев из контекста.
func CommentsFromContext(ctx context.Context) Comments {
	return domain.CommentsFromContext(ctx)
}

// WithCommentSource кладёт живой поставщик комментариев в контекст.
func WithCommentSource(ctx context.Context, src CommentSource) context.Context {
	return domain.WithCommentSource(ctx, src)
}

// CommentSourceFromContext достаёт живой поставщик комментариев.
func CommentSourceFromContext(ctx context.Context) CommentSource {
	return domain.CommentSourceFromContext(ctx)
}

// AllComments отдаёт комментарии для текущего запроса к модели: живой источник
// авторитетен и полностью заменяет снапшот (аналог AllInjections).
func AllComments(ctx context.Context) Comments {
	return domain.AllComments(ctx)
}
