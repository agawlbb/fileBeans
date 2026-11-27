package service

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testMediaWorker(t *testing.T, worker *Service) *Service {
	t.Helper()
	// Linux Unix-domain socket names have a short length ceiling.
	dir, err := os.MkdirTemp("", "media-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "worker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.serveMedia(ctx, socket) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		os.RemoveAll(dir)
	})
	for i := 0; i < 100; i++ {
		conn, err := net.Dial("unixpacket", socket)
		if err == nil {
			conn.Close()
			return &Service{config: Config{ArchiveMediaSocket: socket}}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker socket not ready")
	return nil
}

func healthyMediaWorker() *Service {
	s := &Service{mediaWorker: true}
	s.archiveMemory.required = mediaWorkerDataLimit
	s.archiveMemory.read = func() (archiveMemorySample, error) {
		return archiveMemorySample{available: 4 << 30, limit: 1 << 30, used: 64 << 20}, nil
	}
	return s
}

func TestMediaWorkerSocketRecovery(t *testing.T) {
	dir, err := os.MkdirTemp("", "media-restart-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "media.sock")
	stale, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: socket, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	stale.Close()
	client := &Service{config: Config{ArchiveMediaSocket: socket}}
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- healthyMediaWorker().serveMedia(ctx, socket) }()
		ready := false
		for i := 0; i < 100; i++ {
			if client.archiveCanStart(ctx, false) {
				ready = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if !ready {
			t.Fatal("worker did not recover socket")
		}
		if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("socket retained after shutdown", err)
		}
	}
}

func TestMediaWorkerCapabilitiesAndRestart(t *testing.T) {
	worker := healthyMediaWorker()
	client := testMediaWorker(t, worker)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !client.archiveCanStart(ctx, false) {
		t.Fatal("1 GiB worker must admit healthy work")
	}
	if _, err := client.mediaCall(ctx, mediaRequest{Operation: "shell"}); err == nil {
		t.Fatal("arbitrary operation accepted")
	}
	if err := worker.serveMedia(ctx, client.config.ArchiveMediaSocket); err == nil {
		t.Fatal("duplicate executor accepted")
	}
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "original")
	if err := os.WriteFile(inputPath, []byte("animated original"), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	// Rename after opening: worker must use the capability, not reopen a path.
	if err = os.Rename(inputPath, inputPath+".renamed"); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	plan := mediaPlan{Image: true, Copy: true, Extension: ".apng"}
	if err = client.encodeArchiveMedia(ctx, input, directory, plan, 1024); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "output.apng"))
	if err != nil || string(data) != "animated original" {
		t.Fatalf("descriptor copy: %q %v", data, err)
	}
	plan.Extension = "/../../escape"
	if err = client.encodeArchiveMedia(ctx, input, directory, plan, 1024); err == nil {
		t.Fatal("invalid extension accepted")
	}
	plan.Extension = ".gif"
	if err = client.encodeArchiveMedia(ctx, input, directory, plan, 1); !errors.Is(err, errNormalizationSpace) {
		t.Fatalf("expected bounded output failure: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "output.gif")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial output retained", err)
	}
	// A disconnected executor must never trigger local fallback.
	client.config.ArchiveMediaSocket += ".missing"
	if err = client.encodeArchiveMedia(ctx, input, directory, plan, 1024); err == nil {
		t.Fatal("missing worker accepted")
	}
}

func TestMediaWorkerMemoryDeferral(t *testing.T) {
	worker := healthyMediaWorker()
	worker.archiveMemory.read = func() (archiveMemorySample, error) {
		return archiveMemorySample{available: 4 << 30, limit: 1 << 30, used: 500 << 20}, nil
	}
	client := testMediaWorker(t, worker)
	if client.archiveCanStart(context.Background(), false) {
		t.Fatal("worker exceeded admission budget")
	}
	worker.archiveMemory.mu.Lock()
	// Advance the synthetic recovery clock without a 30-second test sleep.
	worker.archiveMemory.read = func() (archiveMemorySample, error) {
		return archiveMemorySample{available: 4 << 30, limit: 1 << 30, used: 64 << 20}, nil
	}
	worker.archiveMemory.mu.Unlock()
	if client.archiveCanStart(context.Background(), false) {
		t.Fatal("memory recovery skipped cooldown")
	}
	worker.archiveMemory.mu.Lock()
	worker.archiveMemory.healthy = time.Now().Add(-archiveMemoryRecovery)
	worker.archiveMemory.mu.Unlock()
	if !client.archiveCanStart(context.Background(), false) {
		t.Fatal("worker did not recover")
	}
}

func TestMediaWorkerDisconnectCancelsChild(t *testing.T) {
	worker := healthyMediaWorker()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	tool := filepath.Join(dir, "probe")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\necho $$ > '"+pidFile+"'\nexec sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	worker.config.Normalization.FFprobe = tool
	client := testMediaWorker(t, worker)
	input, err := os.Open(tool)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := client.probeArchiveMedia(ctx, input, false); done <- err }()
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(pidFile); err != nil {
		cancel()
		t.Fatal("child did not start", err)
	}
	if _, err := client.probeArchiveMedia(context.Background(), input, false); err == nil {
		cancel()
		t.Fatal("second operation admitted while child was active")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	// Wait for the single-operation slot to be released, then inspect the child.
	for i := 0; i < 200; i++ {
		pid, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat("/proc/" + strings.TrimSpace(string(pid))); errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("disconnected media child survived")
}

func TestArchiveDrainMarker(t *testing.T) {
	s := &Service{}
	if s.archiveDraining() {
		t.Fatal("drain enabled without configuration")
	}
	s.config.ArchiveDrainFile = filepath.Join(t.TempDir(), "drain")
	if s.archiveDraining() {
		t.Fatal("missing marker must allow work")
	}
	if err := os.WriteFile(s.config.ArchiveDrainFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if !s.archiveDraining() {
		t.Fatal("marker did not block new ingests")
	}
	s.config.ArchiveDrainFile += "/invalid/child"
	if !s.archiveDraining() {
		t.Fatal("marker lookup error did not fail closed")
	}
}

func TestMediaWorkerRealEncoding(t *testing.T) {
	ffmpeg := os.Getenv("FILEBEANS_TEST_ARCHIVE_FFMPEG")
	ffprobe := os.Getenv("FILEBEANS_TEST_ARCHIVE_FFPROBE")
	if ffmpeg == "" || ffprobe == "" {
		t.Skip("explicit tools required")
	}
	worker := healthyMediaWorker()
	worker.config.Normalization = NormalizationConfig{FFmpeg: ffmpeg, FFprobe: ffprobe}
	// Exercise the actual worker cgroup guard under the constrained test runner.
	worker.archiveMemory.read = nil
	var client *Service
	if socket := os.Getenv("FILEBEANS_TEST_MEDIA_SOCKET"); socket != "" {
		client = &Service{config: Config{ArchiveMediaSocket: socket}}
	} else {
		client = testMediaWorker(t, worker)
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	path := filepath.Join(dir, "input.mp4")
	cmd := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=720x1280:rate=10:duration=2", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	directory, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	info, err := client.probeArchiveMedia(ctx, input, false)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := makeMediaPlan(info, "input.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if err = client.encodeArchiveMedia(ctx, input, directory, plan, 64<<20); err != nil {
		t.Fatal(err)
	}
	output, err := os.Open(filepath.Join(dir, "output.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err = client.validateArchiveFile(ctx, output, plan); err != nil {
		t.Fatal(err)
	}
}

// A test-only entrypoint permits namespace-isolated integration without an app
// image build, production configuration, credentials, or database access.
func TestMediaWorkerProcess(t *testing.T) {
	socket := os.Getenv("FILEBEANS_TEST_MEDIA_SERVER")
	if socket == "" {
		t.Skip("external worker harness only")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := RunMediaWorker(ctx, socket); err != nil {
		t.Fatal(err)
	}
}
