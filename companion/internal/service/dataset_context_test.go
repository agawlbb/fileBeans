package service

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func testDatasetProtectedActivity(t *testing.T, s *Service) {
	previous := s.vaults
	s.vaults = testVaultService().vaults
	defer func() { s.vaults = previous }()
	job := DatasetJob{ID: ID(), State: "ready", Request: DatasetRequest{Operation: "prepare", Model: "qwen", Inputs: []DatasetInput{{ID: ID(), Source: "Protected", Path: "/hidden/source.png", Revision: strings.Repeat("a", 64)}}}}
	if e := s.insert(job.ID, "dataset", job.State, ID(), job); e != nil {
		t.Fatal(e)
	}
	defer s.db.Exec("DELETE FROM fb_operations WHERE id=?", job.ID)
	call := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal([]map[string]string{{"source": "Protected", "path": "/hidden/source.png", "job": job.ID}})
		req := httptest.NewRequest("POST", "/v1/datasets/activity", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+s.config.Token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}
	if w := call(); w.Code != 200 {
		t.Fatal("unlocked context", w.Code, w.Body.String())
	}
	s.vaults.states[0].unlocked = false
	if w := call(); w.Code != 423 || !strings.Contains(w.Body.String(), "FileBeans mobile app") {
		t.Fatal("hidden locked context", w.Code, w.Body.String())
	}
	s.vaults.states[0].unlocked = true
}
