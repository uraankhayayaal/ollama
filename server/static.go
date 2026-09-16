package server

import (
	"net/http"

	"ai/web"
)

// staticHandler раздаёт статику Web UI из embed-файлов (web/dist).
// Сборка фронта: cd web && npm run build.
func staticHandler() http.Handler {
	return http.FileServer(http.FS(web.DistFS()))
}