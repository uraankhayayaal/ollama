package domain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Типы комментариев задач. Управляет доска (infrastructure/board), но слой
// application ссылается на них при авто-комментариях (эскалации, форсмажоры).
const (
	CommentTypeSystem = "system"
	CommentTypeUser   = "user"
	CommentTypeQA     = "qa"
)

// newCommentID generates a short comment ID.
func newCommentID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(i) + 1
		}
	}
	return "cmt-" + hex.EncodeToString(b)
}

type Comment struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	Author    string    `json:"author"`
	Type      string    `json:"type"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	Accounted bool      `json:"accounted,omitempty"`
}

// Normalize нормализует поля комментария.
func (c *Comment) Normalize() {
	if c.ID == "" {
		c.ID = newCommentID()
	}
	c.Author = strings.TrimSpace(c.Author)
	c.Type = strings.TrimSpace(strings.ToLower(c.Type))
	c.Body = strings.TrimSpace(c.Body)
	if c.Type == "" {
		c.Type = CommentTypeSystem
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
}

// Validate проверяет корректность комментария.
func (c *Comment) Validate() error {
	if c.ID == "" {
		return fmt.Errorf("comment: empty id")
	}
	if c.TaskID == "" {
		return fmt.Errorf("comment: empty task_id")
	}
	if c.Author == "" {
		return fmt.Errorf("comment: empty author")
	}
	switch c.Type {
	case CommentTypeQA, CommentTypeUser, CommentTypeSystem:
	default:
		return fmt.Errorf("comment: unknown type %q", c.Type)
	}
	if c.Body == "" {
		return fmt.Errorf("comment: empty body")
	}
	if c.CreatedAt.IsZero() {
		return fmt.Errorf("comment: zero created_at")
	}
	return nil
}

type Comments []Comment

func (cs Comments) Len() int           { return len(cs) }
func (cs Comments) Swap(i, j int)      { cs[i], cs[j] = cs[j], cs[i] }
func (cs Comments) Less(i, j int) bool { return cs[i].CreatedAt.Before(cs[j].CreatedAt) }

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
