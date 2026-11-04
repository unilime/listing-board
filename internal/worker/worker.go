// Package worker walks running sheets through their checklist.
package worker

import (
	"context"
	"errors"
	"log"
	"os"
	"sync"
	"time"

	"listingboard/internal/board"
)

// MissingBrochure is the exact error text of a sheet whose brochure cannot be read.
const MissingBrochure = "Brochure is missing"

// Store is what the worker needs from persistence.
type Store interface {
	ListRunning(ctx context.Context) ([]board.Job, error)
	Job(ctx context.Context, id string) (board.Job, error)
	UpdateJob(ctx context.Context, job board.Job) error
}

type Worker struct {
	Store   Store
	DataDir string
	// Pause is the wait between two steps of one sheet.
	Pause time.Duration
	// Poll is how often the worker looks for running sheets.
	Poll time.Duration

	mu     sync.Mutex
	active map[string]bool
}

// Run picks up running sheets until ctx is cancelled. Sheets that are already
// running when the process starts are picked up on the first poll, so a
// restarted worker resumes them from their first pending step.
func (w *Worker) Run(ctx context.Context) {
	if w.Poll <= 0 {
		w.Poll = 500 * time.Millisecond
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		w.dispatch(ctx, &wg)
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.Poll):
		}
	}
}

func (w *Worker) dispatch(ctx context.Context, wg *sync.WaitGroup) {
	jobs, err := w.Store.ListRunning(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("worker: list running sheets: %v", err)
		}
		return
	}
	for _, job := range jobs {
		if !w.claim(job.ID) {
			continue
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			defer w.release(id)
			w.process(ctx, id)
		}(job.ID)
	}
}

// claim stops two goroutines of this process from working on the same sheet.
func (w *Worker) claim(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active == nil {
		w.active = map[string]bool{}
	}
	if w.active[id] {
		return false
	}
	w.active[id] = true
	return true
}

func (w *Worker) release(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.active, id)
}

// process does one step, saves it, waits Pause, and repeats until the sheet is
// finished. The sheet is read again before every step, so steps that are
// already done are never done twice and a sheet that stopped running is left alone.
func (w *Worker) process(ctx context.Context, id string) {
	for {
		job, err := w.Store.Job(ctx, id)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, board.ErrNotFound) {
				log.Printf("worker: load sheet %s: %v", id, err)
			}
			return
		}
		if job.Status != "running" {
			return
		}
		if _, err := os.ReadFile(board.BrochurePath(w.DataDir, job.UserID, job.ID)); err != nil {
			job.Status = "failed"
			job.Error = MissingBrochure
			w.save(ctx, job)
			return
		}
		finished := job.Advance()
		if !w.save(ctx, job) || finished {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.Pause):
		}
	}
}

func (w *Worker) save(ctx context.Context, job board.Job) bool {
	job.UpdatedAt = time.Now().UTC()
	if err := w.Store.UpdateJob(ctx, job); err != nil {
		if ctx.Err() == nil {
			log.Printf("worker: save sheet %s: %v", job.ID, err)
		}
		return false
	}
	return true
}
