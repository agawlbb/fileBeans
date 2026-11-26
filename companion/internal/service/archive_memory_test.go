package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchiveHostMemoryParsing(t *testing.T) {
	value, err := hostAvailableMemory([]byte("MemFree: 1 kB\nMemAvailable: 3145728 kB\n"))
	if err != nil || value != 3<<30 {
		t.Fatal("must use available memory including reclaimable cache", value, err)
	}
	for _, text := range []string{"MemFree: 10000000 kB", "MemAvailable: invalid kB", "MemAvailable: 3 MB", "MemAvailable: 18446744073709551615 kB"} {
		if _, err := hostAvailableMemory([]byte(text)); err == nil {
			t.Fatal("accepted unavailable/invalid host metric", text)
		}
	}
}

func TestArchiveMemoryAdmissionAndRecovery(t *testing.T) {
	now := time.Unix(10000, 0)
	sample := archiveMemorySample{available: 3 << 30, used: 32 << 20, limit: 2 << 30}
	g := archiveMemoryGuard{read: func() (archiveMemorySample, error) { return sample, nil }}
	if !g.canStart(now, false) {
		t.Fatal("healthy initial admission blocked")
	}
	sample.available = 2 << 30
	if g.canStart(now, false) {
		t.Fatal("admitted without 2.5 GiB host headroom")
	}
	sample.available = 3 << 30
	if g.canStart(now.Add(time.Second), false) || g.canStart(now.Add(30*time.Second), false) {
		t.Fatal("restarted before 30 seconds of healthy headroom")
	}
	sample.available = 2 << 30
	if g.canStart(now.Add(31*time.Second), false) {
		t.Fatal("admitted during renewed pressure")
	}
	sample.available = 3 << 30
	if g.canStart(now.Add(32*time.Second), false) || !g.canStart(now.Add(62*time.Second), false) {
		t.Fatal("headroom recovery hysteresis failed")
	}
	sample.used = 600 << 20
	if g.canStart(now.Add(63*time.Second), false) {
		t.Fatal("admitted without encoder allowance inside container")
	}
	// A persisted memory deferral also waits after service recreation.
	sample.used = 32 << 20
	restarted := archiveMemoryGuard{read: g.read}
	if restarted.canStart(now, true) || !restarted.canStart(now.Add(30*time.Second), true) || !restarted.canStart(now.Add(30*time.Second), true) {
		t.Fatal("restart lost cooldown or reblocked an admitted job")
	}
}

func TestArchiveMemoryPressureAndMetricFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sample archiveMemorySample
		err    error
		stop   bool
	}{
		{"host spike", archiveMemorySample{available: 900 << 20, used: 32 << 20, limit: 2 << 30}, nil, true},
		{"container spike", archiveMemorySample{available: 3 << 30, used: 1600 << 20, limit: 2 << 30}, nil, true},
		{"moderate host pressure", archiveMemorySample{available: 2 << 30, used: 500 << 20, limit: 2 << 30}, nil, false},
		{"metric unreadable", archiveMemorySample{}, os.ErrPermission, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := archiveMemoryGuard{read: func() (archiveMemorySample, error) { return tc.sample, tc.err }}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			if stop := g.cancelIfPressure(cancel); stop != tc.stop || errors.Is(context.Cause(ctx), errArchiveMemory) != tc.stop {
				t.Fatal("incorrect pressure cancellation", stop, context.Cause(ctx))
			}
			if tc.err != nil && g.canStart(time.Now(), false) {
				t.Fatal("metric failure allowed new work")
			}
		})
	}
}

func TestArchiveMemoryDeferralKeepsJobAndAttemptCount(t *testing.T) {
	now := time.Unix(10000, 0)
	job := archiveTask{State: "waiting", Attempts: 4, Error: "old failure", Original: "original.mov", Hash: "checksum"}
	if !deferArchiveForMemory(&job, now) || !job.WaitingForMemory || job.Error != "" || job.Next != now.Add(5*time.Second).Unix() || job.Attempts != 4 || job.State != "waiting" || job.Original != "original.mov" || job.Hash != "checksum" {
		t.Fatal("memory deferral damaged durable job", job)
	}
	if deferArchiveForMemory(&job, now.Add(time.Minute)) {
		t.Fatal("unchanged memory wait would rewrite journal on every poll")
	}
	items := (&Service{}).archiveWorkStatuses([]archiveTask{job})
	if len(items) != 1 || items[0].Phase != "waitingMemory" || items[0].Error != "" {
		t.Fatal("expected a waiting state rather than failure", items)
	}
}

func TestArchiveMemoryCancellationRemovesPartialAndKeepsOriginal(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = root.WriteFile("original", []byte("retained original"), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := root.Open("original")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if err = root.MkdirAll("stage", 0700); err != nil {
		t.Fatal(err)
	}
	directory, err := root.Open("stage")
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	helper := filepath.Join(t.TempDir(), "encoder")
	if err = os.WriteFile(helper, []byte("#!/bin/sh\nprintf partial > /proc/self/fd/4/output.mp4\nexec /bin/sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	done := make(chan error, 1)
	s := &Service{config: Config{Normalization: NormalizationConfig{FFmpeg: helper}}}
	go func() { done <- s.encodeArchiveMedia(ctx, input, directory, mediaPlan{Extension: ".mp4"}, 1<<20) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err = root.Stat("stage/output.mp4"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("encoder did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	g := archiveMemoryGuard{read: func() (archiveMemorySample, error) {
		return archiveMemorySample{available: 900 << 20, used: 32 << 20, limit: 2 << 30}, nil
	}}
	if !g.cancelIfPressure(cancel) {
		t.Fatal("did not yield under host pressure")
	}
	select {
	case err = <-done:
		if !errors.Is(err, errArchiveMemory) || publicError(err) != "Waiting for memory" {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("encoder did not stop promptly")
	}
	if _, err = root.Stat("stage/output.mp4"); !os.IsNotExist(err) {
		t.Fatal("partial output retained", err)
	}
	original, err := root.ReadFile("original")
	if err != nil || string(original) != "retained original" {
		t.Fatal("original changed", err)
	}
}

func TestArchiveEncoderLowPriority(t *testing.T) {
	command := archiveCommand(context.Background(), "/bin/sh", "-c", "ps -o ni= -p $$; ionice -p $$; cat /proc/self/oom_score_adj")
	output, err := command.Output()
	if err != nil || !strings.Contains(string(output), "10") || !strings.Contains(string(output), "idle") || !strings.Contains(string(output), "500") {
		t.Fatal("encoder must yield CPU and disk priority", string(output), err)
	}
}
