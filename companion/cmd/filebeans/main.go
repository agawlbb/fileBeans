package main

import (
	"context"
	"filebeans/companion/internal/service"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	path := flag.String("config", "/etc/filebeans/config.json", "private configuration path")
	migrate := flag.Bool("migrate", false, "apply schema and exit")
	printSchema := flag.Bool("schema", false, "print migration SQL and exit without connecting")
	flag.Parse()
	if *printSchema {
		fmt.Print(service.Schema)
		return
	}
	c, e := service.Load(*path)
	if e != nil {
		log.Fatal("Invalid or unreadable configuration")
	}
	s, e := service.New(c)
	if e != nil {
		log.Fatal("Database or state directory unavailable")
	}
	defer s.Close()
	if *migrate {
		if e = s.Migrate(); e != nil {
			log.Fatal("Schema migration failed")
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: c.Listen, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: time.Minute, MaxHeaderBytes: 16384}
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	if e = server.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		stop()
		log.Print("HTTP listener stopped unexpectedly")
	}
	stop()
	<-done
}
