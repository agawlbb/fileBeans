package service

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDatasetActivityRequiresUnlockedSession(t *testing.T) {
	b := testBroker(t)
	entry, e := b.reserve("disk", "/", "Private", "synthetic-dataset-activity")
	if e != nil {
		t.Fatal(e)
	}
	call := func(uid uint32) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/dataset-activity", strings.NewReader(`{"source":"disk","path":"/Private/media.mp4"}`))
		req = req.WithContext(context.WithValue(req.Context(), brokerPeer{}, uid))
		w := httptest.NewRecorder()
		b.handler().ServeHTTP(w, req)
		return w
	}
	before := b.datasetActivity
	if w := call(b.config.ControlUID); w.Code != 409 {
		t.Fatal("locked activity", w.Code, w.Body.String())
	}
	if b.datasetActivity != before {
		t.Fatal("locked session extended")
	}
	state := b.manager.find(entry.ID)
	state.unlocked = true
	if w := call(b.config.ReaderUID); w.Code != 403 {
		t.Fatal("reader activity", w.Code)
	}
	if w := call(b.config.ControlUID); w.Code != 200 {
		t.Fatal("unlocked activity", w.Code, w.Body.String())
	}
	if time.Since(b.datasetActivity) > time.Second {
		t.Fatal("activity not recorded")
	}
	state.unlocked = false
}
func TestDatasetActivityExtendsIdleDeadline(t *testing.T) {
	if os.Getenv("FILEBEANS_TEST_FUSE") != "1" {
		t.Skip("isolated FUSE runtime required")
	}
	b := testBroker(t)
	b.config.Gocryptfs = os.Getenv("FILEBEANS_TEST_GOCRYPTFS")
	b.config.Fusermount = "/test-tools/fusermount3"
	b.manager.memoryCheck = func() error { return nil }
	key := []byte(strings.Repeat("k", 32))
	entry, e := b.reserve("disk", "/", "Private", "synthetic-dataset-idle")
	if e != nil {
		t.Fatal(e)
	}
	if e = b.open(entry, key); e != nil {
		t.Fatal(e)
	}
	watches := map[string]*vaultIdleWatch{}
	defer func() {
		for _, w := range watches {
			w.close()
		}
	}()
	start := time.Unix(10000, 0)
	b.pollIdleVaults(watches, start)
	b.datasetActivity = start.Add(50 * time.Minute)
	b.pollIdleVaults(watches, start.Add(65*time.Minute))
	if b.protectionStatus().State != "unlocked" {
		t.Fatal("dataset activity did not reset idle interval")
	}
	b.pollIdleVaults(watches, start.Add(111*time.Minute))
	if b.protectionStatus().State != "locked" {
		t.Fatal("passive idle session did not expire")
	}
}
