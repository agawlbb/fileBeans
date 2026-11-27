package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Supply a real sealed-record envelope without requiring a running database.
// Its protection domain is locked, so decoding must stop before decryption.
type lockedPreviewDriver struct{}
type lockedPreviewConn struct{ policy []byte }
type lockedPreviewRows struct {
	columns int
	read    bool
	policy  []byte
}

func init() { sql.Register("locked-preview-policy", lockedPreviewDriver{}) }
func (lockedPreviewDriver) Open(name string) (driver.Conn, error) {
	return lockedPreviewConn{policy: []byte(name)}, nil
}
func (lockedPreviewConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (lockedPreviewConn) Close() error              { return nil }
func (lockedPreviewConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (c lockedPreviewConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	columns := 2
	if strings.Contains(query, "LEFT JOIN") {
		columns = 3
	}
	return &lockedPreviewRows{columns: columns, policy: c.policy}, nil
}
func (r *lockedPreviewRows) Columns() []string {
	if r.columns == 3 {
		return []string{"id", "original", "policy"}
	}
	return []string{"id", "body"}
}
func (*lockedPreviewRows) Close() error { return nil }
func (r *lockedPreviewRows) Next(values []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	values[0] = "locked-link"
	sealed := []byte(`{"vaultRecord":1,"id":"locked-link","sources":["Protected"],"nonce":"","ciphertext":""}`)
	values[1] = sealed
	if r.columns == 3 {
		values[2] = []byte(strings.ReplaceAll(string(sealed), `"id":"locked-link"`, `"id":"policy-locked-link"`))
	}
	if r.columns == 3 && len(r.policy) != 0 {
		values[2] = r.policy
	}
	return nil
}

func TestLockedArchivePolicyKeepsOrdinaryCachedPreviews(t *testing.T) {
	s, base := filesystemFixture(t)
	s.vaults = testVaultService().vaults
	s.vaults.states[0].unlocked = false
	var err error
	s.db, err = sql.Open("locked-preview-policy", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if _, err = s.archivePolicyLinks(); !errors.Is(err, ErrVaultLocked) {
		t.Fatal("fixture must have locked policy", err)
	}
	if err = os.WriteFile(filepath.Join(base, "ordinary.jpg"), []byte("original must not be decoded"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(base, "ordinary.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(base, thumbnailDirectory)
	if err = os.Mkdir(cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(cacheDir, previewCacheName("ordinary.jpg", filesystemRevision(info)))
	if err = os.WriteFile(cache, []byte("existing cached preview"), 0600); err != nil {
		t.Fatal(err)
	}
	listing := fsRequest(t, s, "GET", "resources", "/", nil, nil)
	var entry filesystemEntry
	if err = json.Unmarshal(listing.Body.Bytes(), &entry); listing.Code != 200 || err != nil || len(entry.Children) != 1 || !entry.Children[0].HasPreview {
		t.Fatal("unrelated locked policy hid preview eligibility", listing.Code, listing.Body.String(), err)
	}
	response := fsRequest(t, s, "GET", "thumbnail", "/ordinary.jpg", nil, nil)
	if response.Code != 200 || response.Body.String() != "existing cached preview" {
		t.Fatal("cached preview blocked", response.Code, response.Body.String())
	}
	s.config.Roots = append(s.config.Roots, Root{Name: "Protected", Path: base})
	locked := archiveRequest(s, "GET", "/v1/fs/thumbnail?source=Protected&path=/ordinary.jpg", "")
	if locked.Code != 409 {
		t.Fatal("locked file preview became readable", locked.Code, locked.Body.String())
	}
	if err = os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	response = fsRequest(t, s, "GET", "thumbnail", "/ordinary.jpg", nil, nil)
	if response.Code != 404 {
		t.Fatal("unknown archive membership must remain cache-only", response.Code, response.Body.String())
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("cache miss generated output", entries, err)
	}
	if _, err = s.archiveLinks(); !errors.Is(err, ErrVaultLocked) {
		t.Fatal("protected metadata became readable", err)
	}
	original, err := os.ReadFile(filepath.Join(base, "ordinary.jpg"))
	if err != nil || string(original) != "original must not be decoded" {
		t.Fatal("original changed", err)
	}
}
