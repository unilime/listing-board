package board

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"time"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrDuplicate = errors.New("duplicate")
)

type User struct {
	ID           string
	Email        string
	PasswordHash string
}

type Step struct {
	Label string `json:"label"`
	State string `json:"state"`
}

type Job struct {
	ID        string    `json:"id"`
	UserID    string    `json:"-"`
	Title     string    `json:"title"`
	Address   string    `json:"address"`
	Status    string    `json:"status"`
	Error     string    `json:"error"`
	RunID     string    `json:"runId"`
	Filename  string    `json:"filename"`
	Checklist []Step    `json:"checklist"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func NewChecklist() []Step {
	return []Step{
		{Label: "Reading the brochure", State: "pending"},
		{Label: "Checking the address", State: "pending"},
		{Label: "Publishing the sheet", State: "pending"},
	}
}

// Advance marks the first pending step done. When no step is left pending
// afterwards the job becomes successful. It reports whether the job is now
// finished. Steps that are already done are never touched again.
func (j *Job) Advance() bool {
	for i := range j.Checklist {
		if j.Checklist[i].State == "pending" {
			j.Checklist[i].State = "done"
			break
		}
	}
	for _, step := range j.Checklist {
		if step.State == "pending" {
			return false
		}
	}
	j.Status = "success"
	j.Error = ""
	return true
}

// BrochurePath is where the brochure bytes of a job live under dataDir.
func BrochurePath(dataDir string, userID, jobID string) string {
	return filepath.Join(dataDir, userID, jobID, "body")
}

func NewID() string {
	return randomHex(16)
}

func NewToken() string {
	return randomHex(32)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

// Store is the persistence used by the HTTP API.
type Store interface {
	CreateUser(ctx context.Context, email, passwordHash string) (User, error)
	UserByEmail(ctx context.Context, email string) (User, error)
	CreateSession(ctx context.Context, userID, token string, expires time.Time) error
	DeleteSession(ctx context.Context, token string) error
	UserByToken(ctx context.Context, token string) (User, error)
	ListJobs(ctx context.Context, userID string) ([]Job, error)
	CreateJob(ctx context.Context, job Job) error
	Job(ctx context.Context, id string) (Job, error)
	UpdateJob(ctx context.Context, job Job) error
}
