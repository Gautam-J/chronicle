// Command chronicle is the Chronicle server.
package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Gautam-J/chronicle/internal/jsonl"
	"github.com/Gautam-J/chronicle/internal/owntracks"
)

type config struct {
	Addr     string
	DataPath string
	User     string
	Pass     string
}

func loadConfig(getenv func(string) string) (config, error) {
	addr := getenv("CHRONICLE_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8300"
	}

	data := getenv("CHRONICLE_DATA")
	if data == "" {
		data = "data/points.jsonl"
	}

	user := getenv("CHRONICLE_OT_USER")
	pass := getenv("CHRONICLE_OT_PASS")

	if user == "" || pass == "" {
		return config{}, errors.New("CHRONICLE_OT_USER and CHRONICLE_OT_PASS must be set")
	}

	return config{Addr: addr, DataPath: data, User: user, Pass: pass}, nil
}

func newMux(store owntracks.Appender, cfg config) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/owntracks/pub", owntracks.NewHandler(store, cfg.User, cfg.Pass, time.Now))
	return mux
}

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	err = os.MkdirAll(filepath.Dir(cfg.DataPath), 0o700)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	w, err := jsonl.Open(cfg.DataPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer w.Close()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           newMux(w, cfg),
		ReadHeaderTimeout: 10 * time.Second,
	}
	err = srv.ListenAndServe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
