package board

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
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

// Comment types.
const (
	CommentTypeQA     = "qa"
	CommentTypeUser   = "user"
	CommentTypeSystem = "system"
)

// Comment represents a task comment left by author (qa/user/system).
type Comment struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	Author    string    `json:"author"`
	Type      string    `json:"type"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// Normalize normalizes comment fields.
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

// Validate checks comment validity.
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

// Comments is a slice of comments.
type Comments []Comment

func (cs Comments) Len() int           { return len(cs) }
func (cs Comments) Swap(i, j int)      { cs[i], cs[j] = cs[j], cs[i] }
func (cs Comments) Less(i, j int) bool { return cs[i].CreatedAt.Before(cs[j].CreatedAt) }

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
