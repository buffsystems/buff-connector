package catalog

import (
	"encoding/base64"
	"net/http"
)

func fileServer(files map[string][]byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	})
}

func base64Std(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
