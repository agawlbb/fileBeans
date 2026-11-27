package service

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

type TLSConfig struct {
	Certificate string `json:"certificate,omitempty"`
	Key         string `json:"key,omitempty"`
}
type PeerConfig struct {
	ID          string   `json:"id"`
	URL         string   `json:"url"`
	Token       string   `json:"token"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Sources     []string `json:"sources"`
}

var nodeIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

func (c Config) validateNode() error {
	if c.Profile != "" && c.Profile != "pi" {
		return errors.New("unknown server profile")
	}
	if c.SQLite != "" && (c.DSN != "" || !filepath.IsAbs(c.SQLite) || filepath.Dir(c.SQLite) != c.State) {
		return errors.New("SQLite must be in private state and cannot coexist with DSN")
	}
	if c.Profile == "pi" && (c.SQLite == "" || c.Normalization.Enabled || c.ArchiveMediaSocket != "") {
		return errors.New("Pi profile requires local SQLite and disables media workers")
	}
	if (c.TLS.Certificate == "") != (c.TLS.Key == "") {
		return errors.New("configure both TLS certificate and key")
	}
	for _, p := range []string{c.TLS.Certificate, c.TLS.Key} {
		if p != "" && !filepath.IsAbs(p) {
			return errors.New("TLS paths must be absolute")
		}
	}
	if (len(c.Peers) > 0 || c.Profile == "pi") && !nodeIDPattern.MatchString(c.ServerID) {
		return errors.New("configure a stable serverId")
	}
	names := map[string]bool{}
	ids := map[string]bool{c.ServerID: true}
	for _, r := range c.Roots {
		names[r.Name] = true
	}
	for _, p := range c.Peers {
		u, e := url.Parse(p.URL)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !nodeIDPattern.MatchString(p.ID) || ids[p.ID] || len(p.Token) < 32 || len(p.Sources) == 0 {
			return errors.New("invalid peer configuration")
		}
		if p.Fingerprint != "" {
			b, e := hex.DecodeString(p.Fingerprint)
			if e != nil || len(b) != 32 {
				return errors.New("peer certificate fingerprint must be SHA-256 hex")
			}
		}
		ids[p.ID] = true
		for _, n := range p.Sources {
			if n == "" || n == "upload" || names[n] {
				return errors.New("sources must have exactly one owner")
			}
			names[n] = true
		}
	}
	return nil
}

const sqliteSchema = `CREATE TABLE IF NOT EXISTS fb_operations (
 id TEXT PRIMARY KEY, kind TEXT NOT NULL, state TEXT NOT NULL, request_key TEXT UNIQUE,
 body BLOB NOT NULL, updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')));
 CREATE INDEX IF NOT EXISTS operations_kind_state ON fb_operations(kind,state);
 CREATE TRIGGER IF NOT EXISTS operations_updated AFTER UPDATE ON fb_operations BEGIN
 UPDATE fb_operations SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE id=NEW.id; END;`

func openSQLite(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	f.Close()
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	d, e := sql.Open("sqlite", u.String())
	if e != nil {
		return nil, e
	}
	d.SetMaxOpenConns(1)
	if e = d.Ping(); e != nil {
		d.Close()
		return nil, e
	}
	return d, nil
}

func (s *Service) sqlDialect(query string) string {
	if s.config.SQLite == "" {
		return query
	}
	query = strings.ReplaceAll(query, " FOR UPDATE", "")
	query = strings.ReplaceAll(query, "CONCAT('policy-',l.id)", "('policy-' || l.id)")
	query = strings.ReplaceAll(query, "ON DUPLICATE KEY UPDATE body=VALUES(body)", "ON CONFLICT(id) DO UPDATE SET body=excluded.body")
	return query
}

var peerClients sync.Map

func peerClient(p PeerConfig) *http.Client {
	raw, _ := json.Marshal(p)
	key := sha256.Sum256(raw)
	if cached, ok := peerClients.Load(key); ok {
		return cached.(*http.Client)
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	if p.Fingerprint != "" {
		t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: func(c tls.ConnectionState) error {
			if len(c.PeerCertificates) == 0 {
				return errors.New("missing peer certificate")
			}
			sum := sha256.Sum256(c.PeerCertificates[0].Raw)
			if !strings.EqualFold(hex.EncodeToString(sum[:]), p.Fingerprint) {
				return errors.New("peer certificate changed")
			}
			cert := c.PeerCertificates[0]
			now := time.Now()
			if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
				return x509.CertificateInvalidError{Cert: cert, Reason: x509.Expired}
			}
			return nil
		}}
	}
	t.ResponseHeaderTimeout = 15 * time.Second
	client := &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cached, loaded := peerClients.LoadOrStore(key, client)
	if loaded {
		t.CloseIdleConnections()
	}
	return cached.(*http.Client)
}

// SQLite has one process owner. A local OS lock replaces MySQL advisory leases.
func (s *Service) runSQLite(ctx context.Context) {
	f, e := os.OpenFile(filepath.Join(s.config.State, "worker.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return
	}
	s.loop(ctx, nil)
}
