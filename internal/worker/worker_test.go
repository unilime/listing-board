package worker

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"listingboard/internal/board"
)

type fakeStore struct {
	mu    sync.Mutex
	jobs  map[string]board.Job
	saves map[string]int
}

func newFake(jobs ...board.Job) *fakeStore {
	f := &fakeStore{jobs: map[string]board.Job{}, saves: map[string]int{}}
	for _, job := range jobs {
		f.jobs[job.ID] = job
	}
	return f
}

func (f *fakeStore) ListRunning(context.Context) ([]board.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []board.Job
	for _, job := range f.jobs {
		if job.Status == "running" {
			out = append(out, clone(job))
		}
	}
	return out, nil
}

func (f *fakeStore) Job(_ context.Context, id string) (board.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	job, ok := f.jobs[id]
	if !ok {
		return board.Job{}, board.ErrNotFound
	}
	return clone(job), nil
}

func (f *fakeStore) UpdateJob(_ context.Context, job board.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[job.ID] = clone(job)
	f.saves[job.ID]++
	return nil
}

func (f *fakeStore) snapshot(id string) (board.Job, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return clone(f.jobs[id]), f.saves[id]
}

func clone(job board.Job) board.Job {
	job.Checklist = append([]board.Step(nil), job.Checklist...)
	return job
}

func runningJob(id string) board.Job {
	return board.Job{
		ID: id, UserID: "user-1", Title: "Loft", Address: "10 Market Street",
		Status: "running", RunID: "run-" + id, Filename: "brochure.pdf",
		Checklist: board.NewChecklist(),
	}
}

func storeBrochure(t *testing.T, dir string, job board.Job) {
	t.Helper()
	path := board.BrochurePath(dir, job.UserID, job.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// start runs a worker and returns a function that stops it and waits for it.
func start(t *testing.T, store Store, dir string, pause time.Duration) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	w := &Worker{Store: store, DataDir: dir, Pause: pause, Poll: 5 * time.Millisecond}
	go func() {
		defer close(done)
		w.Run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWorkerFinishesARunningSheetOneStepAtATime(t *testing.T) {
	dir := t.TempDir()
	job := runningJob("a")
	storeBrochure(t, dir, job)
	store := newFake(job)

	stop := start(t, store, dir, 40*time.Millisecond)
	defer stop()

	// The first step is saved right away, the others only after a pause each.
	waitFor(t, "the first step", func() bool {
		got, _ := store.snapshot("a")
		return got.Checklist[0].State == "done"
	})
	got, _ := store.snapshot("a")
	if got.Status != "running" || got.Checklist[1].State != "pending" || got.Checklist[2].State != "pending" {
		t.Fatalf("after the first step: %+v", got)
	}

	waitFor(t, "success", func() bool {
		got, _ := store.snapshot("a")
		return got.Status == "success"
	})
	got, saves := store.snapshot("a")
	if saves != 3 {
		t.Fatalf("saved %d times, want one save per step (3)", saves)
	}
	for _, step := range got.Checklist {
		if step.State != "done" {
			t.Fatalf("step %q is %s", step.Label, step.State)
		}
	}
	if got.RunID != "run-a" || got.Error != "" {
		t.Fatalf("runId %q, error %q", got.RunID, got.Error)
	}
}

func TestWorkerContinuesFromTheFirstPendingStep(t *testing.T) {
	dir := t.TempDir()
	job := runningJob("b")
	job.Checklist[0].State = "done"
	storeBrochure(t, dir, job)
	store := newFake(job)

	stop := start(t, store, dir, time.Millisecond)
	defer stop()
	waitFor(t, "success", func() bool {
		got, _ := store.snapshot("b")
		return got.Status == "success"
	})
	got, saves := store.snapshot("b")
	if saves != 2 {
		t.Fatalf("saved %d times, want 2: a done step must not be done again", saves)
	}
	if got.RunID != "run-b" {
		t.Fatalf("runId changed to %q", got.RunID)
	}
}

func TestWorkerStoppedMidRunLeavesTheSheetRunning(t *testing.T) {
	dir := t.TempDir()
	job := runningJob("c")
	storeBrochure(t, dir, job)
	store := newFake(job)

	stop := start(t, store, dir, time.Hour)
	waitFor(t, "the first step", func() bool {
		got, _ := store.snapshot("c")
		return got.Checklist[0].State == "done"
	})
	stop()
	got, _ := store.snapshot("c")
	if got.Status != "running" || got.RunID != "run-c" {
		t.Fatalf("after stopping the worker: %+v", got)
	}

	// A new worker process picks the sheet up again.
	stop = start(t, store, dir, time.Millisecond)
	defer stop()
	waitFor(t, "success", func() bool {
		got, _ := store.snapshot("c")
		return got.Status == "success"
	})
	got, saves := store.snapshot("c")
	if saves != 3 || got.RunID != "run-c" {
		t.Fatalf("saves %d, runId %q", saves, got.RunID)
	}
}

func TestWorkerFailsASheetWhoseBrochureIsMissing(t *testing.T) {
	dir := t.TempDir()
	job := runningJob("d") // no brochure on disk
	store := newFake(job)

	stop := start(t, store, dir, time.Millisecond)
	defer stop()
	waitFor(t, "failure", func() bool {
		got, _ := store.snapshot("d")
		return got.Status == "failed"
	})
	got, _ := store.snapshot("d")
	if got.Error != "Brochure is missing" {
		t.Fatalf("error %q", got.Error)
	}
	if got.RunID != "run-d" {
		t.Fatalf("runId %q", got.RunID)
	}
	for _, step := range got.Checklist {
		if step.State != "pending" {
			t.Fatalf("step %q is %s, nothing should have run", step.Label, step.State)
		}
	}
}

func TestWorkerLeavesOtherSheetsAlone(t *testing.T) {
	dir := t.TempDir()
	draft := runningJob("e")
	draft.Status = "draft"
	draft.RunID = ""
	failed := runningJob("f")
	failed.Status = "failed"
	failed.Error = "Brochure is missing"
	done := runningJob("g")
	done.Status = "success"
	store := newFake(draft, failed, done)

	stop := start(t, store, dir, time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	stop()
	for _, id := range []string{"e", "f", "g"} {
		if _, saves := store.snapshot(id); saves != 0 {
			t.Fatalf("sheet %s was saved %d times", id, saves)
		}
	}
	if got, _ := store.snapshot("f"); got.Status != "failed" {
		t.Fatalf("failed sheet is now %s", got.Status)
	}
}
