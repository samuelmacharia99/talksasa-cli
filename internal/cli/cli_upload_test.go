package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samuelmacharia99/talksasa-cli/internal/bundle"
)

// --- Deploy a folder ----------------------------------------------------------------

const (
	createRoute = "POST /services/7/container/uploads"
	deployRoute = "POST /services/7/container/uploads/5/deploy"
	pullRoute   = "GET /services/7/container/pull"
)

func chunkRoute(id int) string { return "PUT /services/7/container/uploads/" + itoa(id) + "/chunk" }

// projectDir fills the harness's folder with a small app whose bundle
// spans several 1 KiB chunks, and points TMPDIR at a folder of its own so
// a test can check the bundle file is gone afterwards.
func projectDir(t *testing.T, h *harness) string {
	t.Helper()
	noise := make([]byte, 5000)
	rand.New(rand.NewSource(1)).Read(noise) // incompressible, so several chunks
	files := map[string]string{
		"package.json":            `{"name":"shop"}`,
		"src/index.js":            "console.log('hi')\n",
		"public/noise.bin":        string(noise),
		".env":                    "SECRET=1",
		"node_modules/x/index.js": "x",
	}
	for name, content := range files {
		full := filepath.Join(h.dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tmp := t.TempDir()
	for _, v := range []string{"TMPDIR", "TMP", "TEMP"} { // os.TempDir: Unix, then Windows
		t.Setenv(v, tmp)
	}
	return tmp
}

// expectedBundle packs the folder the way the CLI will: packing is
// deterministic, so these are the bytes that must arrive.
func expectedBundle(t *testing.T, dir string) []byte {
	t.Helper()
	sum, err := bundle.Pack(context.Background(), dir, bundle.Options{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer sum.Remove()
	data, err := os.ReadFile(sum.Path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertNoBundleLeft(t *testing.T, tmp string) {
	t.Helper()
	left, _ := os.ReadDir(tmp)
	for _, e := range left {
		if strings.HasPrefix(e.Name(), "talksasa-bundle-") {
			t.Fatalf("the bundle file %s was left behind", e.Name())
		}
	}
}

type chunkCall struct {
	upload      int
	offset      int64
	length      int
	contentType string
	declared    int64
}

// fakeUploads plays the platform's side of an upload: it keeps the bytes
// it was sent, answers chunks as CodeUploadService does, and lets a test
// replace any one chunk answer (fault, by the 0-based call number).
type fakeUploads struct {
	t          *testing.T
	mu         sync.Mutex
	chunkBytes int64
	nextID     int
	data       map[int][]byte
	size       int64
	sha        string
	creates    []map[string]any
	chunks     []chunkCall
	resumeAt   []byte // a first create resumes with these bytes already received
	fault      map[int]func(w http.ResponseWriter, f *fakeUploads, id int) bool
}

func newFakeUploads(t *testing.T, h *harness, chunkBytes int64) *fakeUploads {
	f := &fakeUploads{t: t, chunkBytes: chunkBytes, nextID: 5, data: map[int][]byte{}, fault: map[int]func(http.ResponseWriter, *fakeUploads, int) bool{}}
	for id := 5; id < 9; id++ {
		h.api.on(chunkRoute(id), func(w http.ResponseWriter, r *http.Request, _ int) { f.chunk(w, r, id) })
	}
	h.api.on(createRoute, func(w http.ResponseWriter, r *http.Request, _ int) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.creates = append(f.creates, req)
		f.size = int64(req["size_bytes"].(float64))
		f.sha = req["sha256"].(string)
		id := f.nextID
		f.nextID++
		status := 201
		if len(f.creates) == 1 && f.resumeAt != nil {
			f.data[id] = append([]byte(nil), f.resumeAt...)
			status = 200
		} else {
			f.data[id] = nil
		}
		jsonAnswer(w, status, `{"message":"Upload started.","data":{"id":`+itoa(id)+`,"status":"uploading","size_bytes":`+
			strconv.FormatInt(f.size, 10)+`,"received_bytes":`+itoa(len(f.data[id]))+`,"sha256":"`+f.sha+
			`","chunk_bytes":`+strconv.FormatInt(f.chunkBytes, 10)+`,"release_number":null,"expires_at":"2026-11-11T12:00:00+00:00"}}`)
	})
	return f
}

func (f *fakeUploads) chunk(w http.ResponseWriter, r *http.Request, id int) {
	body, _ := io.ReadAll(r.Body)
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)

	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.chunks)
	f.chunks = append(f.chunks, chunkCall{upload: id, offset: offset, length: len(body), contentType: r.Header.Get("Content-Type"), declared: r.ContentLength})
	if fault := f.fault[n]; fault != nil && fault(w, f, id) {
		return
	}
	have := int64(len(f.data[id]))
	switch {
	case int64(len(body)) > f.chunkBytes:
		jsonAnswer(w, 422, `{"message":"A chunk may be at most `+strconv.FormatInt(f.chunkBytes, 10)+` bytes.","code":"validation_failed","received_bytes":`+strconv.FormatInt(have, 10)+`}`)
		return
	case offset > have:
		jsonAnswer(w, 409, `{"message":"Chunk out of order: the platform has `+strconv.FormatInt(have, 10)+` bytes.","code":"conflict","received_bytes":`+strconv.FormatInt(have, 10)+`}`)
		return
	case offset == have:
		f.data[id] = append(f.data[id], body...)
	}
	have = int64(len(f.data[id]))
	jsonAnswer(w, 200, `{"message":"Chunk received.","data":{"received_bytes":`+strconv.FormatInt(have, 10)+
		`,"size_bytes":`+strconv.FormatInt(f.size, 10)+`,"complete":`+strconv.FormatBool(have == f.size)+`}}`)
}

// received is what the platform holds for upload id.
func (f *fakeUploads) received(id int) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.data[id]...)
}

func (f *fakeUploads) offsets() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, len(f.chunks))
	for i, c := range f.chunks {
		out[i] = c.offset
	}
	return out
}

// uploadPull is GET .../pull for an app whose code comes from source,
// following pull 41 of release 3.
func uploadPull(source, status string, active bool) string {
	src := "null"
	if source != "" {
		src = `"` + source + `"`
	}
	b, _ := json.Marshal(active)
	return `{"data":{"repository":{"url":null,"branch":null},"code_source":` + src + `,"pull":{"id":41,"status":"` + status +
		`","is_active":` + string(b) + `,"commit":null,"source":"upload","release":{"id":12,"number":3,"status":"deployed","sha256":"` + releaseSHA + `"},` +
		`"steps":[{"key":"stage","label":"Unpack the upload","status":"completed"}]}}}`
}

const releaseSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

const deployAnswer = `{"message":"Deploy started.","data":{"id":41,"status":"pending","is_active":true,"source":"upload","release":{"id":12,"number":3,"status":"received","sha256":"` + releaseSHA + `"}}}`

// script answers route with each body in turn (then the last again),
// counting from when it is set rather than from the route's first call.
func script(h *harness, route string, bodies ...string) {
	var mu sync.Mutex
	next := 0
	h.api.on(route, func(w http.ResponseWriter, _ *http.Request, _ int) {
		mu.Lock()
		n := next
		next++
		mu.Unlock()
		if n >= len(bodies) {
			n = len(bodies) - 1
		}
		jsonAnswer(w, 200, bodies[n])
	})
}

// readyToDeploy scripts an app whose code is uploaded, that deploys the
// upload and succeeds.
func readyToDeploy(h *harness, source string) {
	script(h, pullRoute,
		uploadPull(source, "completed", false), // where the code comes from
		uploadPull("upload", "running", true),
		uploadPull("upload", "completed", false),
	)
	h.api.answer(deployRoute, 202, deployAnswer)
}

func TestDeployDirSendsTheBundleInChunksAndWaits(t *testing.T) {
	h := newHarness(t)
	tmp := projectDir(t, h)
	want := expectedBundle(t, h.dir)
	f := newFakeUploads(t, h, 1024)
	readyToDeploy(h, "upload")

	r := h.run("", "deploy", "--app", "7", "--dir", h.dir)
	r.expect(t, ExitOK, "Packed", "3 files", "Release 3 is deploying", "Unpack the upload", "Deployed app #7 release 3 (sha256 0123456789ab).")

	if got := f.received(5); !bytes.Equal(got, want) {
		t.Fatalf("the platform received %d bytes that are not the bundle (%d bytes)", len(got), len(want))
	}
	if len(f.chunks) < 5 {
		t.Fatalf("only %d chunks for a %d-byte bundle in 1 KiB chunks", len(f.chunks), len(want))
	}
	var next int64
	for i, c := range f.chunks {
		if c.offset != next || c.length > 1024 || c.length == 0 || int64(c.length) != c.declared || c.contentType != "application/octet-stream" {
			t.Fatalf("chunk %d: %+v (expected offset %d)", i, c, next)
		}
		next += int64(c.length)
	}

	sum := sha256.Sum256(want)
	create := f.creates[0]
	if create["sha256"] != hex.EncodeToString(sum[:]) || int(create["size_bytes"].(float64)) != len(want) ||
		create["file_count"].(float64) != 3 || create["unpacked_bytes"].(float64) <= 0 || create["replace_git"] != nil {
		t.Fatalf("create body %v", create)
	}
	deploy := h.api.lastBody(deployRoute)
	if deploy["run_composer"] != true || deploy["run_migrations"] != true || deploy["force_rebuild"] != false || deploy["replace_git"] != false {
		t.Fatalf("deploy body %v", deploy)
	}
	if strings.Contains(r.stderr, "Warning") {
		t.Fatalf("unexpected warning: %s", r.stderr)
	}
	assertNoBundleLeft(t, tmp)
}

func TestDeployDirSendsTheDeployOptions(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	newFakeUploads(t, h, 1<<20)
	readyToDeploy(h, "upload")
	h.run("", "deploy", "--app", "7", "--dir", h.dir, "--no-composer", "--no-migrations", "--rebuild").expect(t, ExitOK)
	deploy := h.api.lastBody(deployRoute)
	if deploy["run_composer"] != false || deploy["run_migrations"] != false || deploy["force_rebuild"] != true {
		t.Fatalf("deploy body %v", deploy)
	}
}

func TestDeployDirAsksBeforeReplacingCodeThatWasNotUploaded(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	newFakeUploads(t, h, 1<<20)
	readyToDeploy(h, "") // no code source yet

	h.run("", "deploy", "--app", "7", "--dir", h.dir).expect(t, ExitUsage, "Replace the code of app #7 with this folder?", "storage, uploads and settings are kept", "Pass --yes")
	if n := h.api.count(createRoute); n != 0 {
		t.Fatalf("uploaded without confirmation (%d creates)", n)
	}

	h.tty = true
	readyToDeploy(h, "")
	h.run("n\n", "deploy", "--app", "7", "--dir", h.dir).expect(t, ExitFailed, "cancelled")
	if n := h.api.count(createRoute); n != 0 {
		t.Fatal("uploaded after the answer no")
	}
	script(h, pullRoute, uploadPull("", "completed", false), uploadPull("upload", "completed", false))
	h.run("y\n", "deploy", "--app", "7", "--dir", h.dir).expect(t, ExitOK, "Deployed app #7 release 3")
}

func TestDeployDirResumesAPartSentUpload(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	want := expectedBundle(t, h.dir)
	f := newFakeUploads(t, h, 1024)
	f.resumeAt = want[:1500]
	readyToDeploy(h, "upload")

	h.run("", "deploy", "--app", "7", "--dir", h.dir).expect(t, ExitOK, "Resuming upload at 1.5 KiB of", "Deployed app #7 release 3")
	if offsets := f.offsets(); offsets[0] != 1500 {
		t.Fatalf("resumed at %d, want 1500 (offsets %v)", offsets[0], offsets)
	}
	if !bytes.Equal(f.received(5), want) {
		t.Fatal("the resumed upload is not the bundle")
	}
}

func TestDeployDirSeeksToWhereThePlatformIs(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	want := expectedBundle(t, h.dir)
	f := newFakeUploads(t, h, 1024)
	// On the third chunk the platform has lost part of the second: it
	// says where it is, and the client carries on from there.
	f.fault[2] = func(w http.ResponseWriter, f *fakeUploads, id int) bool {
		f.data[id] = f.data[id][:1500]
		jsonAnswer(w, 409, `{"message":"Chunk out of order: the platform has 1500 bytes.","code":"conflict","received_bytes":1500}`)
		return true
	}
	readyToDeploy(h, "upload")

	h.run("", "deploy", "--app", "7", "--dir", h.dir).expect(t, ExitOK, "Deployed")
	if got := f.offsets()[:4]; got[0] != 0 || got[1] != 1024 || got[2] != 2048 || got[3] != 1500 {
		t.Fatalf("offsets %v", f.offsets())
	}
	if !bytes.Equal(f.received(5), want) {
		t.Fatal("the bundle arrived damaged")
	}
}

func TestDeployDirRidesOutABusyPlatform(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	want := expectedBundle(t, h.dir)
	f := newFakeUploads(t, h, 1024)
	f.fault[1] = func(w http.ResponseWriter, _ *fakeUploads, _ int) bool {
		jsonAnswer(w, 503, `{"message":"The platform is busy.","code":"upstream_unavailable"}`)
		return true
	}
	f.fault[2] = func(w http.ResponseWriter, _ *fakeUploads, _ int) bool {
		jsonAnswer(w, 429, `{"message":"Too many requests.","code":"rate_limited"}`)
		return true
	}
	readyToDeploy(h, "upload")

	h.run("", "deploy", "--app", "7", "--dir", h.dir).expect(t, ExitOK, "The platform is busy; trying again", "(1 of 8)", "Deployed")
	if got := f.offsets()[:4]; got[1] != 1024 || got[2] != 1024 || got[3] != 1024 {
		t.Fatalf("the refused chunk was not sent again at its offset: %v", f.offsets())
	}
	if !bytes.Equal(f.received(5), want) {
		t.Fatal("the bundle arrived damaged")
	}
}

func TestDeployDirGivesUpAfterEightRetriesAndSaysHowToResume(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	f := newFakeUploads(t, h, 1024)
	for i := 1; i < 20; i++ {
		f.fault[i] = func(w http.ResponseWriter, _ *fakeUploads, _ int) bool {
			jsonAnswer(w, 503, `{"message":"The platform is busy.","code":"upstream_unavailable"}`)
			return true
		}
	}
	readyToDeploy(h, "upload")

	h.run("", "deploy", "--app", "7", "--dir", h.dir).expect(t, ExitUnavailable, "The platform is busy", "stopped at 1.0 KiB", "Run the same command again to resume.")
	if n := len(f.offsets()); n != 1+1+uploadRetries {
		t.Fatalf("%d chunk requests, want one success then %d tries of the next", n, 1+uploadRetries)
	}
	if h.api.count(deployRoute) != 0 {
		t.Fatal("deployed an incomplete upload")
	}
}

func TestDeployDirStartsAnExpiredUploadAgainOnce(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	want := expectedBundle(t, h.dir)
	f := newFakeUploads(t, h, 1024)
	f.fault[1] = func(w http.ResponseWriter, _ *fakeUploads, _ int) bool {
		jsonAnswer(w, 409, `{"message":"This upload expired. Start it again.","code":"conflict","status":"expired"}`)
		return true
	}
	script(h, pullRoute, uploadPull("upload", "completed", false), uploadPull("upload", "completed", false))
	h.api.answer("POST /services/7/container/uploads/6/deploy", 202, deployAnswer)

	h.run("", "deploy", "--app", "7", "--dir", h.dir).expect(t, ExitOK, "no longer has this upload", "starting it again", "Deployed")
	if len(f.creates) != 2 {
		t.Fatalf("%d creates, want 2", len(f.creates))
	}
	if !bytes.Equal(f.received(6), want) {
		t.Fatal("the fresh upload is not the whole bundle")
	}

	// A second expiry is reported, not chased.
	h2 := newHarness(t)
	projectDir(t, h2)
	f2 := newFakeUploads(t, h2, 1024)
	for i := 0; i < 10; i++ {
		f2.fault[i] = func(w http.ResponseWriter, _ *fakeUploads, _ int) bool {
			jsonAnswer(w, 409, `{"message":"This upload expired. Start it again.","code":"conflict","status":"expired"}`)
			return true
		}
	}
	h2.api.answer(pullRoute, 200, uploadPull("upload", "completed", false))
	h2.run("", "deploy", "--app", "7", "--dir", h2.dir).expect(t, ExitConflict, "This upload expired")
	if len(f2.creates) != 2 {
		t.Fatalf("%d creates, want 2", len(f2.creates))
	}
}

func TestDeployDirSendsTheRestWhenTheDeployFindsItIncomplete(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	want := expectedBundle(t, h.dir)
	f := newFakeUploads(t, h, 1<<20)
	script(h, pullRoute, uploadPull("upload", "completed", false), uploadPull("upload", "completed", false))
	h.api.on(deployRoute, func(w http.ResponseWriter, _ *http.Request, n int) {
		if n == 0 {
			f.mu.Lock()
			f.data[5] = f.data[5][:100]
			f.mu.Unlock()
			jsonAnswer(w, 409, `{"message":"The upload is incomplete: the platform has 100 of many bytes.","code":"conflict","received_bytes":100,"status":"uploading"}`)
			return
		}
		jsonAnswer(w, 202, deployAnswer)
	})
	h.run("", "deploy", "--app", "7", "--dir", h.dir).expect(t, ExitOK, "Deployed")
	if offsets := f.offsets(); len(offsets) != 2 || offsets[1] != 100 {
		t.Fatalf("offsets %v", offsets)
	}
	if !bytes.Equal(f.received(5), want) {
		t.Fatal("the bundle arrived damaged")
	}
}

func TestDeployDirRefusesAGitAppWithoutReplaceGit(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	h.api.answer(pullRoute, 200, `{"data":{"repository":{"url":"https://github.com/acme/shop.git","branch":"main"},"code_source":"git","pull":null}}`)

	h.run("", "deploy", "--app", "7", "--dir", h.dir, "--yes").expect(t, ExitConflict,
		"deploys from its Git repository (https://github.com/acme/shop.git)", "--replace-git", "Destructive")
	if h.api.count(createRoute) != 0 {
		t.Fatal("uploaded to a Git app")
	}

	// The platform's own refusal (the repository connected meanwhile)
	// gets the same hint.
	h2 := newHarness(t)
	projectDir(t, h2)
	h2.api.answer(pullRoute, 200, uploadPull("upload", "completed", false))
	h2.api.answer(createRoute, 409, `{"message":"This app deploys from its Git repository.","code":"git_connected"}`)
	h2.run("", "deploy", "--app", "7", "--dir", h2.dir).expect(t, ExitConflict, "This app deploys from its Git repository.", "Pass --replace-git", "Destructive")
}

func TestDeployDirReplaceGitAsksAndSendsTheFlag(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	f := newFakeUploads(t, h, 1<<20)
	script(h, pullRoute,
		`{"data":{"repository":{"url":"https://github.com/acme/shop.git","branch":"main"},"code_source":"git","pull":null}}`,
		uploadPull("upload", "completed", false))
	h.api.answer(deployRoute, 202, deployAnswer)

	h.run("", "deploy", "--app", "7", "--dir", h.dir, "--replace-git").expect(t, ExitUsage,
		"This disconnects app #7's Git repository and turns off auto-deploy.", "Replace the code", "Pass --yes")
	if len(f.creates) != 0 {
		t.Fatal("uploaded without confirmation")
	}

	script(h, pullRoute,
		`{"data":{"repository":{"url":"https://github.com/acme/shop.git","branch":"main"},"code_source":"git","pull":null}}`,
		uploadPull("upload", "completed", false))
	h.run("", "deploy", "--app", "7", "--dir", h.dir, "--replace-git", "--yes").expect(t, ExitOK, "Deployed")
	if f.creates[0]["replace_git"] != true || h.api.lastBody(deployRoute)["replace_git"] != true {
		t.Fatalf("replace_git not sent: create %v deploy %v", f.creates[0], h.api.lastBody(deployRoute))
	}
}

func TestDeployDirReplaceGitAsksEvenForAnUploadApp(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	newFakeUploads(t, h, 1<<20)
	readyToDeploy(h, "upload")
	h.run("", "deploy", "--app", "7", "--dir", h.dir, "--replace-git").expect(t, ExitUsage, "This disconnects", "Continue?")
}

func TestDeployDirFlagsThatDoNotGoTogether(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	file := filepath.Join(h.dir, "package.json")
	cases := map[string][]string{
		"--dir with --replace":         {"deploy", "--app", "7", "--dir", h.dir, "--replace", "--yes"},
		"--replace-git without --dir":  {"deploy", "--app", "7", "--replace-git", "--yes"},
		"--dry-run without --dir":      {"deploy", "--app", "7", "--dry-run"},
		"--dir that is a file":         {"deploy", "--app", "7", "--dir", file},
		"--dir that is not there":      {"deploy", "--app", "7", "--dir", filepath.Join(h.dir, "missing")},
		"--dir with an empty argument": {"deploy", "--app", "7", "--dir", ""},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			h.run("", args...).expect(t, ExitUsage)
		})
	}
	h.api.mu.Lock()
	defer h.api.mu.Unlock()
	if len(h.api.calls) != 0 {
		t.Fatalf("a refused command line made requests: %v", h.api.calls)
	}
}

func TestDeployDirDryRunSendsNothing(t *testing.T) {
	h := newHarness(t)
	tmp := projectDir(t, h)
	t.Setenv("TALKSASA_TOKEN", "") // not even signed in

	r := h.run("", "deploy", "--dir", h.dir, "--dry-run")
	r.expect(t, ExitOK, "Packed", "Dry run: nothing was uploaded.")
	if r.stdout != "package.json\npublic/noise.bin\nsrc/index.js\n" {
		t.Fatalf("stdout %q", r.stdout)
	}

	r = h.run("", "deploy", "--dir", h.dir, "--dry-run", "--json")
	r.expect(t, ExitOK)
	var out struct {
		DryRun  bool `json:"dry_run"`
		Summary struct {
			SHA256    string `json:"sha256"`
			FileCount int    `json:"file_count"`
			Size      int64  `json:"compressed_bytes"`
		} `json:"summary"`
		Files []struct {
			Path string `json:"path"`
			Size int64  `json:"size"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("stdout is not one JSON document: %q", r.stdout)
	}
	if !out.DryRun || out.Summary.FileCount != 3 || len(out.Files) != 3 || len(out.Summary.SHA256) != 64 || out.Summary.Size <= 0 {
		t.Fatalf("got %+v", out)
	}
	if r.stderr != "" {
		t.Fatalf("stderr in --json: %q", r.stderr)
	}

	h.api.mu.Lock()
	defer h.api.mu.Unlock()
	if len(h.api.calls) != 0 {
		t.Fatalf("a dry run made requests: %v", h.api.calls)
	}
	assertNoBundleLeft(t, tmp)
}

func TestDeployDirJSONPrintsOneDocument(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	newFakeUploads(t, h, 1024)
	readyToDeploy(h, "upload")

	r := h.run("", "deploy", "--app", "7", "--dir", h.dir, "--json")
	r.expect(t, ExitOK)
	var out struct {
		Data struct {
			Pull struct {
				ID      int    `json:"id"`
				Status  string `json:"status"`
				Source  string `json:"source"`
				Release struct {
					Number int `json:"number"`
				} `json:"release"`
			} `json:"pull"`
		} `json:"data"`
	}
	dec := json.NewDecoder(strings.NewReader(r.stdout))
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("stdout is not JSON: %q", r.stdout)
	}
	if dec.More() {
		t.Fatalf("stdout holds more than one document: %q", r.stdout)
	}
	if out.Data.Pull.Status != "completed" || out.Data.Pull.Source != "upload" || out.Data.Pull.Release.Number != 3 {
		t.Fatalf("got %+v", out)
	}
	if r.stderr != "" {
		t.Fatalf("progress went to stderr in --json: %q", r.stderr)
	}
}

func TestDeployDirNoWait(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	newFakeUploads(t, h, 1024)
	readyToDeploy(h, "upload")

	r := h.run("", "deploy", "--app", "7", "--dir", h.dir, "--no-wait")
	r.expect(t, ExitOK)
	if !strings.Contains(r.stdout, "Release 3 is deploying. Follow it with: talksasa deploy status") {
		t.Fatalf("stdout %q", r.stdout)
	}
	if n := h.api.count(pullRoute); n != 1 {
		t.Fatalf("followed the deploy (%d pull reads) with --no-wait", n)
	}
}

func TestDeployDirReportsARejectedBundle(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	newFakeUploads(t, h, 1<<20)
	h.api.answer(pullRoute, 200, uploadPull("upload", "completed", false))
	h.api.answer(deployRoute, 422, `{"message":"The bundle contains a link (public/x). Links are not allowed; include the file itself.","code":"bundle_rejected","status":"rejected","bundle_reason":"unsafe_entry"}`)

	h.run("", "deploy", "--app", "7", "--dir", h.dir).expect(t, ExitInvalid, "The bundle contains a link (public/x)")
}

func TestDeployDirInterruptedKeepsTheUploadForResume(t *testing.T) {
	h := newHarness(t)
	tmp := projectDir(t, h)
	f := newFakeUploads(t, h, 1024)
	readyToDeploy(h, "upload")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.fault[2] = func(w http.ResponseWriter, _ *fakeUploads, _ int) bool {
		cancel() // Ctrl-C while the third chunk is in flight
		jsonAnswer(w, 503, `{"message":"gone","code":"upstream_unavailable"}`)
		return true
	}

	h.runContext(ctx, "", "deploy", "--app", "7", "--dir", h.dir).expect(t, ExitInterrupted, "interrupted at 2.0 KiB of", "Run the same command again to resume.")
	if n := h.api.count("DELETE /services/7/container/uploads/5"); n != 0 {
		t.Fatal("the upload was cancelled; it should be kept for resume")
	}
	if h.api.count(deployRoute) != 0 {
		t.Fatal("deployed after an interrupt")
	}
	assertNoBundleLeft(t, tmp)
}

func TestDeployDirShowsProgressOnATerminal(t *testing.T) {
	h := newHarness(t)
	h.errTTY = true
	projectDir(t, h)
	newFakeUploads(t, h, 1024)
	readyToDeploy(h, "upload")
	r := h.run("", "deploy", "--app", "7", "--dir", h.dir)
	r.expect(t, ExitOK, "\rUploading", "(100%)", "Deployed")
	if strings.Contains(r.stdout, "Uploading") {
		t.Fatal("progress went to stdout")
	}
}

func TestDeployDirWarnsAboutSecretsAndAFolderThatIsNotAnApp(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	if err := os.Remove(filepath.Join(h.dir, "package.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, ".env.production"), []byte("SECRET=2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, ".talksasaignore"), []byte("!.env.production\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := h.run("", "deploy", "--dir", h.dir, "--dry-run")
	r.expect(t, ExitOK, "Warning: these files usually hold secrets and will be uploaded: .env.production", "Environment tab", "Is it the project's root folder?")
	if strings.Contains(r.stdout, ".env\n") || !strings.Contains(r.stdout, ".env.production") {
		t.Fatalf("stdout %q", r.stdout)
	}
}

func TestDeployDirRefusesAFolderOverALimit(t *testing.T) {
	h := newHarness(t)
	projectDir(t, h)
	deep := h.dir
	for i := 0; i < 65; i++ {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.api.answer(pullRoute, 200, uploadPull("upload", "completed", false))
	h.run("", "deploy", "--app", "7", "--dir", h.dir).expect(t, ExitInvalid, "levels deep", "at most 64", ".talksasaignore", "/d/")
	if h.api.count(createRoute) != 0 {
		t.Fatal("uploaded a folder over the limit")
	}
}

func TestGitDeployOnAnUploadAppPointsAtDir(t *testing.T) {
	h := newHarness(t)
	h.api.answer("POST /services/7/container/pull", 409, `{"message":"This app's code is uploaded, not pulled from Git.","code":"conflict"}`)
	h.api.answer(pullRoute, 200, uploadPull("upload", "completed", false))
	h.run("", "deploy", "--app", "7").expect(t, ExitConflict, "not pulled from Git", "This app deploys uploads: run talksasa deploy --dir .")

	// Any other conflict is left as it is.
	h2 := newHarness(t)
	h2.api.answer("POST /services/7/container/pull", 409, `{"message":"A pull is already running.","code":"conflict"}`)
	h2.api.answer(pullRoute, 200, pull(40, "running", true, ""))
	r := h2.run("", "deploy", "--app", "7")
	r.expect(t, ExitConflict, "A pull is already running.")
	if strings.Contains(r.stderr, "deploy --dir") {
		t.Fatalf("hint on a Git app: %s", r.stderr)
	}
}

func TestDeployStatusShowsTheRelease(t *testing.T) {
	h := newHarness(t)
	h.api.answer(pullRoute, 200, uploadPull("upload", "completed", false))
	h.run("", "deploy", "status", "--app", "7").expect(t, ExitOK, "Code: uploaded with talksasa deploy --dir", "Release: 3 (sha256 0123456789ab)")
}

// --- Releases -----------------------------------------------------------------------

const releasesAnswer = `{"message":"Releases.","data":{"code_source":"upload","releases":[
	{"id":12,"number":3,"status":"deployed","is_live":true,"kept":true,"sha256":"` + releaseSHA + `","size_bytes":2048,"unpacked_bytes":9000,"file_count":12,"uploaded_at":"2026-11-11T10:00:00+00:00","last_deployed_at":"2026-11-11T10:05:00+00:00"},
	{"id":11,"number":2,"status":"stored","is_live":false,"kept":true,"sha256":"aa","size_bytes":1024,"unpacked_bytes":4000,"file_count":10,"uploaded_at":"2026-11-10T10:00:00+00:00","last_deployed_at":"2026-11-10T10:05:00+00:00"},
	{"id":9,"number":1,"status":"missing","is_live":false,"kept":false,"sha256":"bb","size_bytes":512,"unpacked_bytes":null,"file_count":null,"uploaded_at":"2026-11-09T10:00:00+00:00","last_deployed_at":null}]}}`

func TestReleasesAreListed(t *testing.T) {
	h := newHarness(t)
	h.api.answer("GET /services/7/container/releases", 200, releasesAnswer)
	r := h.run("", "releases", "--app", "7")
	r.expect(t, ExitOK, "RELEASE", "LIVE", "live", "deployed", "2.0 KiB", "12", "missing (not kept)")
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[1], "3 ") || !strings.HasPrefix(lines[3], "1 ") {
		t.Fatalf("table %q", r.stdout)
	}

	r = h.run("", "releases", "list", "--app", "7", "--json")
	r.expect(t, ExitOK)
	var body map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &body); err != nil || body["data"] == nil {
		t.Fatalf("--json %q", r.stdout)
	}

	h2 := newHarness(t)
	h2.api.answer("GET /services/7/container/releases", 200, `{"data":{"code_source":"git","releases":[]}}`)
	h2.run("", "releases", "--app", "7").expect(t, ExitOK, "no uploaded releases", "deploys from its Git repository")
}

func TestReleaseDeployByNumber(t *testing.T) {
	h := newHarness(t)
	h.api.answer("GET /services/7/container/releases", 200, releasesAnswer)
	h.api.answer("POST /services/7/container/releases/11/deploy", 202,
		`{"message":"Deploy started.","data":{"id":42,"status":"pending","is_active":true,"source":"upload","release":{"id":11,"number":2,"sha256":"aa"}}}`)
	script(h, pullRoute,
		`{"data":{"code_source":"upload","pull":{"id":42,"status":"running","is_active":true,"source":"upload","release":{"id":11,"number":2,"sha256":"aabbccddeeff0011"}}}}`,
		`{"data":{"code_source":"upload","pull":{"id":42,"status":"completed","is_active":false,"source":"upload","release":{"id":11,"number":2,"sha256":"aabbccddeeff0011"}}}}`)

	h.run("", "releases", "deploy", "2", "--app", "7").expect(t, ExitUsage, "Deploy release 2 to app #7?", "Pass --yes")
	if h.api.count("POST /services/7/container/releases/11/deploy") != 0 {
		t.Fatal("deployed without confirmation")
	}
	h.run("", "releases", "deploy", "#2", "--app", "7", "--yes").expect(t, ExitOK, "Release 2 is deploying to app #7 (pull #42)", "Deployed app #7 release 2 (sha256 aabbccddeeff).")

	h.run("", "releases", "deploy", "7", "--app", "7", "--yes").expect(t, ExitNotFound, "has no release 7")
	h.run("", "releases", "deploy", "1", "--app", "7", "--yes").expect(t, ExitConflict, "no longer kept")
	h.run("", "releases", "deploy", "two", "--app", "7", "--yes").expect(t, ExitUsage, "not a release number")
	h.run("", "releases", "deploy", "--app", "7", "--yes").expect(t, ExitUsage, "release number")

	h.api.answer("POST /services/7/container/releases/12/deploy", 202,
		`{"data":{"id":43,"status":"pending","is_active":true,"source":"upload","release":{"id":12,"number":3}}}`)
	h.run("", "releases", "deploy", "3", "--app", "7", "--yes", "--no-wait").expect(t, ExitOK, "Release 3 is deploying. Follow it with: talksasa deploy status")
}

func TestUploadRetryDelays(t *testing.T) {
	const s = 1000 // milliseconds, in units of RetryDelay
	want := []int{1, 2, 4, 8, 16, 30, 30, 30}
	for i, w := range want {
		if got := retryDelay(s, i+1); got != s*time.Duration(w) {
			t.Errorf("attempt %d: %v, want %v", i+1, got, s*time.Duration(w))
		}
	}
}
