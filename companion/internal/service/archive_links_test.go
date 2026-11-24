package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func archiveRequest(s *Service, method, path, payload string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(payload))
	r.Header.Set("Authorization", "Bearer "+s.config.Token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func testArchiveDatabase(t *testing.T, s *Service) {
	previous := s.config.Filesystem
	s.config.Filesystem.Enabled = true
	defer func() { s.config.Filesystem = previous }()
	defer s.db.Exec("DELETE FROM fb_operations WHERE kind='archiveLink'")
	for _, root := range s.config.Roots {
		for _, p := range []string{"archive-test/library/sub/deep", "archive-test/archive/sub/deep", "archive-test/other"} {
			if err := os.MkdirAll(filepath.Join(root.Path, p), 0700); err != nil {
				t.Fatal(err)
			}
		}
		defer os.RemoveAll(filepath.Join(root.Path, "archive-test"))
	}
	makeLink := func(library, archive, source string) *httptest.ResponseRecorder {
		return archiveRequest(s, "POST", "/v1/archive-links", `{"library":{"source":"vps","path":"`+library+`"},"archive":{"source":"`+source+`","path":"`+archive+`"}}`)
	}
	w := makeLink("/archive-test/library", "/archive-test/archive", "samba")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var link ArchiveLink
	if err := json.Unmarshal(w.Body.Bytes(), &link); err != nil {
		t.Fatal(err)
	}
	if w = makeLink("/archive-test/library", "/archive-test/archive", "samba"); w.Code != 200 {
		t.Fatal("retry", w.Code, w.Body.String())
	}
	for _, tc := range [][3]string{{"/archive-test/library/sub", "/archive-test/other", "samba"}, {"/archive-test/other", "/archive-test/archive/sub", "samba"}, {"/archive-test/library", "/archive-test/other", "vps"}} {
		if w = makeLink(tc[0], tc[1], tc[2]); w.Code != 409 {
			t.Fatal("overlap", w.Code, w.Body.String())
		}
	}
	os.MkdirAll(filepath.Join(s.config.Roots[1].Path, "archive-test/archive/sub/future"), 0700)
	os.WriteFile(filepath.Join(s.config.Roots[1].Path, "archive-test/archive/sub/future/photo.jpg"), []byte("original"), 0600)
	restarted := &Service{db: s.db, config: s.config, vaults: s.vaults}
	listing := fsRequest(t, restarted, "GET", "resources", "/archive-test/archive/sub", nil, nil)
	if listing.Code != 200 || !strings.Contains(listing.Body.String(), `"archiveRole":"archive"`) {
		t.Fatal(listing.Code, listing.Body.String())
	}
	listing = fsRequest(t, restarted, "GET", "resources", "/archive-test/archive/sub/future", nil, nil)
	var entry filesystemEntry
	if err := json.Unmarshal(listing.Body.Bytes(), &entry); err != nil || len(entry.Children) != 1 || entry.Children[0].HasPreview || entry.Children[0].ArchiveRole != "archive" {
		t.Fatal("future subtree preview eligibility", listing.Body.String(), err)
	}
	opened := fsRequest(t, restarted, "GET", "content", "/archive-test/archive/sub/future/photo.jpg", nil, nil)
	if opened.Code != 200 || opened.Body.String() != "original" {
		t.Fatal("explicit viewing blocked", opened.Code, opened.Body.String())
	}
	// Even a nonexistent file must be rejected before opening media or running tools.
	thumb := fsRequest(t, restarted, "GET", "thumbnail", "/archive-test/archive/sub/deep/not-opened.mp4", nil, nil)
	if thumb.Code != 415 {
		t.Fatal("thumbnail reached storage", thumb.Code, thumb.Body.String())
	}
	if _, err := s.trash(Item{Source: "samba", Path: "archive-test/archive"}, ID()); !errors.Is(err, ErrArchiveLinked) {
		t.Fatal("delete root", err)
	}
	if _, err := s.trash(Item{Source: "samba", Path: "archive-test"}, ID()); !errors.Is(err, ErrArchiveLinked) {
		t.Fatal("delete ancestor", err)
	}
	move := `{"source":"samba","from":"/archive-test/archive","to":"/archive-test/renamed"}`
	if w = fsRequest(t, s, "POST", "move", "/", strings.NewReader(move), nil); w.Code != 204 {
		t.Fatal("rename", w.Code, w.Body.String())
	}
	links, err := s.resolvedArchiveLinks()
	if err != nil || len(links) != 1 || links[0].Archive.Path != "/archive-test/renamed" {
		t.Fatal(links, err)
	}
	if err = os.Rename(filepath.Join(s.config.Roots[1].Path, "archive-test/renamed"), filepath.Join(s.config.Roots[1].Path, "archive-test/external")); err != nil {
		t.Fatal(err)
	}
	links, err = s.resolvedArchiveLinks()
	if err != nil || links[0].Archive.Available {
		t.Fatal("external move guessed", links, err)
	}
	if w = archiveRequest(s, "DELETE", "/v1/archive-links/"+link.ID, ""); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err = os.Stat(filepath.Join(s.config.Roots[1].Path, "archive-test/external")); err != nil {
		t.Fatal("unlink changed data", err)
	}
	// Same-source disjoint folders are valid; ancestors of an endpoint are not.
	w = makeLink("/archive-test/library", "/archive-test/other", "vps")
	if w.Code != 201 {
		t.Fatal("same source", w.Code, w.Body.String())
	}
	s.db.Exec("DELETE FROM fb_operations WHERE kind='archiveLink'")
	base := s.config.Roots[1].Path
	os.MkdirAll(filepath.Join(base, "archive-test/archive/sub"), 0700)
	os.WriteFile(filepath.Join(base, "archive-test/archive/sub/a.jpg"), []byte("original"), 0600)
	os.WriteFile(filepath.Join(base, "archive-test/archive/sub/A.mov"), []byte("original"), 0600)
	if w = makeLink("/archive-test/library", "/archive-test/archive", "samba"); w.Code != 409 {
		t.Fatal("existing name conflict", w.Code, w.Body.String())
	}
	// Both endpoints protected: metadata is sealed and vault-relative locations
	// follow broker relocation while locking never loses the relationship.
	s.db.Exec("DELETE FROM fb_operations WHERE kind='archiveLink'")
	os.Remove(filepath.Join(base, "archive-test/archive/sub/A.mov"))
	previousVaults := s.vaults
	s.vaults = testVaultService().vaults
	defer func() { s.vaults = previousVaults }()
	s.vaults.states[0].config.Source = "vps"
	s.vaults.states[0].config.Path = "/archive-test/library"
	s.vaults.states[1].config.Source = "samba"
	s.vaults.states[1].config.Path = "/archive-test/archive"
	w = makeLink("/archive-test/library/sub", "/archive-test/archive/sub", "samba")
	if w.Code != 201 {
		t.Fatal("protected pair", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &link)
	var stored []byte
	if err = s.db.QueryRow("SELECT body FROM fb_operations WHERE id=?", link.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("archive-test")) || bytes.Contains(stored, []byte("/sub")) {
		t.Fatal("protected locations leaked")
	}
	if err = os.Rename(filepath.Join(base, "archive-test/archive"), filepath.Join(base, "archive-test/relocated")); err != nil {
		t.Fatal(err)
	}
	s.vaults.states[1].config.Path = "/archive-test/relocated"
	links, err = s.resolvedArchiveLinks()
	if err != nil || len(links) != 1 || links[0].Archive.Path != "/archive-test/relocated/sub" || !links[0].Archive.Available {
		t.Fatal(links, err)
	}
	s.vaults.states[1].unlocked = false
	if w = archiveRequest(s, "GET", "/v1/archive-links", ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal("locked locations leaked", w.Code, w.Body.String())
	}
	if err = s.guardArchiveRemoval("vps", "archive-test/library"); !errors.Is(err, ErrVaultLocked) {
		t.Fatal("locked link deletion allowed", err)
	}
	s.vaults.states[1].unlocked = true
	if w = archiveRequest(s, "DELETE", "/v1/archive-links/"+link.ID, ""); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}

}

func TestArchiveRecordProtection(t *testing.T) {
	s := testVaultService()
	link := ArchiveLink{ID: ID(), Library: ArchiveEndpoint{Source: "Protected", Path: "/secret/library"}, Archive: ArchiveEndpoint{Source: "Protected", Path: "/secret/archive"}}
	data, _ := json.Marshal(link)
	// Match the existing protected-record fixture's source label.
	configs := s.vaults.configs()
	if len(configs) == 0 {
		t.Fatal("missing protected fixture")
	}
	link.Library.Source = configs[0].Source
	link.Archive.Source = configs[0].Source
	data, _ = json.Marshal(link)
	encoded, err := s.encodeRecord(link.ID, "archiveLink", data)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("secret")) {
		t.Fatal("plaintext archive paths")
	}
	decoded, err := s.decodeRecord(link.ID, "archiveLink", encoded)
	if err != nil || !bytes.Equal(decoded, data) {
		t.Fatal(err)
	}
}
func TestArchiveMembershipBoundaries(t *testing.T) {
	links := []ArchiveLink{{ID: "one", Library: ArchiveEndpoint{Source: "same", Path: "/library"}, Archive: ArchiveEndpoint{Source: "same", Path: "/archive"}}}
	for _, tc := range []struct {
		p, role string
		root    bool
	}{{"/archive", "archive", true}, {"/archive/sub/a.mov", "archive", false}, {"/archive-old/a.mov", "", false}, {"/library/sub/a.mp4", "library", false}} {
		_, role, root := archiveMembership(links, "same", tc.p)
		if role != tc.role || root != tc.root {
			t.Fatal(tc, role, root)
		}
	}
}
func TestFilenamePolicyPublication(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"Photo.MOV", "café.jpg", ".env", "trip.tar.gz"} {
		if err = root.WriteFile(name, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []string{"photo.mp4", "cafe\u0301.PNG", ".ENV.txt", "trip.tar.zip"} {
		if err = filenameConflict(root, target, "", false); !errors.Is(err, ErrConflict) {
			t.Fatal(target, err)
		}
	}
	if filenameKey(".env") != ".env" || filenameKey("no-extension") != "no-extension" || filenameKey("Straße.JPG") != filenameKey("STRASSE.MOV") {
		t.Fatal("normalization")
	}
	if err = filenameConflict(root, "Photo.MOV", "Photo.MOV", false); err != nil {
		t.Fatal("replace", err)
	}
	root.MkdirAll("sub", 0700)
	if err = filenameConflict(root, "sub/photo.mp4", "", false); err != nil {
		t.Fatal(err)
	}
	root.WriteFile("incoming", []byte("new"), 0600)
	if err = renameExclusive(root, "incoming", "photo.mp4"); !errors.Is(err, ErrConflict) {
		t.Fatal("publication", err)
	}
	root.MkdirAll(".filebeans-stage/folder", 0700)
	root.WriteFile(".filebeans-stage/folder/x.jpg", nil, 0600)
	root.WriteFile(".filebeans-stage/folder/X.mp4", nil, 0600)
	if err = renameExclusive(root, ".filebeans-stage/folder", "folder"); !errors.Is(err, ErrConflict) {
		t.Fatal("nested conflict", err)
	}
}
func TestFilenamePolicyConcurrentPublication(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	root.MkdirAll(".filebeans-stage", 0700)
	for _, name := range []string{"one", "two"} {
		root.WriteFile(".filebeans-stage/"+name, []byte("new"), 0600)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, tc := range [][2]string{{"one", "same.jpg"}, {"two", "SAME.mov"}} {
		wg.Add(1)
		go func(tc [2]string) {
			defer wg.Done()
			results <- renameExclusive(root, ".filebeans-stage/"+tc[0], tc[1])
		}(tc)
	}
	wg.Wait()
	close(results)
	passed, failed := 0, 0
	for err := range results {
		if err == nil {
			passed++
		} else if errors.Is(err, ErrConflict) {
			failed++
		} else {
			t.Fatal(err)
		}
	}
	if passed != 1 || failed != 1 {
		t.Fatal(passed, failed)
	}
}

func TestFilesystemFilenameStemConflicts(t *testing.T) {
	s, base := filesystemFixture(t)
	if w := fsRequest(t, s, "POST", "content", "/Photo.MOV", strings.NewReader("keep"), nil); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := fsRequest(t, s, "POST", "content", "/photo.mp4", strings.NewReader("conflict"), nil); w.Code != 409 {
		t.Fatal("upload conflict", w.Code, w.Body.String())
	}
	os.WriteFile(filepath.Join(base, "other.jpg"), []byte("other"), 0600)
	if w := fsRequest(t, s, "POST", "move", "/", strings.NewReader(`{"source":"samba","from":"/other.jpg","to":"/PHOTO.png"}`), nil); w.Code != 409 {
		t.Fatal("rename conflict", w.Code, w.Body.String())
	}
	route := "uploads/12345678-1234-1234-1234-123456789abc"
	if w := fsRequest(t, s, "POST", route, "/photo.mp4", strings.NewReader(`{"size":0}`), nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := fsRequest(t, s, "POST", route+"/complete", "/photo.mp4", nil, nil); w.Code != 409 {
		t.Fatal("resumable conflict", w.Code, w.Body.String())
	}
	source, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	source.WriteFile("retained", []byte("original"), 0600)
	if err = renameRoots(source, "retained", destination, "photo.jpg"); !errors.Is(err, ErrConflict) {
		t.Fatal("restore conflict", err)
	}
	if data, err := source.ReadFile("retained"); err != nil || string(data) != "original" {
		t.Fatal("retained payload lost", err)
	}
	if err = validateManifestFilenames([]File{{To: "sub/a.jpg"}, {To: "sub/A.mov"}}); !errors.Is(err, ErrConflict) {
		t.Fatal("batch conflict", err)
	}
	if err = validateManifestFilenames([]File{{To: "one/a.jpg"}, {To: "two/a.mov"}}); err != nil {
		t.Fatal("different directories", err)
	}
}

func TestGeneratedDatasetCaptionExceptionIsNarrow(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	root.MkdirAll(".filebeans-stage/dataset", 0700)
	root.WriteFile(".filebeans-stage/dataset/1.jpg", []byte("image"), 0600)
	root.WriteFile(".filebeans-stage/dataset/1.txt", []byte("caption"), 0600)
	if err = validateFilenameTree(root, ".filebeans-stage/dataset"); !errors.Is(err, ErrConflict) {
		t.Fatal("ordinary policy weakened", err)
	}
	if err = renameExclusive(root, ".filebeans-stage/dataset", "dataset", true); err != nil {
		t.Fatal("generated caption pair blocked", err)
	}
	if err = filenameConflict(root, "dataset/1.mp4", "", false); !errors.Is(err, ErrConflict) {
		t.Fatal("upload exception leaked", err)
	}
	root.WriteFile("dataset/1.png", []byte("extra media"), 0600)
	if err = validateFilenameTree(root, "dataset", true); !errors.Is(err, ErrConflict) {
		t.Fatal("multiple media files allowed", err)
	}
}
