package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"listingboard/internal/board"
	"listingboard/internal/worker"
)

// The API only hands a draft over to the worker. The sheet becomes successful
// when the worker has walked it through the checklist, not inside the handler.
func TestStartMarksTheJobSuccessful(t *testing.T) {
	srv, dir := newTestServer(t)
	cookie := signUp(t, srv, "ada@example.com")
	job := createSheet(t, srv, cookie, "Market loft", "10 Market Street")
	uploadFile(t, srv, cookie, job.ID, "brochure.pdf", []byte("%PDF-1.4\n"), "application/pdf")

	res := call(srv, http.MethodPost, "/api/jobs/"+job.ID+"/start", cookie, nil, "")
	if res.Code != http.StatusOK {
		t.Fatalf("status %d: %s", res.Code, res.Body.String())
	}
	started := decodeJob(t, res)
	if started.Status != "running" {
		t.Fatalf("status %s right after start, want running", started.Status)
	}
	if started.RunID == "" {
		t.Fatal("runId is empty")
	}
	for _, step := range started.Checklist {
		if step.State != "pending" {
			t.Fatalf("step %q is %s right after start, want pending", step.Label, step.State)
		}
	}

	runWorker(t, srv, dir, 10*time.Millisecond, func(w *worker.Worker) bool {
		return fetchJob(t, srv, cookie, job.ID).Status == "success"
	})

	got := fetchJob(t, srv, cookie, job.ID)
	if got.Status != "success" {
		t.Fatalf("status %s after the worker ran, want success", got.Status)
	}
	if got.RunID != started.RunID {
		t.Fatalf("runId changed from %s to %s", started.RunID, got.RunID)
	}
	for _, step := range got.Checklist {
		if step.State != "done" {
			t.Fatalf("step %q is %s", step.Label, step.State)
		}
	}
}

// The client's filename is kept in the sheet only. The bytes live at
// {DATA_DIR}/{userID}/{jobID}/body whatever the file was called.
func TestUploadKeepsTheClientFilename(t *testing.T) {
	srv, dir := newTestServer(t)
	cookie := signUp(t, srv, "ada@example.com")
	job := createSheet(t, srv, cookie, "Market loft", "10 Market Street")
	data := []byte("%PDF-1.4\n")
	uploadFile(t, srv, cookie, job.ID, "brochure.pdf", data, "application/pdf")

	if got := fetchJob(t, srv, cookie, job.ID).Filename; got != "brochure.pdf" {
		t.Fatalf("filename %q, want brochure.pdf", got)
	}
	path := filepath.Join(dir, accountID(t, srv, cookie), job.ID, "body")
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("brochure was not stored at %s: %v", path, err)
	}
	if !bytes.Equal(stored, data) {
		t.Fatalf("stored %q, want %q", stored, data)
	}
	if _, err := os.Stat(filepath.Join(dir, job.ID, "brochure.pdf")); err == nil {
		t.Fatal("brochure was also stored under its client filename")
	}
}

func TestStartWithoutABrochureIsRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	cookie := signUp(t, srv, "ada@example.com")
	job := createSheet(t, srv, cookie, "Market loft", "10 Market Street")

	res := call(srv, http.MethodPost, "/api/jobs/"+job.ID+"/start", cookie, nil, "")
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", res.Code)
	}
	if !strings.Contains(res.Body.String(), "Attach a brochure first") {
		t.Fatalf("body %s", res.Body.String())
	}
	if got := fetchJob(t, srv, cookie, job.ID).Status; got != "draft" {
		t.Fatalf("status %s, want draft", got)
	}
}

func TestStartingARunningSheetAgainKeepsTheRunID(t *testing.T) {
	srv, _ := newTestServer(t)
	cookie := signUp(t, srv, "ada@example.com")
	job := sheetWithBrochure(t, srv, cookie, "Market loft")

	first := startSheet(t, srv, cookie, job.ID, http.StatusOK)
	second := startSheet(t, srv, cookie, job.ID, http.StatusOK)
	if first.RunID == "" || first.RunID != second.RunID {
		t.Fatalf("runId %q then %q", first.RunID, second.RunID)
	}
	if second.Status != "running" {
		t.Fatalf("status %s, want running", second.Status)
	}
}

func TestSecondSheetCannotStartWhileOneIsRunning(t *testing.T) {
	srv, _ := newTestServer(t)
	ada := signUp(t, srv, "ada@example.com")
	grace := signUp(t, srv, "grace@example.com")
	one := sheetWithBrochure(t, srv, ada, "Market loft")
	two := sheetWithBrochure(t, srv, ada, "Harbour flat")
	other := sheetWithBrochure(t, srv, grace, "Hill house")

	startSheet(t, srv, ada, one.ID, http.StatusOK)
	startSheet(t, srv, ada, two.ID, http.StatusConflict)
	if got := fetchJob(t, srv, ada, two.ID).Status; got != "draft" {
		t.Fatalf("refused sheet is %s, want draft", got)
	}
	// Another account is not affected.
	startSheet(t, srv, grace, other.ID, http.StatusOK)
}

func TestAFailedSheetStaysFailed(t *testing.T) {
	srv, _ := newTestServer(t)
	cookie := signUp(t, srv, "ada@example.com")
	job := sheetWithBrochure(t, srv, cookie, "Market loft")
	stored, _ := srv.store.Job(context.Background(), job.ID)
	stored.Status = "failed"
	stored.Error = "Brochure is missing"
	stored.RunID = "run-1"
	if err := srv.store.UpdateJob(context.Background(), stored); err != nil {
		t.Fatal(err)
	}

	got := startSheet(t, srv, cookie, job.ID, http.StatusOK)
	if got.Status != "failed" || got.RunID != "run-1" || got.Error != "Brochure is missing" {
		t.Fatalf("failed sheet changed: %+v", got)
	}
}

func TestOtherUsersSheetsAreNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	ada := signUp(t, srv, "ada@example.com")
	grace := signUp(t, srv, "grace@example.com")
	job := sheetWithBrochure(t, srv, ada, "Market loft")

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/jobs/" + job.ID},
		{http.MethodGet, "/api/jobs/" + job.ID + "/file"},
		{http.MethodPost, "/api/jobs/" + job.ID + "/start"},
	} {
		res := call(srv, route.method, route.path, grace, nil, "")
		if res.Code != http.StatusNotFound {
			t.Fatalf("%s %s: status %d, want 404", route.method, route.path, res.Code)
		}
	}
	res := upload(t, srv, grace, job.ID, "other.txt", []byte("hello"), "text/plain")
	if res.Code != http.StatusNotFound {
		t.Fatalf("upload: status %d, want 404", res.Code)
	}
	if got := fetchJob(t, srv, ada, job.ID); got.Status != "draft" || got.Filename != "brochure.pdf" {
		t.Fatalf("the owner's sheet was changed: %+v", got)
	}
	if res := call(srv, http.MethodGet, "/api/jobs/"+board.NewID(), ada, nil, ""); res.Code != http.StatusNotFound {
		t.Fatalf("unknown sheet: status %d, want 404", res.Code)
	}
}

func TestDownloadReturnsTheBrochureBytes(t *testing.T) {
	srv, _ := newTestServer(t)
	cookie := signUp(t, srv, "ada@example.com")
	job := createSheet(t, srv, cookie, "Market loft", "10 Market Street")
	data := []byte("%PDF-1.4\nbody")
	uploadFile(t, srv, cookie, job.ID, "../brochure.pdf", data, "application/pdf")

	res := call(srv, http.MethodGet, "/api/jobs/"+job.ID+"/file", cookie, nil, "")
	if res.Code != http.StatusOK {
		t.Fatalf("status %d", res.Code)
	}
	if !bytes.Equal(res.Body.Bytes(), data) {
		t.Fatalf("body %q", res.Body.String())
	}
	if got := res.Header().Get("Content-Type"); got != "application/pdf" {
		t.Fatalf("content type %q", got)
	}
	if got := res.Header().Get("Content-Disposition"); !strings.Contains(got, `filename=brochure.pdf`) {
		t.Fatalf("content disposition %q", got)
	}
}

func TestSecondUploadIsRejected(t *testing.T) {
	srv, dir := newTestServer(t)
	cookie := signUp(t, srv, "ada@example.com")
	job := sheetWithBrochure(t, srv, cookie, "Market loft")

	res := upload(t, srv, cookie, job.ID, "again.txt", []byte("another brochure"), "text/plain")
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", res.Code)
	}
	stored, err := os.ReadFile(filepath.Join(dir, accountID(t, srv, cookie), job.ID, "body"))
	if err != nil || string(stored) != "%PDF-1.4\n" {
		t.Fatalf("first brochure was replaced: %q, %v", stored, err)
	}
	if got := fetchJob(t, srv, cookie, job.ID).Filename; got != "brochure.pdf" {
		t.Fatalf("filename %q", got)
	}
}

func TestUploadSizeLimit(t *testing.T) {
	srv, dir := newTestServer(t)
	cookie := signUp(t, srv, "ada@example.com")

	over := createSheet(t, srv, cookie, "Too big", "1 Long Road")
	res := upload(t, srv, cookie, over.ID, "big.txt", bytes.Repeat([]byte("a"), 1<<20+1), "text/plain")
	if res.Code != http.StatusBadRequest {
		t.Fatalf("1 MiB + 1 byte: status %d, want 400", res.Code)
	}
	if got := fetchJob(t, srv, cookie, over.ID).Filename; got != "" {
		t.Fatalf("filename %q after a rejected upload", got)
	}
	if files := filesUnder(t, dir); len(files) != 0 {
		t.Fatalf("rejected upload left files on disk: %v", files)
	}

	exact := createSheet(t, srv, cookie, "Just right", "2 Long Road")
	res = upload(t, srv, cookie, exact.ID, "ok.txt", bytes.Repeat([]byte("a"), 1<<20), "text/plain")
	if res.Code != http.StatusOK {
		t.Fatalf("exactly 1 MiB: status %d, want 200: %s", res.Code, res.Body.String())
	}
}

func TestUploadTypeComesFromTheBytes(t *testing.T) {
	cases := []struct {
		name        string
		filename    string
		contentType string
		data        []byte
		want        int
	}{
		{"pdf bytes with a lying type and extension", "notes.exe", "image/png", []byte("%PDF-1.7 binary \x00\xff"), http.StatusOK},
		{"text with a pdf name and type", "brochure.pdf", "application/pdf", []byte("Sunny loft, 2 beds\n"), http.StatusOK},
		{"utf-8 text", "brochure.txt", "text/plain", []byte("Квартира біля парку"), http.StatusOK},
		{"png bytes called txt", "brochure.txt", "text/plain", []byte("\x89PNG\r\n\x1a\n\x00\x00"), http.StatusBadRequest},
		{"nul byte in text", "brochure.txt", "text/plain", []byte("hello\x00world"), http.StatusBadRequest},
		{"invalid utf-8", "brochure.txt", "text/plain", []byte("caf\xe9"), http.StatusBadRequest},
		{"empty file", "brochure.pdf", "application/pdf", []byte{}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, dir := newTestServer(t)
			cookie := signUp(t, srv, "ada@example.com")
			job := createSheet(t, srv, cookie, "Market loft", "10 Market Street")
			res := upload(t, srv, cookie, job.ID, tc.filename, tc.data, tc.contentType)
			if res.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", res.Code, tc.want, res.Body.String())
			}
			if tc.want != http.StatusOK {
				if files := filesUnder(t, dir); len(files) != 0 {
					t.Fatalf("rejected upload left files on disk: %v", files)
				}
			}
		})
	}
}

func TestUploadCannotEscapeItsDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	srv := New(newMem(), dir, "")
	cookie := signUp(t, srv, "ada@example.com")
	job := createSheet(t, srv, cookie, "Market loft", "10 Market Street")

	uploadFile(t, srv, cookie, job.ID, "../../escaped.txt", []byte("hello"), "text/plain")

	if got := fetchJob(t, srv, cookie, job.ID).Filename; got != "../../escaped.txt" {
		t.Fatalf("filename %q, want the original name", got)
	}
	want := filepath.Join(accountID(t, srv, cookie), job.ID, "body")
	files := filesUnder(t, root)
	if len(files) != 1 || files[0] != filepath.Join("data", want) {
		t.Fatalf("files %v, want only data/%s", files, want)
	}
}

func TestListHidesOtherUsersJobs(t *testing.T) {
	srv, _ := newTestServer(t)
	ada := signUp(t, srv, "ada@example.com")
	grace := signUp(t, srv, "grace@example.com")
	job := createSheet(t, srv, ada, "Market loft", "10 Market Street")

	res := call(srv, http.MethodGet, "/api/jobs", grace, nil, "")
	if res.Code != http.StatusOK {
		t.Fatalf("status %d: %s", res.Code, res.Body.String())
	}
	var jobs []board.Job
	if err := json.Unmarshal(res.Body.Bytes(), &jobs); err != nil {
		t.Fatal(err)
	}
	for _, item := range jobs {
		if item.ID == job.ID {
			t.Fatal("another account can see this sheet in the list")
		}
	}
}

func TestStartRequiresASession(t *testing.T) {
	srv, _ := newTestServer(t)
	res := call(srv, http.MethodPost, "/api/jobs/"+board.NewID()+"/start", "", nil, "")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", res.Code)
	}
}

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	return New(newMem(), dir, ""), dir
}

func signUp(t *testing.T, srv *Server, email string) string {
	t.Helper()
	body := strings.NewReader(`{"email":"` + email + `","password":"password123"}`)
	res := call(srv, http.MethodPost, "/api/register", "", body, "application/json")
	if res.Code != http.StatusCreated {
		t.Fatalf("register %d: %s", res.Code, res.Body.String())
	}
	for _, cookie := range res.Result().Cookies() {
		if cookie.Name == sessionCookie {
			return cookie.Value
		}
	}
	t.Fatal("register did not set a session")
	return ""
}

func createSheet(t *testing.T, srv *Server, cookie, title, address string) board.Job {
	t.Helper()
	body := strings.NewReader(`{"title":"` + title + `","address":"` + address + `"}`)
	res := call(srv, http.MethodPost, "/api/jobs", cookie, body, "application/json")
	if res.Code != http.StatusCreated {
		t.Fatalf("create %d: %s", res.Code, res.Body.String())
	}
	return decodeJob(t, res)
}

func uploadFile(t *testing.T, srv *Server, cookie, id, name string, data []byte, contentType string) {
	t.Helper()
	res := upload(t, srv, cookie, id, name, data, contentType)
	if res.Code != http.StatusOK {
		t.Fatalf("upload %d: %s", res.Code, res.Body.String())
	}
}

func upload(t *testing.T, srv *Server, cookie, id, name string, data []byte, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, name))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return call(srv, http.MethodPost, "/api/jobs/"+id+"/file", cookie, &buf, writer.FormDataContentType())
}

func sheetWithBrochure(t *testing.T, srv *Server, cookie, title string) board.Job {
	t.Helper()
	job := createSheet(t, srv, cookie, title, "10 Market Street")
	uploadFile(t, srv, cookie, job.ID, "brochure.pdf", []byte("%PDF-1.4\n"), "application/pdf")
	return job
}

func startSheet(t *testing.T, srv *Server, cookie, id string, want int) board.Job {
	t.Helper()
	res := call(srv, http.MethodPost, "/api/jobs/"+id+"/start", cookie, nil, "")
	if res.Code != want {
		t.Fatalf("start: status %d, want %d: %s", res.Code, want, res.Body.String())
	}
	if want != http.StatusOK {
		return board.Job{}
	}
	return decodeJob(t, res)
}

func fetchJob(t *testing.T, srv *Server, cookie, id string) board.Job {
	t.Helper()
	res := call(srv, http.MethodGet, "/api/jobs/"+id, cookie, nil, "")
	if res.Code != http.StatusOK {
		t.Fatalf("get sheet: status %d: %s", res.Code, res.Body.String())
	}
	return decodeJob(t, res)
}

func accountID(t *testing.T, srv *Server, cookie string) string {
	t.Helper()
	res := call(srv, http.MethodGet, "/api/me", cookie, nil, "")
	if res.Code != http.StatusOK {
		t.Fatalf("me: status %d", res.Code)
	}
	var me account
	if err := json.Unmarshal(res.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	return me.ID
}

// runWorker runs a real worker against the server's store until done reports
// true or five seconds have passed.
func runWorker(t *testing.T, srv *Server, dir string, pause time.Duration, done func(*worker.Worker) bool) {
	t.Helper()
	st, ok := srv.store.(worker.Store)
	if !ok {
		t.Fatal("the test store does not implement worker.Store")
	}
	w := &worker.Worker{Store: st, DataDir: dir, Pause: pause, Poll: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		w.Run(ctx)
	}()
	defer func() {
		cancel()
		<-stopped
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if done(w) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// filesUnder lists the regular files below dir, relative to it.
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !entry.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func decodeJob(t *testing.T, res *httptest.ResponseRecorder) board.Job {
	t.Helper()
	var job board.Job
	if err := json.Unmarshal(res.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	return job
}

func call(srv *Server, method, path, cookie string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	return res
}

type mem struct {
	mu       sync.Mutex
	users    map[string]board.User
	byEmail  map[string]string
	sessions map[string]memSession
	jobs     map[string]board.Job
}

type memSession struct {
	userID  string
	expires time.Time
}

func newMem() *mem {
	return &mem{
		users:    map[string]board.User{},
		byEmail:  map[string]string{},
		sessions: map[string]memSession{},
		jobs:     map[string]board.Job{},
	}
}

func (m *mem) CreateUser(_ context.Context, email, passwordHash string) (board.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byEmail[email]; ok {
		return board.User{}, board.ErrDuplicate
	}
	user := board.User{ID: board.NewID(), Email: email, PasswordHash: passwordHash}
	m.users[user.ID] = user
	m.byEmail[email] = user.ID
	return user, nil
}

func (m *mem) UserByEmail(_ context.Context, email string) (board.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byEmail[email]
	if !ok {
		return board.User{}, board.ErrNotFound
	}
	return m.users[id], nil
}

func (m *mem) CreateSession(_ context.Context, userID, token string, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[token] = memSession{userID: userID, expires: expires}
	return nil
}

func (m *mem) DeleteSession(_ context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, token)
	return nil
}

func (m *mem) UserByToken(_ context.Context, token string) (board.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[token]
	if !ok || time.Now().After(session.expires) {
		return board.User{}, board.ErrNotFound
	}
	user, ok := m.users[session.userID]
	if !ok {
		return board.User{}, board.ErrNotFound
	}
	return user, nil
}

func (m *mem) ListJobs(_ context.Context, userID string) ([]board.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var jobs []board.Job
	for _, job := range m.jobs {
		if job.UserID == userID {
			jobs = append(jobs, cloneJob(job))
		}
	}
	if jobs == nil {
		jobs = []board.Job{}
	}
	return jobs, nil
}

func (m *mem) ListRunning(_ context.Context) ([]board.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var jobs []board.Job
	for _, job := range m.jobs {
		if job.Status == "running" {
			jobs = append(jobs, cloneJob(job))
		}
	}
	return jobs, nil
}

func (m *mem) CreateJob(_ context.Context, job board.Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs[job.ID] = cloneJob(job)
	return nil
}

func (m *mem) Job(_ context.Context, id string) (board.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return board.Job{}, board.ErrNotFound
	}
	return cloneJob(job), nil
}

func (m *mem) UpdateJob(_ context.Context, job board.Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[job.ID]; !ok {
		return board.ErrNotFound
	}
	m.jobs[job.ID] = cloneJob(job)
	return nil
}

func cloneJob(job board.Job) board.Job {
	job.Checklist = append([]board.Step(nil), job.Checklist...)
	return job
}
