// Package web — собранная статика Web UI (frontend бандл из web/dist),
// встраиваемая в сервер для раздачи через одного процесса.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// DistFS возвращает файловую систему с собранной статикой (web/dist).
// Используется http.FileServer в пакете server.
func DistFS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("web: no embedded dist: " + err.Error())
	}
	return sub
}