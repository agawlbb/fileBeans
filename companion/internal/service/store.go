package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"encoding/json"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
)

//go:embed schema.sql
var Schema string

type Item struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	Name   string `json:"name,omitempty"`
}
type CopyRequest struct {
	Operation       string `json:"operation,omitempty"`
	DestinationName string `json:"destinationName,omitempty"`
	Items           []Item `json:"items"`
	Destination     Item   `json:"destination"`
}
type File struct {
	Vault     string `json:"vault,omitempty"`
	DriveID   string `json:"driveId,omitempty"`
	From      string `json:"from"`
	To        string `json:"to"`
	Size      int64  `json:"size"`
	Version   string `json:"version"`
	Done      int64  `json:"done"`
	Directory bool   `json:"directory"`
	Source    string `json:"source"`
	Hash      string `json:"hash,omitempty"`
	Mime      string `json:"mime,omitempty"`
	TargetID  string `json:"targetId,omitempty"`
	Session   string `json:"session,omitempty"`
	Completed bool   `json:"completed,omitempty"`
	Attempted bool   `json:"attempted,omitempty"`
}
type Job struct {
	PeerPlan     []PeerSelection `json:"peerPlan,omitempty"`
	Move         *MoveState      `json:"move,omitempty"`
	CanCancel    *bool           `json:"canCancel,omitempty"`
	RclonePlan   []File          `json:"rclonePlan,omitempty"`
	Engine       string          `json:"engine,omitempty"`
	ID           string          `json:"id"`
	State        string          `json:"state"`
	Request      CopyRequest     `json:"request"`
	Manifest     []File          `json:"manifest,omitempty"`
	Connection   string          `json:"connection,omitempty"`
	Total        int64           `json:"total"`
	Done         int64           `json:"done"`
	Speed        float64         `json:"speed"`
	Current      string          `json:"current"`
	Error        string          `json:"error,omitempty"`
	Attempts     int             `json:"attempts"`
	Next         int64           `json:"next"`
	Cancel       bool            `json:"cancel"`
	Published    []string        `json:"published,omitempty"`
	RemoteStage  string          `json:"remoteStage,omitempty"`
	ProgressUnit string          `json:"progressUnit,omitempty"`
	ItemsTotal   int             `json:"itemsTotal,omitempty"`
	ItemsDone    int             `json:"itemsDone,omitempty"`
}
type Trash struct {
	Vault         string `json:"vault,omitempty"`
	LegacyStorage string `json:"legacyStorage,omitempty"`
	Storage       string `json:"storage,omitempty"`
	ID            string `json:"id"`
	Source        string `json:"source"`
	Original      string `json:"original"`
	Name          string `json:"name"`
	Deleted       int64  `json:"deleted"`
	Expires       int64  `json:"expires"`
	State         string `json:"state"`
	Error         string `json:"error,omitempty"`
}

func openDB(dsn string) (*sql.DB, error) {
	cfg, e := mysql.ParseDSN(dsn)
	if e != nil {
		return nil, e
	}
	connector, e := mysql.NewConnector(cfg)
	if e != nil {
		return nil, e
	}
	d := sql.OpenDB(&capacityConnector{Connector: connector})
	d.SetMaxOpenConns(5)
	// Each worker process reserves a lease connection; queries share the rest.
	// Keep one warm query connection, releasing spare burst capacity promptly.
	d.SetMaxIdleConns(1)
	d.SetConnMaxIdleTime(30 * time.Second)
	d.SetConnMaxLifetime(time.Minute * 3)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e = d.PingContext(ctx)
	if e != nil {
		d.Close()
		return nil, e
	}
	return d, nil
}

// Connection limits are temporary capacity pressure. Wait for a slot rather
// than returning the server's account-limit error to an operation. Only retry
// opening connections: replaying SQL could duplicate a completed mutation.
type capacityConnector struct {
	driver.Connector
}

func (c *capacityConnector) Connect(ctx context.Context) (driver.Conn, error) {
	for {
		conn, err := c.Connector.Connect(ctx)
		var mysqlErr *mysql.MySQLError
		if !errors.As(err, &mysqlErr) || (mysqlErr.Number != 1203 && mysqlErr.Number != 1226 && mysqlErr.Number != 1040) {
			return conn, err
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
func (s *Service) save(id, kind, state string, v any) error {
	b, e := json.Marshal(v)
	if e == nil {
		b, e = s.encodeRecord(id, kind, b)
	}
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, e = s.db.ExecContext(ctx, "UPDATE fb_operations SET state=?,body=? WHERE id=? AND kind=?", state, b, id, kind)
	return e
}
func (s *Service) insert(id, kind, state, key string, v any) error {
	b, e := json.Marshal(v)
	if e == nil {
		b, e = s.encodeRecord(id, kind, b)
	}
	if e != nil {
		return e
	}
	var k any
	if key != "" {
		k = key
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, e = s.db.ExecContext(ctx, "INSERT INTO fb_operations(id,kind,state,request_key,body) VALUES(?,?,?,?,?)", id, kind, state, k, b)
	return e
}
func (s *Service) read(id, kind string, v any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var b []byte
	e := s.db.QueryRowContext(ctx, "SELECT body FROM fb_operations WHERE id=? AND kind=?", id, kind).Scan(&b)
	if e != nil {
		return e
	}
	b, e = s.decodeRecord(id, kind, b)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func (s *Service) records(kind string) ([][]byte, error) { return s.operationRecords(kind, false) }
func (s *Service) operationRecords(kind string, active bool) ([][]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	query := "SELECT id,body FROM fb_operations WHERE kind=?"
	if active {
		query += " AND state IN ('queued','preparing','running','discovering','waiting')"
	}
	query += " ORDER BY updated_at DESC"
	if s.config.Profile == "pi" {
		query += " LIMIT 100"
	}
	rows, e := s.db.QueryContext(ctx, query, kind)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var b []byte
		var id string
		if e = rows.Scan(&id, &b); e != nil {
			return nil, e
		}
		b, e = s.decodeRecord(id, kind, b)
		if errors.Is(e, ErrVaultLocked) {
			continue
		}
		if e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

var ErrConflict = errors.New("destination exists or source changed")
var ErrUnsafe = errors.New("operation is outside allowed storage or uses an unsafe path")
var ErrUnsupported = errors.New("unsupported Drive item or download forbidden")
var ErrMount = errors.New("configured network mount is unavailable")
var ErrAuthorization = errors.New("Google Drive authorization required")

var ErrDriveWrite = errors.New("Google Drive full-access consent required")
var ErrDriveAccount = errors.New("Google Drive connection changed")
var ErrDrivePermission = errors.New("Google Drive does not permit this operation")
var ErrUncertain = errors.New("Google has not confirmed this operation")
