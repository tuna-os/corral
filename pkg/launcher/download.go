package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CacheDir joins the launcher cache under a VM home: <vmHome>/cache/launcher.
// Verified images land here keyed by digest, never by URL basename.
func CacheDir(vmHome string) string {
	return filepath.Join(vmHome, "cache", "launcher")
}

// CachedPath is where a verified image for im must live. The name carries
// the digest so a cache hit is self-identifying — but the bytes are still
// re-verified on every hit, because a filename proves nothing about content.
func CachedPath(vmHome string, im ManifestImage) string {
	return filepath.Join(CacheDir(vmHome), im.CacheKey())
}

// openAdmittedFile opens path for verified reading: the Lstat must show a
// regular file (symlinks refused without following), and the OPEN descriptor
// must be regular and the SAME file the Lstat saw (os.SameFile: device +
// inode). The second check closes the Lstat → Open swap window — a path
// replaced by a FIFO or a symlink between the two would otherwise defeat the
// regular-file refusal or hang the hash. Callers hash and copy from the
// returned descriptor, never by re-opening the path.
func openAdmittedFile(path string) (*os.File, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing symlink %s — verified images must be regular files", path)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing non-regular file %s (%s) — verified images must be regular files", path, fi.Mode())
	}
	// Non-blocking open: a path swapped for a FIFO after the Lstat must
	// reach the descriptor checks below, not hang the open itself.
	f, err := openFileRead(path)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !opened.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("refusing non-regular file %s (%s, swapped after admission check) — verified images must be regular files", path, opened.Mode())
	}
	if !os.SameFile(fi, opened) {
		_ = f.Close()
		return nil, fmt.Errorf("refusing %s: replaced between admission check and open — verified images must be immutable at the descriptor boundary", path)
	}
	return f, nil
}

// copyChunk bounds one cancellable copy step: ctx is honored at least this
// often during hashing and copies.
const copyChunk = 32 * 1024

// copyCtx copies up to limit bytes from src to dst, checking ctx around
// every chunk: before each read, after each read (before the bytes are
// written), after each write, and at EOF and limit completion. Size+1
// bounds bytes; these checks bound ADMISSION — a cancellation landing in
// the final read or write fails the copy instead of returning a successful
// hash over bytes the caller no longer wants. What the checks cannot do is
// preempt a single blocking Read or Write: ordinary regular-file I/O has no
// hard per-call bound, so callers needing an absolute operation budget wrap
// ctx with a deadline (admitLocal and admitHTTP both do).
func copyCtx(ctx context.Context, dst io.Writer, src io.Reader, limit int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var total int64
	buf := make([]byte, copyChunk)
	for total < limit {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		want := int64(len(buf))
		if remaining := limit - total; remaining < want {
			want = remaining
		}
		n, rerr := src.Read(buf[:want])
		if n > 0 {
			// Cancelled during the read: the bytes are discarded —
			// not written, not counted — and the copy fails.
			if err := ctx.Err(); err != nil {
				return total, err
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
			if err := ctx.Err(); err != nil {
				return total, err
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				// EOF is admission: a cancellation that landed in
				// the final read must fail, not succeed.
				if err := ctx.Err(); err != nil {
					return total, err
				}
				return total, nil
			}
			return total, rerr
		}
	}
	// Limit completion is admission too.
	if err := ctx.Err(); err != nil {
		return total, err
	}
	return total, nil
}

// hashAdmitted hashes the admitted descriptor against exact length + digest
// without loading it whole into memory. The read is capped at Size+1, so a
// growing source is refused on length instead of hashing unbounded input.
// The context is re-checked before the digest comparison: a cancellation
// that landed with the final bytes fails verification instead of blessing
// a hash the caller no longer wants.
func hashAdmitted(ctx context.Context, f *os.File, path string, im ManifestImage) error {
	h := sha256.New()
	n, err := copyCtx(ctx, h, f, im.Size+1)
	if err != nil {
		return fmt.Errorf("hashing %s: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("hashing %s interrupted: %w", path, err)
	}
	if n != im.Size {
		return fmt.Errorf("length mismatch for %s: got %d bytes, manifest wants %d", path, n, im.Size)
	}
	if hex.EncodeToString(h.Sum(nil)) != im.SHA256 {
		return fmt.Errorf("digest mismatch for %s: does not match manifest sha256 %s", path, im.SHA256)
	}
	return nil
}

// VerifyFile checks an on-disk file against exact length + digest without
// loading it whole into memory. See VerifyFileCtx for the contract.
func VerifyFile(path string, im ManifestImage) error {
	return VerifyFileCtx(context.Background(), path, im)
}

// VerifyFileCtx is VerifyFile under ctx (admission cancels the hash).
// Only regular files are admitted — symlinks, FIFOs, directories and
// devices are refused before a byte is read, so a poisoned cache entry or
// a FIFO that never ends cannot hang or mislead verification — the
// descriptor is pinned against swap-after-check, the read is capped at
// Size+1, and cancellation stops the hash promptly.
func VerifyFileCtx(ctx context.Context, path string, im ManifestImage) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("verification of %s interrupted: %w", path, err)
	}
	f, err := openAdmittedFile(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return hashAdmitted(ctx, f, path, im)
}

// DownloadOpts bounds the network fetch. Zero values take safe defaults.
type DownloadOpts struct {
	// Timeout bounds the whole fetch. Default 30 minutes.
	Timeout time.Duration
	// Client overrides the HTTP client (for tests). Nil uses a default with
	// Timeout applied. A provided client keeps its own CheckRedirect when
	// set; otherwise the HTTPS-no-downgrade policy below applies.
	Client *http.Client
	// Ctx cancels the fetch. Nil uses context.Background.
	Ctx context.Context
}

func downloadCtx(opts DownloadOpts) context.Context {
	if opts.Ctx != nil {
		return opts.Ctx
	}
	return context.Background()
}

// Admit downloads im into the launcher cache and returns the verified local
// path. Fail-closed contract:
//
//   - cache hits are re-verified (length + digest); a mismatch deletes the
//     entry and re-downloads rather than trusting the filename.
//   - network fetches enforce https (file:// is a local copy, still
//     verified), an exact byte budget (Size+1 — one byte over is refusal,
//     not truncation), a timeout, a no-downgrade redirect policy (https
//     never redirects to http), regular-file-only inputs, checked temp
//     closes, and atomic admission via temp+rename so a torn download
//     never poisons the cache.
//   - unverified bytes are never returned: any error refuses admission.
//     Admission is cancellable via DownloadOpts.Ctx.
func Admit(vmHome string, im ManifestImage, opts DownloadOpts) (string, error) {
	if err := im.Validate(); err != nil {
		return "", err
	}
	cacheDir := CacheDir(vmHome)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("creating cache dir: %w", err)
	}
	dest := CachedPath(vmHome, im)
	if _, err := os.Stat(dest); err == nil {
		if err := VerifyFileCtx(downloadCtx(opts), dest, im); err == nil {
			return dest, nil
		} else {
			// A poisoned cache entry must not survive: remove and re-fetch.
			_ = os.Remove(dest)
		}
	}
	if strings.HasPrefix(im.URL, "file://") {
		return dest, admitLocal(im.URL[len("file://"):], dest, im, opts)
	}
	return dest, admitHTTP(dest, im, opts)
}

func admitLocal(src, dest string, im ManifestImage, opts DownloadOpts) error {
	ctx := downloadCtx(opts)
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("local image admission interrupted: %w", err)
	}
	// Local admission gets the same absolute budget as the network path:
	// DownloadOpts.Timeout bounds the whole verify+copy, so a stalled
	// filesystem cannot hold admission open past it. (Chunk checks alone
	// cannot preempt one blocking Read — see copyCtx.)
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// One admitted descriptor for verify AND copy: the file is pinned at
	// open (regular + same-file), verified from the descriptor, rewound,
	// and copied from the same descriptor — there is no second open for a
	// swap to slip through, and the copy re-hashes with the same Size+1
	// budget, so a source mutated between verify and copy is refused.
	in, err := openAdmittedFile(src)
	if err != nil {
		return fmt.Errorf("local image failed verification: %w", err)
	}
	defer func() { _ = in.Close() }()
	if err := hashAdmitted(ctx, in, src, im); err != nil {
		return fmt.Errorf("local image failed verification: %w", err)
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("local image failed verification: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".admit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	h := sha256.New()
	n, err := copyCtx(ctx, io.MultiWriter(tmp, h), in, im.Size+1)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copying local image: %w", err)
	}
	// A failed close can mean buffered bytes never reached the disk — the
	// admission is only as proven as this close.
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("copying local image: %w", err)
	}
	if n != im.Size || hex.EncodeToString(h.Sum(nil)) != im.SHA256 {
		return fmt.Errorf("local image changed during copy — refused")
	}
	// Pre-publication: the rename is the admission. A cancellation that
	// landed with the final bytes must refuse, not publish.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("local image admission interrupted: %w", err)
	}
	return os.Rename(tmpName, dest)
}

// redirectPolicy refuses anything but HTTP(S) redirects, refuses HTTPS to
// HTTP downgrades (the manifest's HTTPS-only contract survives redirects),
// and keeps the standard 10-redirect cap.
func redirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopping after 10 redirects (last: %s)", req.URL)
	}
	if req.URL.Scheme != "https" && req.URL.Scheme != "http" {
		return fmt.Errorf("refusing redirect to non-HTTP(S) URL %q", req.URL)
	}
	if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("refusing redirect from HTTPS to %q (downgrade attack?)", req.URL)
	}
	return nil
}

func admitHTTP(dest string, im ManifestImage, opts DownloadOpts) error {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	if client.CheckRedirect == nil {
		client.CheckRedirect = redirectPolicy
	}
	// The timeout is enforced by the request context independently of the
	// client's own Timeout: a supplied client with Timeout 0 must not
	// inherit an unbounded fetch.
	ctx, cancel := context.WithTimeout(downloadCtx(opts), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, im.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", im.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %d", im.URL, resp.StatusCode)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".admit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	h := sha256.New()
	// Size+1 budget: exactly Size passes, one byte over fails. The limit
	// reader proves length without trusting Content-Length.
	limited := io.LimitReader(resp.Body, im.Size+1)
	n, err := io.Copy(io.MultiWriter(tmp, h), limited)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("downloading %s: %w", im.URL, err)
	}
	// A failed close can mean buffered bytes never reached the disk — the
	// admission is only as proven as this close. (The response-body close
	// stays best-effort: length and digest are already proven from the
	// bytes in hand, so a body-close failure cannot corrupt the cache.)
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("downloading %s: %w", im.URL, err)
	}
	if n != im.Size {
		return fmt.Errorf("length mismatch for %s: got %d bytes, manifest wants %d", im.URL, n, im.Size)
	}
	if hex.EncodeToString(h.Sum(nil)) != im.SHA256 {
		return fmt.Errorf("digest mismatch for %s: does not match manifest sha256 %s", im.URL, im.SHA256)
	}
	// Pre-publication: the rename is the admission. A cancellation that
	// landed with the final bytes must refuse, not publish.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("downloading %s interrupted: %w", im.URL, err)
	}
	return os.Rename(tmpName, dest)
}
