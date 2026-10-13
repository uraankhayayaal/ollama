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

type commentKey struct{}

// NewCommentContext кладёт снапшот комментариев в контекст.
func NewCommentContext(ctx context.Context, cms Comments) context.Context {
	if len(cms) == 0 {
		return ctx
	}
	return context.WithValue(ctx, commentKey{}, cms)
}

// CommentsFromContext достаёт снапшот комментариев из контекста.
func CommentsFromContext(ctx context.Context) Comments {
	if ctx == nil {
		return nil
	}
	if cms, ok := ctx.Value(commentKey{}).(Comments); ok {
		return cms
	}
	return nil
}

// CommentSource — живой поставщик комментариев (перечитывается перед запросом).
type CommentSource func(ctx context.Context) (Comments, error)

type commentSourceKey struct{}

// WithCommentSource кладёт живой поставщик комментариев в контекст.
func WithCommentSource(ctx context.Context, src CommentSource) context.Context {
	if src == nil {
		return ctx
	}
	return context.WithValue(ctx, commentSourceKey{}, src)
}

// CommentSourceFromContext достаёт живой поставщик комментариев.
func CommentSourceFromContext(ctx context.Context) CommentSource {
	if ctx == nil {
		return nil
	}
	src, _ := ctx.Value(commentSourceKey{}).(CommentSource)
	return src
}

// AllComments отдаёт комментарии для текущего запроса к модели: живой источник
// авторитетен и полностью заменяет снапшот (аналог AllInjections).
func AllComments(ctx context.Context) Comments {
	src := CommentSourceFromContext(ctx)
	if src != nil {
		live, err := src(ctx)
		if err == nil {
			return live
		}
	}
	return CommentsFromContext(ctx)
}
