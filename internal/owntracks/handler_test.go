package owntracks

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testUser = "gautam"
	testPass = "s3cret"
)

// fixedNow is 2026-10-20 10:00:00.123456 UTC; its UnixMicro is wantIngest.
var fixedNow = time.Date(2026, 10, 20, 10, 0, 0, 123456000, time.UTC)

const wantIngest = int64(1792490400123456)

// memStore is an in-memory Appender that records every line.
type memStore struct {
	lines [][]byte
	err   error
}

func (m *memStore) Append(line []byte) error {
	if m.err != nil {
		return m.err
	}
	m.lines = append(m.lines, bytes.Clone(line))
	return nil
}

const locationBody = `{"_type":"location","tid":"gp","tst":1792490000,"lat":12.97,"lon":77.59,"acc":12}`

func TestHandler(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		auth      bool
		user      string
		pass      string
		body      string
		storeErr  error
		wantCode  int
		wantLines int
	}{
		{name: "valid post", method: http.MethodPost, auth: true, user: testUser, pass: testPass, body: locationBody, wantCode: 200, wantLines: 1},
		{name: "GET not allowed", method: http.MethodGet, auth: true, user: testUser, pass: testPass, wantCode: 405},
		{name: "no auth", method: http.MethodPost, body: locationBody, wantCode: 401},
		{name: "wrong password", method: http.MethodPost, auth: true, user: testUser, pass: "nope", body: locationBody, wantCode: 401},
		{name: "wrong user", method: http.MethodPost, auth: true, user: "eve", pass: testPass, body: locationBody, wantCode: 401},
		{name: "invalid json", method: http.MethodPost, auth: true, user: testUser, pass: testPass, body: `{"lat":`, wantCode: 400},
		{name: "empty body", method: http.MethodPost, auth: true, user: testUser, pass: testPass, body: "", wantCode: 400},
		{name: "body over 64 KiB", method: http.MethodPost, auth: true, user: testUser, pass: testPass, body: `{"pad":"` + strings.Repeat("x", 64<<10) + `"}`, wantCode: 413},
		{name: "store fails", method: http.MethodPost, auth: true, user: testUser, pass: testPass, body: locationBody, storeErr: errors.New("disk full"), wantCode: 500},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &memStore{err: tc.storeErr}
			h := NewHandler(store, testUser, testPass, func() time.Time { return fixedNow })

			req := httptest.NewRequest(tc.method, "/owntracks/pub", strings.NewReader(tc.body))
			if tc.auth {
				req.SetBasicAuth(tc.user, tc.pass)
			}
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if len(store.lines) != tc.wantLines {
				t.Errorf("stored %d lines, want %d", len(store.lines), tc.wantLines)
			}
		})
	}
}

func TestHandlerSuccessResponse(t *testing.T) {
	h := NewHandler(&memStore{}, testUser, testPass, func() time.Time { return fixedNow })
	req := httptest.NewRequest(http.MethodPost, "/owntracks/pub", strings.NewReader(locationBody))
	req.SetBasicAuth(testUser, testPass)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("response body = %q, want %q", got, "[]")
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

func TestStoreFailureResponse(t *testing.T) {
	store := &memStore{err: errors.New("disk full")}
	h := NewHandler(store, testUser, testPass, func() time.Time { return fixedNow })
	req := httptest.NewRequest(http.MethodPost, "/owntracks/pub", strings.NewReader(locationBody))
	req.SetBasicAuth(testUser, testPass)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	// A failed store must not also send the success body: the phone would
	// read "[]" and believe the point was saved.
	if strings.Contains(rec.Body.String(), "[]") {
		t.Errorf("response body = %q, must not contain the success body []", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "disk full") {
		t.Errorf("response body = %q, must not leak the internal error", rec.Body.String())
	}
}

func TestStoredLine(t *testing.T) {
	// Pretty-printed input: the stored line must still be a single line.
	pretty := "{\n  \"_type\": \"location\",\n  \"lat\": 12.97,\n  \"lon\": 77.59\n}\n"

	store := &memStore{}
	h := NewHandler(store, testUser, testPass, func() time.Time { return fixedNow })
	req := httptest.NewRequest(http.MethodPost, "/owntracks/pub", strings.NewReader(pretty))
	req.SetBasicAuth(testUser, testPass)

	h.ServeHTTP(httptest.NewRecorder(), req)

	if len(store.lines) != 1 {
		t.Fatalf("stored %d lines, want 1", len(store.lines))
	}
	line := store.lines[0]

	if bytes.ContainsAny(line, "\r\n") {
		t.Errorf("stored line contains a newline: %q", line)
	}

	var got struct {
		IngestTime int64           `json:"ingest_time"`
		Body       json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("stored line is not valid JSON: %v\nline: %s", err, line)
	}
	if got.IngestTime != wantIngest {
		t.Errorf("ingest_time = %d, want %d", got.IngestTime, wantIngest)
	}

	var wantBody, gotBody bytes.Buffer
	_ = json.Compact(&wantBody, []byte(pretty))
	_ = json.Compact(&gotBody, got.Body)
	if gotBody.String() != wantBody.String() {
		t.Errorf("body = %s, want %s", gotBody.String(), wantBody.String())
	}
}
