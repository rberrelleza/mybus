package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rberrelleza/mybus/internal/alexa"
	"github.com/rberrelleza/mybus/internal/skill"
	"github.com/rberrelleza/mybus/internal/store"
	"github.com/rberrelleza/mybus/internal/transit"
)

const launch = `{"version":"1.0","context":{"System":{"application":{"applicationId":"amzn1.ask.skill.test"},"user":{"userId":"u1"}}},
"request":{"type":"LaunchRequest","requestId":"r1","timestamp":"2026-11-17T18:00:00Z","locale":"en-US"}}`

func newTestHandler(verify bool) *handler {
	s := &skill.Skill{ID: "amzn1.ask.skill.test", Transit: transit.NewClient("unused"), Store: &store.Memory{}}
	return &handler{skill: s, verifier: &alexa.Verifier{}, verify: verify, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestHandlerServesSkill(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler(false).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(launch)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var resp alexa.ResponseEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Version != "1.0" || resp.Response.OutputSpeech == nil || !strings.HasPrefix(resp.Response.OutputSpeech.SSML, "<speak>") {
		t.Fatalf("unexpected response %s", rec.Body)
	}
}

func TestHandlerRejectsUnsignedRequests(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestHandler(true).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(launch)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandlerRejectsOtherSkill(t *testing.T) {
	rec := httptest.NewRecorder()
	body := strings.Replace(launch, "amzn1.ask.skill.test", "amzn1.ask.skill.other", 1)
	newTestHandler(false).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
