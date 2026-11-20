package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemResumableUpload(t *testing.T) {
	s, base := filesystemFixture(t)
	id := "12345678-1234-1234-1234-123456789abc"
	route := "uploads/" + id
	check := func(method, route, data string, headers map[string]string, code int) string {
		t.Helper()
		got := fsRequest(t, s, method, route, "/movie.mp4", strings.NewReader(data), headers)
		if got.Code != code {
			t.Fatalf("%s %s: %d %s", method, route, got.Code, got.Body.String())
		}
		return got.Body.String()
	}
	check("POST", route, `{"size":6}`, nil, 200)
	check("POST", route, `{"size":7}`, nil, 409)
	check("PUT", route, "abc", map[string]string{"Content-Range": "bytes 0-2/6"}, 200)
	check("PUT", route, "abc", map[string]string{"Content-Range": "bytes 0-2/6"}, 409)
	check("PUT", route, "d", map[string]string{"Content-Range": "bytes 3-5/6"}, 400)
	// Recreate the service to prove there is no in-memory offset dependency.
	s2, _ := filesystemFixture(t)
	s2.config = s.config
	s = s2
	if !strings.Contains(check("GET", route, "", nil, 200), `"received":3`) {
		t.Fatal("offset not recovered")
	}
	check("POST", route+"/complete", "", nil, 409)
	check("PUT", route, "def", map[string]string{"Content-Range": "bytes 3-5/6"}, 200)
	check("POST", route+"/complete", "", nil, 200)
	check("POST", route+"/complete", "", nil, 200)
	check("DELETE", route, "", nil, 204)
	data, e := os.ReadFile(filepath.Join(base, "movie.mp4"))
	if e != nil || string(data) != "abcdef" {
		t.Fatal(string(data), e)
	}
}
func TestFilesystemUploadPublicationCollision(t *testing.T) {
	s, base := filesystemFixture(t)
	route := "uploads/12345678-1234-1234-1234-123456789abc"
	got := fsRequest(t, s, "POST", route, "/empty", strings.NewReader(`{"size":0}`), nil)
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	if e := os.WriteFile(filepath.Join(base, "empty"), []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	got = fsRequest(t, s, "POST", route+"/complete", "/empty", nil, nil)
	if got.Code != 409 {
		t.Fatal(got.Code, got.Body.String())
	}
	data, _ := os.ReadFile(filepath.Join(base, "empty"))
	if string(data) != "keep" {
		t.Fatal("replaced existing file")
	}
}
