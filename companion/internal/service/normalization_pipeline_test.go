package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestNormalizationCompletedIsUntouched(t *testing.T) {
	// No DB, media executable, or source file exists: completed jobs must short-circuit.
	j := NormalizationJob{ID: "completed-before-upgrade", State: "completed", Profile: normalizationProfile, Published: true, OutputHash: "retained", OutputVersion: "retained-version"}
	before := j
	if err := (&Service{}).runNormalizationJob(context.Background(), &j); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(j, before) || normRunnable(j) {
		t.Fatal("completed record changed or became runnable")
	}
}

func TestNormalizationPipelineBounds(t *testing.T) {
	p := newNormPipeline()
	used := uint64(100 << 20)
	p.memory = func() (uint64, error) { return used, nil }
	ctx := context.WithValue(context.Background(), normPipelineKey{}, p)
	release, err := normStage(ctx, "encode")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = normStage(cancelled, "encode"); !errors.Is(err, context.Canceled) {
		t.Fatal("second encoder was admitted", err)
	}
	validate, err := normStage(ctx, "validate")
	if err != nil {
		t.Fatal("validation could not overlap encoding", err)
	}
	validate()
	release()
	release() // Stage release must be idempotent.
	oldCtx, oldCancel := context.WithCancelCause(ctx)
	newCtx, newCancel := context.WithCancelCause(ctx)
	defer oldCancel(nil)
	defer newCancel(nil)
	p.active["old"] = normTask{order: 1, cancel: oldCancel}
	p.active["new"] = normTask{order: 2, cancel: newCancel}
	used = normMemoryAdmission
	if p.admit() {
		t.Fatal("admission above memory threshold")
	}
	used = normMemoryStop + 1
	p.pressure()
	if !errors.Is(context.Cause(newCtx), errNormalizationMemory) || oldCtx.Err() != nil {
		t.Fatal("did not cancel newest task only")
	}
	delete(p.active, "new")
	used = 100 << 20
	if p.admit() {
		t.Fatal("fallback still overlaps jobs")
	}
	delete(p.active, "old")
	if !p.admit() {
		t.Fatal("fallback cannot resume alone")
	}
}
