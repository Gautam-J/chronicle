package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gautam-J/chronicle/internal/jsonl"
)

// env builds a getenv function from a map, so tests never touch the real environment.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    config
		wantErr bool
	}{
		{
			name: "defaults",
			env:  map[string]string{"CHRONICLE_OT_USER": "gautam", "CHRONICLE_OT_PASS": "pw"},
			want: config{Addr: "127.0.0.1:8300", DataPath: "data/points.jsonl", User: "gautam", Pass: "pw"},
		},
		{
			name: "overrides",
			env: map[string]string{
				"CHRONICLE_ADDR": "127.0.0.1:9999", "CHRONICLE_DATA": "/mnt/ssd/points.jsonl",
				"CHRONICLE_OT_USER": "gautam", "CHRONICLE_OT_PASS": "pw",
			},
			want: config{Addr: "127.0.0.1:9999", DataPath: "/mnt/ssd/points.jsonl", User: "gautam", Pass: "pw"},
		},
		{
			name:    "missing password",
			env:     map[string]string{"CHRONICLE_OT_USER": "gautam"},
			wantErr: true,
		},
		{
			name:    "missing user",
			env:     map[string]string{"CHRONICLE_OT_PASS": "pw"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := loadConfig(env(tc.env))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("loadConfig() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("loadConfig() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("loadConfig() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// End to end through the real mux and the real file writer.
func TestMuxStoresPoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "points.jsonl")
	store, err := jsonl.Open(path)
	if err != nil {
		t.Fatalf("jsonl.Open: %v", err)
	}
	defer store.Close()

	cfg := config{User: "gautam", Pass: "pw"}
	srv := httptest.NewServer(newMux(store, cfg))
	defer srv.Close()

	body := `{"_type":"location","tid":"gp","tst":1792490000,"lat":12.97,"lon":77.59}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/owntracks/pub", strings.NewReader(body))
	req.SetBasicAuth("gautam", "pw")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("file has %d lines, want 1: %q", len(lines), data)
	}
	var rec struct {
		IngestTime int64           `json:"ingest_time"`
		Body       json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("stored line is not JSON: %v", err)
	}
	if rec.IngestTime == 0 {
		t.Error("ingest_time is 0, want the real clock")
	}
	if string(rec.Body) != body {
		t.Errorf("body = %s, want %s", rec.Body, body)
	}
}

func TestMuxUnknownPath(t *testing.T) {
	srv := httptest.NewServer(newMux(nil, config{User: "u", Pass: "p"}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/nope")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
