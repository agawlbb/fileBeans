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
	syscall.Umask(0007) // New copied files/dirs are group writable without a chmod pass.
	path := flag.String("config", "/etc/filebeans/config.json", "private configuration path")
	normalizer := flag.Bool("normalize-worker", false, "run the serial media normalization worker")
	mediaSocket := flag.String("media-worker", "", "run isolated archive media worker on this Unix socket")
	migrate := flag.Bool("migrate", false, "apply schema and exit")
	printSchema := flag.Bool("schema", false, "print migration SQL and exit without connecting")
	exportState := flag.String("export-source-state", "", "export source Trash metadata to a new private file")
	importState := flag.String("import-source-state", "", "import exported Trash metadata into Pi SQLite")
	handoff := flag.String("handoff-bundle", "", "sealed vault handoff bundle for source ownership")
	preserveArchives := flag.Bool("preserve-archive-links", false, "mirror sealed archive policy while retaining its original coordinator")
	flag.Parse()
	if *mediaSocket != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := service.RunMediaWorker(ctx, *mediaSocket); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *printSchema {
		fmt.Print(service.Schema)
		return
	}
	c, e := service.Load(*path)
	if e != nil {
		log.Fatal("Invalid or unreadable configuration")
	}
	if *exportState != "" || *importState != "" {
		if *handoff == "" || (*exportState != "" && *importState != "") {
			log.Fatal("Specify one state operation and a handoff bundle")
		}
		output := *exportState
		importing := *importState != ""
		if importing {
			output = *importState
		}
		if err := service.MigrationOperations(c, *handoff, output, importing, *preserveArchives); err != nil {
			log.Fatal("Source metadata handoff failed: ", err)
		}
		return
	}
	fingerprint, err := service.EnsureNodeTLS(&c)
	if err != nil {
		log.Fatal("TLS setup failed; existing identity was not replaced")
	}
	if fingerprint != "" {
		log.Printf("Server ID: %s; certificate SHA-256: %s", c.ServerID, fingerprint)
	}
	if *migrate {
		if e = service.MigrateConfig(c); e != nil {
			log.Fatal("Schema migration failed")
		}
		return
	}
	s, e := service.New(c)
	if e != nil {
		log.Fatal("Database or state directory unavailable")
	}
	defer s.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *normalizer {
		if !c.Normalization.Enabled {
			log.Fatal("Normalization is not enabled")
		}
		s.RunNormalization(ctx)
		return
	}
	if e := s.StartFilesystem(ctx); e != nil {
		log.Fatal("Filesystem consumer unavailable; check broker and renderer configuration")
	}
	server := &http.Server{Addr: c.Listen, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: time.Minute, MaxHeaderBytes: 16384}
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	if c.TLS.Certificate != "" {
		e = server.ListenAndServeTLS(c.TLS.Certificate, c.TLS.Key)
	} else {
		e = server.ListenAndServe()
	}
	if e != nil && e != http.ErrServerClosed {
		stop()
		log.Print("HTTP listener stopped unexpectedly")
	}
	stop()
	<-done
}
