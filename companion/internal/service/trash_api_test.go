package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func testTrashAPI(t *testing.T, s *Service) {
	call := func(route string, value any, permanent bool) *httptest.ResponseRecorder {
		t.Helper()
		var body []byte
		if value != nil {
			body, _ = json.Marshal(value)
		}
		r := httptest.NewRequest("POST", "/v1/"+route, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+s.config.Token)
		r.Header.Set("Idempotency-Key", ID())
		if permanent {
			r.Header.Set("X-Confirm-Permanent", "yes")
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	for _, source := range []string{"vps", "samba"} {
		for _, directory := range []bool{false, true} {
			t.Run(source+map[bool]string{false: "-file", true: "-folder"}[directory], func(t *testing.T) {
				root, e := s.root(source)
				if e != nil {
					t.Fatal(e)
				}
				defer root.Close()
				path := "delete-api-" + ID()
				payloadPath := path
				if directory {
					if e = mkdir(root, filepath.Join(path, "empty")); e != nil {
						t.Fatal(e)
					}
					payloadPath = filepath.Join(path, "data")
				}
				if e = root.WriteFile(payloadPath, []byte("keep contents"), 0600); e != nil {
					t.Fatal(e)
				}
				deleted := call("trash", Item{Source: source, Path: path}, false)
				if deleted.Code != 201 {
					t.Fatal("delete", deleted.Code, deleted.Body.String())
				}
				var entry Trash
				if e = json.Unmarshal(deleted.Body.Bytes(), &entry); e != nil {
					t.Fatal(e)
				}
				if entry.State != "retained" {
					t.Fatal(entry.State)
				}
				for _, filter := range []string{source, "different-source"} {
					request := httptest.NewRequest("GET", "/v1/trash?source="+filter, nil)
					request.Header.Set("Authorization", "Bearer "+s.config.Token)
					response := httptest.NewRecorder()
					s.Handler().ServeHTTP(response, request)
					if response.Code != 200 {
						t.Fatal("trash listing", response.Code, response.Body.String())
					}
					var listed []Trash
					if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
						t.Fatal(err)
					}
					found := false
					for _, item := range listed {
						if item.Source != filter {
							t.Fatal("wrong source in trash")
						}
						if item.ID == entry.ID {
							found = true
						}
					}
					if found != (filter == source) {
						t.Fatal("retained deletion not listed for its source")
					}
				}

				if _, e = root.Stat(path); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("original still visible", e)
				}
				restored := call("trash/"+entry.ID+"/restore", nil, false)
				if restored.Code != 200 {
					t.Fatal("restore", restored.Code, restored.Body.String())
				}
				if data, e := root.ReadFile(payloadPath); e != nil || string(data) != "keep contents" {
					t.Fatal("restore contents", e)
				}
				if directory {
					if _, e = root.Stat(filepath.Join(path, "empty")); e != nil {
						t.Fatal(e)
					}
				}
				deleted = call("trash", Item{Source: source, Path: path}, false)
				if deleted.Code != 201 {
					t.Fatal("repeat delete", deleted.Code, deleted.Body.String())
				}
				if e = json.Unmarshal(deleted.Body.Bytes(), &entry); e != nil {
					t.Fatal(e)
				}
				denied := call("trash/"+entry.ID+"/purge", nil, false)
				if denied.Code != 400 {
					t.Fatal("permanent delete without confirmation accepted")
				}
				purged := call("trash/"+entry.ID+"/purge", nil, true)
				if purged.Code != 200 {
					t.Fatal("purge", purged.Code, purged.Body.String())
				}
				if _, e = root.Stat(trashDirectory(entry)); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("purged data retained", e)
				}
				if e = s.read(entry.ID, "trash", &entry); e != nil || entry.State != "purged" {
					t.Fatal("purge checkpoint", e)
				}
			})
		}
	}
}
