// Package owntracks receives location posts from the OwnTracks app in HTTP mode.
package owntracks

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

type Appender interface {
	Append(line []byte) error
}

type logLine struct {
	IngestTime int64           `json:"ingest_time"`
	Body       json.RawMessage `json:"body"`
}

func NewHandler(store Appender, user, pass string, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		u, p, ok := r.BasicAuth()
		res := subtle.ConstantTimeCompare([]byte(u), []byte(user)) & subtle.ConstantTimeCompare([]byte(p), []byte(pass))
		if !ok || res == 0 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 64<<10) // 64 * 1024 = 64 KiB
		body, err := io.ReadAll(r.Body)

		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}

		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if !json.Valid(body) {
			http.Error(w, "bad json body", http.StatusBadRequest)
			return
		}

		line, err := json.Marshal(logLine{IngestTime: now().UnixMicro(), Body: body})
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		err = store.Append(line)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	})
}
