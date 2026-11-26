package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

type capacityTestConnector struct {
	driver.Connector
	connect func(context.Context) (driver.Conn, error)
}

func (c capacityTestConnector) Connect(ctx context.Context) (driver.Conn, error) {
	return c.connect(ctx)
}

type capacityTestConn struct{ driver.Conn }

func (capacityTestConn) Close() error { return nil }

func TestCapacityConnectorWaitsForServerSlot(t *testing.T) {
	for _, code := range []uint16{1203, 1226, 1040} {
		t.Run(time.Duration(code).String(), func(t *testing.T) {
			var attempts atomic.Int32
			c := &capacityConnector{Connector: capacityTestConnector{connect: func(context.Context) (driver.Conn, error) {
				if attempts.Add(1) < 3 {
					return nil, &mysql.MySQLError{Number: code}
				}
				return capacityTestConn{}, nil
			}}}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, err := c.Connect(ctx)
			if err != nil || conn == nil || attempts.Load() != 3 {
				t.Fatalf("connection did not recover: attempts=%d err=%v", attempts.Load(), err)
			}
			conn.Close()
		})
	}
}

func TestCapacityConnectorCancellationAndOtherErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &capacityConnector{Connector: capacityTestConnector{connect: func(context.Context) (driver.Conn, error) {
		cancel()
		return nil, &mysql.MySQLError{Number: 1226}
	}}}
	if _, err := c.Connect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	denied := &mysql.MySQLError{Number: 1045}
	c.Connector = capacityTestConnector{connect: func(context.Context) (driver.Conn, error) { return nil, denied }}
	if _, err := c.Connect(context.Background()); err != denied {
		t.Fatalf("authentication error must not retry: %v", err)
	}
}

func TestDatabasePoolQueuesBurstWithoutOpeningExtraConnections(t *testing.T) {
	var opens atomic.Int32
	db := sql.OpenDB(capacityTestConnector{connect: func(context.Context) (driver.Conn, error) {
		opens.Add(1)
		return capacityTestConn{}, nil
	}})
	defer db.Close()
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lease, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			time.Sleep(10 * time.Millisecond)
			conn.Close()
		}()
	}
	workers.Wait()
	if stats := db.Stats(); stats.WaitCount == 0 || stats.InUse != 1 || stats.OpenConnections > 2 {
		t.Fatalf("unexpected pool state after burst: %+v (opens=%d)", stats, opens.Load())
	}
}
