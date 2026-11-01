// Command chronicle is the Chronicle server.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/Gautam-J/chronicle/internal/jsonl"
	chlog "github.com/Gautam-J/chronicle/internal/log"
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

func serve() {
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

func verifyLog(dir string, out io.Writer) error {
	if _, err := os.Stat(dir); err != nil {
		return err
	}

	opts := chlog.Options{}
	l, err := chlog.Open(dir, opts)
	if err != nil {
		return err
	}
	defer l.Close()

	n, err := l.Verify()
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "ok: %d records, hash chain intact\n", n)
	return nil
}

func tailFile(ctx context.Context, path string, out io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}

	defer f.Close()
	_, err = f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}

	err = jsonl.Follow(ctx, f, 500*time.Millisecond, func(line []byte) {
		fmt.Fprintf(out, "%s\n", line)
	})
	return err
}

func displayUsage() {
	fmt.Fprintln(os.Stderr, "usage: chronicle serve | verify <log dir> | tail [path]")
}

func main() {
	if len(os.Args) < 2 {
		displayUsage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		serve()
	case "verify":
		if len(os.Args) != 3 {
			displayUsage()
			os.Exit(2)
		}

		if err := verifyLog(os.Args[2], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "tail":
		path := "data/points.jsonl"
		if v := os.Getenv("CHRONICLE_DATA"); v != "" {
			path = v
		}

		if len(os.Args) > 2 {
			path = os.Args[2]
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()

		if err := tailFile(ctx, path, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		displayUsage()
		os.Exit(2)
	}
}
