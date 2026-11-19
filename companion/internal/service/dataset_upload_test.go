package service

import (
	"crypto/sha256"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testDatasetUpload(t *testing.T, s *Service) {
	body := "synthetic dataset upload"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	key := ID()
	source := s.config.Roots[0].Name
	call := func(content, digest string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/datasets/uploads?source="+source+"&path=/upload-fixture.txt", strings.NewReader(content))
		req.Header.Set("Authorization", "Bearer "+s.config.Token)
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("X-Content-SHA256", digest)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}
	if w := call(body, hash); w.Code != 201 {
		t.Fatal("upload", w.Code, w.Body.String())
	}
	if w := call(body, hash); w.Code != 200 {
		t.Fatal("idempotent retry", w.Code, w.Body.String())
	}
	if w := call("different", fmt.Sprintf("%x", sha256.Sum256([]byte("different")))); w.Code != 409 {
		t.Fatal("key reused", w.Code)
	}
	data, _ := os.ReadFile(filepath.Join(s.config.Roots[0].Path, "upload-fixture.txt"))
	if string(data) != body {
		t.Fatal("original replaced")
	}
}
