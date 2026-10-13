package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func downloadImage(url string, data []byte) ManifestImage {
	sum := sha256.Sum256(data)
	return ManifestImage{
		URL:      url,
		SHA256:   hex.EncodeToString(sum[:]),
		Size:     int64(len(data)),
		Arch:     "amd64",
		Firmware: "uefi",
		Format:   "qcow2",
	}
}

func TestVerifyFile(t *testing.T) {
	dir := t.TempDir()
	data := []byte("verified-bytes-1234")
	im := downloadImage("https://example.com/a.qcow2", data)
	path := filepath.Join(dir, "a.qcow2")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(path, im); err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}
	if err := os.WriteFile(path, []byte("tampered-bytes-123"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(path, im); err == nil {
		t.Error("tampered file must fail verification")
	}
}

func TestAdmit_FileAndCacheHit(t *testing.T) {
	data := []byte("http-image-bytes")
	vmHome := t.TempDir()
	// Admit requires https or file; file:// exercises the full admit path
	// (verify → atomic copy → verified cache hit) without network.
	fileSrc := filepath.Join(t.TempDir(), "src.qcow2")
	if err := os.WriteFile(fileSrc, data, 0o644); err != nil {
		t.Fatal(err)
	}
	fileIm := downloadImage("file://"+fileSrc, data)
	got, err := Admit(vmHome, fileIm, DownloadOpts{})
	if err != nil {
		t.Fatalf("Admit file: %v", err)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("admitted file missing: %v", err)
	}
	// Second admission is a verified cache hit (source can vanish).
	if err := os.Remove(fileSrc); err != nil {
		t.Fatal(err)
	}
	got2, err := Admit(vmHome, fileIm, DownloadOpts{})
	if err != nil {
		t.Fatalf("Admit cache hit: %v", err)
	}
	if got2 != got {
		t.Errorf("cache hit path changed: %q vs %q", got2, got)
	}
}

func TestAdmit_HTTPDigestMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("wrong-bytes-here!"))
	}))
	defer srv.Close()
	vmHome := t.TempDir()
	im := downloadImage(srv.URL+"/tuna.qcow2", []byte("expected-bytes-12"))
	// Bypass the https gate: call admitHTTP directly with the test server.
	dest := filepath.Join(CacheDir(vmHome), im.CacheKey())
	if err := os.MkdirAll(CacheDir(vmHome), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := admitHTTP(dest, im, DownloadOpts{}); err == nil {
		t.Error("digest mismatch must refuse admission")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("refused download must not leave a cache entry")
	}
}

func TestAdmit_HTTPOverlong(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this payload is longer than declared"))
	}))
	defer srv.Close()
	vmHome := t.TempDir()
	im := downloadImage(srv.URL+"/tuna.qcow2", []byte("short"))
	dest := filepath.Join(CacheDir(vmHome), im.CacheKey())
	if err := os.MkdirAll(CacheDir(vmHome), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := admitHTTP(dest, im, DownloadOpts{}); err == nil || !strings.Contains(err.Error(), "length mismatch") {
		t.Errorf("overlong body must fail on length, got %v", err)
	}
}

func TestAdmit_PoisonedCacheReverified(t *testing.T) {
	vmHome := t.TempDir()
	data := []byte("good-bytes-123456")
	fileSrc := filepath.Join(t.TempDir(), "src.qcow2")
	if err := os.WriteFile(fileSrc, data, 0o644); err != nil {
		t.Fatal(err)
	}
	im := downloadImage("file://"+fileSrc, data)
	dest := CachedPath(vmHome, im)
	if err := os.MkdirAll(CacheDir(vmHome), 0o755); err != nil {
		t.Fatal(err)
	}
	// Plant a poisoned entry with the right name and wrong bytes.
	if err := os.WriteFile(dest, []byte("poisoned-bytes-12"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Admit(vmHome, im, DownloadOpts{})
	if err != nil {
		t.Fatalf("Admit should re-fetch over poison: %v", err)
	}
	if err := VerifyFile(got, im); err != nil {
		t.Errorf("admitted bytes still bad: %v", err)
	}
}

func TestAdmit_InvalidManifestRefused(t *testing.T) {
	im := ManifestImage{URL: "https://example.com/x.qcow2", SHA256: "short", Size: 10, Arch: "amd64", Firmware: "uefi"}
	if _, err := Admit(t.TempDir(), im, DownloadOpts{}); err == nil {
		t.Error("invalid manifest must be refused before any fetch")
	}
}

func TestVerifyFile_RefusesNonRegular(t *testing.T) {
	dir := t.TempDir()
	data := []byte("verified-bytes-1234")
	im := downloadImage("https://example.com/a.qcow2", data)
	real := filepath.Join(dir, "a.qcow2")
	if err := os.WriteFile(real, data, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.qcow2")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(link, im); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("symlink must be refused, got %v", err)
	}
	if err := VerifyFile(dir, im); err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Errorf("directory must be refused, got %v", err)
	}
}

func TestVerifyFile_RefusesOverlong(t *testing.T) {
	dir := t.TempDir()
	im := downloadImage("https://example.com/a.qcow2", []byte("short"))
	path := filepath.Join(dir, "a.qcow2")
	// Ten times the declared size: refused on length without hashing it all.
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 10*int(im.Size))), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(path, im); err == nil || !strings.Contains(err.Error(), "length mismatch") {
		t.Errorf("overlong file must fail on length, got %v", err)
	}
}

func TestAdmit_HTTPRedirectPolicy(t *testing.T) {
	payload := []byte("redirected-bytes!")
	// Plain http-to-http redirect: followed (the downgrade rule only
	// guards https origins).
	mux := http.NewServeMux()
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(payload) })
	mux.HandleFunc("/redir", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	im := downloadImage(srv.URL+"/redir", payload)
	vmHome := t.TempDir()
	dest := filepath.Join(CacheDir(vmHome), im.CacheKey())
	if err := os.MkdirAll(CacheDir(vmHome), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := admitHTTP(dest, im, DownloadOpts{}); err != nil {
		t.Errorf("http-to-http redirect must be followed: %v", err)
	}
}

// slowChunks is a finite reader that drips: 200 chunks at 20ms each (~4s
// total), so a 50ms cancellation must win long before the read completes.
type slowChunks struct {
	chunks int
	chunk  []byte
}

func (s *slowChunks) Read(p []byte) (int, error) {
	if s.chunks == 0 {
		return 0, io.EOF
	}
	time.Sleep(20 * time.Millisecond)
	s.chunks--
	return copy(p, s.chunk), nil
}

func TestCopyCtx_Cancelled(t *testing.T) {
	// Pre-cancelled: immediate error, zero bytes.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n, err := copyCtx(ctx, io.Discard, strings.NewReader("hello"), 6); err == nil || n != 0 {
		t.Errorf("pre-cancelled copy = %d, %v — want 0, context error", n, err)
	}
	// Mid-copy: cancellation stops the copy promptly instead of grinding
	// through the remaining ~4s of chunks.
	ctx, cancel = context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	n, err := copyCtx(ctx, io.Discard, &slowChunks{chunks: 200, chunk: make([]byte, copyChunk)}, 200*copyChunk)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("mid-copy cancellation = %d, %v — want context.Canceled", n, err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("cancelled copy took %s — cancellation did not stop it", elapsed)
	}
}

func TestVerifyFileCtx_Cancelled(t *testing.T) {
	dir := t.TempDir()
	data := []byte("verified-bytes-1234")
	im := downloadImage("https://example.com/a.qcow2", data)
	path := filepath.Join(dir, "a.qcow2")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := VerifyFileCtx(ctx, path, im); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("cancelled verification must report interruption, got %v", err)
	}
}

func TestAdmit_CancelledCtx(t *testing.T) {
	data := []byte("cancel-me-bytes!")
	vmHome := t.TempDir()
	fileSrc := filepath.Join(t.TempDir(), "src.qcow2")
	if err := os.WriteFile(fileSrc, data, 0o644); err != nil {
		t.Fatal(err)
	}
	im := downloadImage("file://"+fileSrc, data)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Admit(vmHome, im, DownloadOpts{Ctx: ctx}); err == nil {
		t.Error("admission under a cancelled context must fail")
	}
}

func TestVerifyFile_RefusesFIFO(t *testing.T) {
	dir := t.TempDir()
	im := downloadImage("https://example.com/a.qcow2", []byte("x"))
	fifo := filepath.Join(dir, "trap.fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	// Must refuse promptly: opening a FIFO for read would block forever.
	done := make(chan error, 1)
	go func() { done <- VerifyFile(fifo, im) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "non-regular") {
			t.Errorf("FIFO must be refused as non-regular, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("VerifyFile hung on a FIFO")
	}
}

func TestVerifyFile_SwapRaceBounded(t *testing.T) {
	dir := t.TempDir()
	data := []byte("stable-verified-content!!")
	im := downloadImage("https://example.com/a.qcow2", data)
	fifo := filepath.Join(dir, "trap.fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "race.qcow2")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// Swap the path between good content and a symlink-to-FIFO as fast as
	// possible while verification hammers it: no call may hang (each is
	// ctx-bounded) and none may admit wrong bytes (nil requires the proven
	// digest). Hitting the swap window is probabilistic; the bounds hold
	// deterministically.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Remove(path)
			_ = os.Symlink(fifo, path)
			_ = os.Remove(path)
			_ = os.WriteFile(path, data, 0o644)
		}
	}()
	defer func() { close(stop); wg.Wait() }()
	deadline := time.Now().Add(60 * time.Second)
	for i := 0; i < 200; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := VerifyFileCtx(ctx, path, im)
		cancel()
		if err != nil && strings.Contains(err.Error(), "deadline exceeded") {
			t.Fatalf("call %d hung past its context bound: %v", i, err)
		}
		// err == nil admitted the good content (digest proven); any other
		// error is a legitimate refusal (missing/symlink/FIFO/replaced).
	}
	if time.Now().After(deadline) {
		t.Error("swap-race verification took too long — a hang slipped through")
	}
}

func TestAdmitHTTP_TimeoutIndependentOfClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hang until the client goes away: a Close-proof stall.
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
			_, _ = w.Write([]byte("too-late"))
		}
	}))
	defer srv.Close()
	im := downloadImage(srv.URL+"/tuna.qcow2", []byte("expected-bytes-12"))
	vmHome := t.TempDir()
	dest := filepath.Join(CacheDir(vmHome), im.CacheKey())
	if err := os.MkdirAll(CacheDir(vmHome), 0o755); err != nil {
		t.Fatal(err)
	}
	// A supplied client with Timeout 0: the fetch must still obey
	// opts.Timeout via the request context.
	start := time.Now()
	err := admitHTTP(dest, im, DownloadOpts{Client: &http.Client{}, Timeout: 200 * time.Millisecond})
	if err == nil {
		t.Fatal("hung fetch must fail on the timeout")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("hung fetch took %s — opts.Timeout was discarded", elapsed)
	}
	if _, serr := os.Stat(dest); !os.IsNotExist(serr) {
		t.Error("failed fetch must not leave a cache entry")
	}
}

func TestAdmit_HTTPSDowngradeRefused(t *testing.T) {
	payload := []byte("downgraded-bytes!!")
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer plain.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/final", http.StatusFound)
	}))
	defer tls.Close()
	im := downloadImage(tls.URL+"/redir", payload)
	vmHome := t.TempDir()
	dest := filepath.Join(CacheDir(vmHome), im.CacheKey())
	if err := os.MkdirAll(CacheDir(vmHome), 0o755); err != nil {
		t.Fatal(err)
	}
	client := tls.Client() // trusts the test cert; CheckRedirect nil → policy applies
	if err := admitHTTP(dest, im, DownloadOpts{Client: client}); err == nil ||
		!strings.Contains(err.Error(), "downgrade") {
		t.Errorf("https-to-http redirect must be refused as a downgrade, got %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("refused download must not leave a cache entry")
	}
}
