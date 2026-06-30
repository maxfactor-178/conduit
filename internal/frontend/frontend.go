package frontend

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:web
var webFS embed.FS

// Handler returns an http.Handler that serves the embedded frontend assets
// rooted at the "web" subdirectory so that /index.html is served at /.
func Handler() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic("frontend: embed sub: " + err.Error())
	}
	return http.FileServer(http.FS(sub))
}
