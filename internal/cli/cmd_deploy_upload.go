package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/samuelmacharia99/talksasa-cli/internal/api"
	"github.com/samuelmacharia99/talksasa-cli/internal/bundle"
)

// deploy --dir: pack a folder, send it in resumable chunks, and deploy it
// through the same pull pipeline as a Git pull.

const (
	// uploadRetries is how many times one request is tried again after a
	// network failure, a busy platform or a rate limit.
	uploadRetries = 8
	// maxRetryAfter is the longest Retry-After waited out; a platform that
	// asks for longer is reported instead.
	maxRetryAfter = 2 * time.Minute
	// progressEvery throttles the redrawn progress line.
	progressEvery = 200 * time.Millisecond
)

func validateDirFlags(opts *deployOptions) error {
	if opts.replace {
		return &usageError{msg: "--dir and --replace do not go together: --replace clones a Git repository fresh"}
	}
	if strings.TrimSpace(opts.dir) == "" {
		return &usageError{msg: "--dir needs a folder, such as: talksasa deploy --dir ."}
	}
	st, err := os.Stat(opts.dir)
	if err != nil || !st.IsDir() {
		return &usageError{msg: fmt.Sprintf("--dir %s is not a folder", opts.dir)}
	}
	return nil
}

func (a *App) deployDir(ctx context.Context, opts *deployOptions) error {
	if opts.dryRun {
		return a.dryRunBundle(ctx, opts.dir)
	}

	c, t, err := a.appClient(ctx)
	if err != nil {
		return err
	}
	_, status, err := c.PullStatus(ctx, t.ID)
	if err != nil {
		return err
	}
	source := ""
	if status.CodeSource != nil {
		source = *status.CodeSource
	}
	if source == api.SourceGit && !opts.replaceGit {
		return withExit(ExitConflict, fmt.Errorf("%s deploys from its Git repository (%s). To deploy this folder instead, pass --replace-git: "+
			"it disconnects the repository and turns off auto-deploy, and needs a token with the Destructive permission", t, str(status.Repository.URL)))
	}

	sum, err := a.packBundle(ctx, opts.dir)
	if err != nil {
		return err
	}
	defer sum.Remove()

	if err := a.confirmDirDeploy(t, source, opts.replaceGit); err != nil {
		return err
	}

	f, err := os.Open(sum.Path)
	if err != nil {
		return err
	}
	defer f.Close()

	u := &uploader{a: a, c: c, t: t, sum: sum, file: f, replaceGit: opts.replaceGit}
	u.progress = &progressLine{w: a.Stderr, enabled: a.StderrIsTerminal && !a.flags.json}
	resp, pull, err := u.run(ctx, api.UploadDeployOptions{
		RunComposer:   !opts.noComposer,
		RunMigrations: !opts.noMigrations,
		ForceRebuild:  opts.rebuild,
		ReplaceGit:    opts.replaceGit,
	})
	if err != nil {
		return err
	}

	release := ""
	if pull.Release != nil && pull.Release.Number != nil {
		release = fmt.Sprintf("Release %d", *pull.Release.Number)
	}
	if opts.noWait {
		if a.flags.json {
			return a.printBody(resp)
		}
		if release == "" {
			a.out("The upload is deploying (pull #%d). Follow it with: talksasa deploy status", pull.ID)
		} else {
			a.out("%s is deploying. Follow it with: talksasa deploy status", release)
		}
		return nil
	}
	if !a.flags.json {
		if release == "" {
			release = "The upload"
		}
		a.note("%s is deploying to %s (pull #%d).", release, t, pull.ID)
	}
	return a.waitForPull(ctx, c, t, pull.ID, opts.timeout)
}

// packBundle packs the folder and reports what went in.
func (a *App) packBundle(ctx context.Context, dir string) (*bundle.Summary, error) {
	sum, err := bundle.Pack(ctx, dir, bundle.Options{})
	if err != nil {
		var limit *bundle.LimitError
		var input *bundle.InputError
		switch {
		case errors.As(err, &limit), errors.As(err, &input):
			return nil, withExit(ExitInvalid, err)
		case errors.Is(err, context.Canceled):
			return nil, withExit(ExitInterrupted, errors.New("interrupted while packing; nothing was sent"))
		}
		return nil, fmt.Errorf("pack %s: %w", dir, err)
	}
	if !a.flags.json {
		a.printBundleSummary(dir, sum)
	}
	return sum, nil
}

func (a *App) printBundleSummary(dir string, sum *bundle.Summary) {
	line := fmt.Sprintf("Packed %s: %d files in %d folders, %s unpacked, %s compressed",
		dir, sum.FileCount, sum.FolderCount, bytesHuman(sum.UnpackedBytes), bytesHuman(sum.Size))
	var skipped []string
	if sum.SkippedLinks > 0 {
		skipped = append(skipped, plural(sum.SkippedLinks, "link", "links"))
	}
	if sum.SkippedOther > 0 {
		skipped = append(skipped, plural(sum.SkippedOther, "device, socket or pipe", "devices, sockets or pipes"))
	}
	if len(skipped) > 0 {
		line += "; skipped " + strings.Join(skipped, " and ")
	}
	a.note("%s.", line)
	if len(sum.LinkExamples) > 0 {
		more := ""
		if sum.SkippedLinks > len(sum.LinkExamples) {
			more = ", …"
		}
		a.note("Symbolic links are never followed or uploaded: %s%s", strings.Join(sum.LinkExamples, ", "), more)
	}
	if len(sum.EnvFiles) > 0 {
		a.note("Warning: these files usually hold secrets and will be uploaded: %s", strings.Join(sum.EnvFiles, ", "))
		a.note("  The app's environment comes from its Environment tab (talksasa env), not from the folder.")
		a.note("  Leave them out with a line in .talksasaignore, such as: .env.*")
	}
	if len(sum.RootMarkers) == 0 {
		a.note("Warning: %s has none of %s at its top. Is it the project's root folder?", dir, strings.Join(bundle.RootMarkerNames, ", "))
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// dryRunBundle packs the folder to report on it, and sends nothing.
func (a *App) dryRunBundle(ctx context.Context, dir string) error {
	sum, err := a.packBundle(ctx, dir)
	if err != nil {
		return err
	}
	defer sum.Remove()
	if a.flags.json {
		return a.printJSON(struct {
			DryRun  bool            `json:"dry_run"`
			Summary *bundle.Summary `json:"summary"`
			Files   []bundle.File   `json:"files"`
		}{true, sum, sum.Files})
	}
	for _, f := range sum.Files {
		a.out("%s", f.Path)
	}
	a.note("Dry run: nothing was uploaded.")
	return nil
}

// confirmDirDeploy asks before replacing the app's code with the folder,
// and always before disconnecting its Git repository.
func (a *App) confirmDirDeploy(t target, source string, replaceGit bool) error {
	var parts []string
	if replaceGit {
		parts = append(parts, fmt.Sprintf("This disconnects %s's Git repository and turns off auto-deploy.", t))
	}
	switch {
	case source != api.SourceUpload:
		parts = append(parts, fmt.Sprintf("Replace the code of %s with this folder? Files in the app that are not in the folder are removed; storage, uploads and settings are kept.", t))
	case replaceGit:
		parts = append(parts, "Continue?")
	}
	if len(parts) == 0 {
		return nil
	}
	return a.confirm(strings.Join(parts, " "))
}

// uploader sends one bundle and asks for its deploy.
type uploader struct {
	a          *App
	c          *api.Client
	t          target
	sum        *bundle.Summary
	file       io.ReaderAt
	replaceGit bool
	progress   *progressLine

	// sent is how far the platform had the bundle when last heard from.
	sent int64
}

// run creates (or resumes) the upload, sends what the platform lacks, and
// deploys it. An upload the platform let expire or cancelled is started
// again once.
func (u *uploader) run(ctx context.Context, opts api.UploadDeployOptions) (*api.Response, api.Pull, error) {
	restarted := false
	for {
		up, err := u.create(ctx)
		if err != nil {
			return nil, api.Pull{}, u.explain(ctx, err)
		}
		resends := 0
		for {
			err = u.send(ctx, &up)
			if err == nil {
				var resp *api.Response
				var pull api.Pull
				resp, pull, err = u.deploy(ctx, up, opts)
				if err == nil {
					return resp, pull, nil
				}
				// The platform has less than all of it: send the rest.
				var apiErr *api.Error
				if errors.As(err, &apiErr) && apiErr.Status == 409 && apiErr.Text("status") == api.UploadUploading && resends < 2 {
					if n, ok := apiErr.Int64("received_bytes"); ok && n >= 0 && n < up.SizeBytes {
						resends++
						up.ReceivedBytes = n
						continue
					}
				}
			}
			if !restarted && uploadGone(err) {
				restarted = true
				u.note("The platform no longer has this upload (%s); starting it again.", err)
				break
			}
			return nil, api.Pull{}, u.explain(ctx, err)
		}
	}
}

// uploadGone reports a refusal because the upload expired or was cancelled.
func uploadGone(err error) bool {
	var apiErr *api.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 409 {
		return false
	}
	s := apiErr.Text("status")
	return s == api.UploadExpired || s == api.UploadCancelled
}

// explain turns a failure into what the user can do about it.
func (u *uploader) explain(ctx context.Context, err error) error {
	size := u.sum.Size
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		if u.sent > 0 && u.sent < size {
			return withExit(ExitInterrupted, fmt.Errorf("interrupted at %s of %s; the platform keeps what it has for a while. Run the same command again to resume.",
				bytesHuman(u.sent), bytesHuman(size)))
		}
		return withExit(ExitInterrupted, errors.New("interrupted. Run the same command again to resume."))
	}
	var apiErr *api.Error
	if errors.As(err, &apiErr) && apiErr.Code == "git_connected" && !u.replaceGit {
		return fmt.Errorf("%w\nPass --replace-git to disconnect the repository and deploy this folder (needs a token with the Destructive permission).", err)
	}
	if u.sent > 0 && u.sent < size && isTransient(err) {
		return fmt.Errorf("%w\nThe upload stopped at %s of %s. Run the same command again to resume.", err, bytesHuman(u.sent), bytesHuman(size))
	}
	return err
}

func (u *uploader) create(ctx context.Context) (api.Upload, error) {
	unpacked, files := u.sum.UnpackedBytes, u.sum.FileCount
	req := api.NewUpload{
		SizeBytes:     u.sum.Size,
		SHA256:        u.sum.SHA256,
		UnpackedBytes: &unpacked,
		FileCount:     &files,
		ReplaceGit:    u.replaceGit,
	}
	var resp *api.Response
	var up api.Upload
	err := u.retry(ctx, func(ctx context.Context) error {
		var err error
		resp, up, err = u.c.CreateUpload(ctx, u.t.ID, req)
		return err
	})
	if err != nil {
		return up, err
	}

	switch {
	case up.ID <= 0 || up.SizeBytes != u.sum.Size || !strings.EqualFold(up.SHA256, u.sum.SHA256):
		return up, fmt.Errorf("the platform answered with a different upload (#%d, %d bytes, sha256 %s) than the one asked for; try again", up.ID, up.SizeBytes, shortCommit(up.SHA256))
	case up.ChunkBytes <= 0:
		return up, fmt.Errorf("the platform did not say how large a chunk may be (chunk_bytes %d)", up.ChunkBytes)
	case up.ReceivedBytes < 0 || up.ReceivedBytes > up.SizeBytes:
		return up, fmt.Errorf("the platform reports %d of %d bytes received, which cannot be", up.ReceivedBytes, up.SizeBytes)
	case up.Status != "" && up.Status != api.UploadUploading:
		return up, fmt.Errorf("the platform answered with an upload that is %s, not uploading; try again", up.Status)
	}
	u.sent = up.ReceivedBytes
	if resp.Status == 200 && up.ReceivedBytes > 0 {
		u.note("Resuming upload at %s of %s.", bytesHuman(up.ReceivedBytes), bytesHuman(up.SizeBytes))
	}
	return up, nil
}

// send uploads the bundle from up.ReceivedBytes to the end, in chunks of
// the size the platform gave, following where the platform says it is.
func (u *uploader) send(ctx context.Context, up *api.Upload) error {
	size, chunk := up.SizeBytes, up.ChunkBytes
	offset := up.ReceivedBytes
	failures, stalls := 0, 0

	u.progress.begin(offset, size)
	defer u.progress.end()

	for offset < size {
		n := min(chunk, size-offset)
		body := &progressReader{r: io.NewSectionReader(u.file, offset, n), progress: u.progress, at: offset}
		_, res, err := u.c.UploadChunk(ctx, u.t.ID, up.ID, offset, body, n)
		if err == nil {
			if res.ReceivedBytes < 0 || res.ReceivedBytes > size {
				return fmt.Errorf("the platform reports %d of %d bytes received, which cannot be", res.ReceivedBytes, size)
			}
			if res.ReceivedBytes <= offset {
				// Acknowledged without moving on: a chunk it already had.
				if stalls++; stalls > uploadRetries {
					return fmt.Errorf("the platform stopped taking the upload at %s of %s", bytesHuman(offset), bytesHuman(size))
				}
			} else {
				stalls, failures = 0, 0
			}
			offset = res.ReceivedBytes
			u.sent = offset
			up.ReceivedBytes = offset
			u.progress.set(offset)
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		var apiErr *api.Error
		if errors.As(err, &apiErr) {
			received, hasReceived := apiErr.Int64("received_bytes")
			hasReceived = hasReceived && received >= 0 && received <= size
			switch {
			case apiErr.Status == 409 && hasReceived:
				// Out of order: carry on from where the platform is.
				if stalls++; stalls > uploadRetries {
					return err
				}
				offset, u.sent, up.ReceivedBytes = received, received, received
				u.progress.set(offset)
				continue
			case apiErr.Status == 409:
				switch apiErr.Text("status") {
				case api.UploadReceived, api.UploadStored, api.UploadDeployed:
					// It already has the whole bundle, checked.
					offset, u.sent, up.ReceivedBytes = size, size, size
					continue
				}
				return err // expired or cancelled: the caller starts again
			case apiErr.Code == "validation_failed" && hasReceived:
				// The chunk was refused and cut back; send it again,
				// smaller if the platform says so.
				if failures++; failures > uploadRetries {
					return err
				}
				if limit, ok := apiErr.Int64("max_chunk_bytes"); ok && limit > 0 && limit < chunk {
					chunk = limit
				}
				offset, u.sent, up.ReceivedBytes = received, received, received
				u.progress.set(offset)
				continue
			}
		}
		if !isTransient(err) {
			return err
		}
		if failures++; failures > uploadRetries {
			return err
		}
		if werr := u.wait(ctx, failures, err); werr != nil {
			return werr
		}
	}
	return nil
}

func (u *uploader) deploy(ctx context.Context, up api.Upload, opts api.UploadDeployOptions) (*api.Response, api.Pull, error) {
	var resp *api.Response
	var pull api.Pull
	err := u.retry(ctx, func(ctx context.Context) error {
		var err error
		resp, pull, err = u.c.DeployUpload(ctx, u.t.ID, up.ID, opts)
		return err
	})
	if err == nil && pull.ID <= 0 {
		err = errors.New("the platform accepted the deploy but did not say which pull runs it; follow it with: talksasa deploy status")
	}
	return resp, pull, err
}

// retry calls call until it succeeds, fails for good, or has been retried
// uploadRetries times. Creating an upload and asking for its deploy are
// both safe to repeat.
func (u *uploader) retry(ctx context.Context, call func(context.Context) error) error {
	for attempt := 1; ; attempt++ {
		err := call(ctx)
		if err == nil || ctx.Err() != nil || !isTransient(err) || attempt > uploadRetries {
			return err
		}
		if werr := u.wait(ctx, attempt, err); werr != nil {
			return werr
		}
	}
}

// wait sleeps before retry number attempt (1-based): RetryDelay doubling to
// 30 times itself, or longer when the platform's Retry-After says so.
func (u *uploader) wait(ctx context.Context, attempt int, cause error) error {
	delay := retryDelay(u.a.RetryDelay, attempt)
	var apiErr *api.Error
	if errors.As(cause, &apiErr) && apiErr.RetryAfter > delay {
		if apiErr.RetryAfter > maxRetryAfter {
			return cause
		}
		delay = apiErr.RetryAfter
	}
	u.note("%s; trying again in %s (%d of %d).", strings.TrimSuffix(cause.Error(), "."), delay.Round(time.Millisecond), attempt, uploadRetries)

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryDelay(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	delay := base
	for i := 1; i < attempt && delay < 30*base; i++ {
		delay *= 2
	}
	return min(delay, 30*base)
}

// note prints a line on stderr around the progress line (never in --json).
func (u *uploader) note(format string, args ...any) {
	if u.a.flags.json {
		return
	}
	u.progress.pause(func() { u.a.note(format, args...) })
}

// progressLine is a single redrawn "Uploading" line on a terminal. The
// HTTP transport reads chunk bodies on its own goroutine, so it locks.
type progressLine struct {
	mu      sync.Mutex
	w       io.Writer
	enabled bool
	active  bool
	total   int64
	done    int64
	drawn   time.Time
	width   int
}

func (p *progressLine) begin(done, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active, p.done, p.total = true, done, total
	p.draw(true)
}

func (p *progressLine) set(done int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.active {
		return
	}
	p.done = done
	p.draw(done >= p.total)
}

// end finishes the line, leaving the last state shown.
func (p *progressLine) end() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active && p.enabled && p.width > 0 {
		p.draw(true)
		fmt.Fprintln(p.w)
	}
	p.active, p.width = false, 0
}

// pause clears the line, runs print, and draws the line again.
func (p *progressLine) pause(print func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.enabled && p.width > 0 {
		fmt.Fprintf(p.w, "\r%s\r", strings.Repeat(" ", p.width))
		p.width = 0
	}
	print()
	if p.active {
		p.draw(true)
	}
}

func (p *progressLine) draw(force bool) {
	if !p.enabled || p.total <= 0 {
		return
	}
	if !force && time.Since(p.drawn) < progressEvery {
		return
	}
	p.drawn = time.Now()
	line := fmt.Sprintf("Uploading %s of %s (%d%%)", bytesHuman(p.done), bytesHuman(p.total), p.done*100/p.total)
	pad := ""
	if len(line) < p.width {
		pad = strings.Repeat(" ", p.width-len(line))
	}
	fmt.Fprintf(p.w, "\r%s%s", line, pad)
	p.width = len(line)
}

// progressReader reports a chunk's bytes as the transport reads them.
type progressReader struct {
	r        io.Reader
	progress *progressLine
	at       int64
}

func (r *progressReader) Read(b []byte) (int, error) {
	n, err := r.r.Read(b)
	r.at += int64(n)
	r.progress.set(r.at)
	return n, err
}
