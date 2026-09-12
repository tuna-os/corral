package qemu

import (
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// Frame is one captured framebuffer and what can be said about it without a
// human looking.
type Frame struct {
	Path   string  `json:"path"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	StdDev float64 `json:"stddev"`
}

// BlankStdDev is the standard deviation below which a frame counts as blank.
//
// The number comes from TunaOS's iso-e2e.sh, which measures the same thing
// with ImageMagick and calls anything at or under 0.02 unpainted. A uniform
// screen — black, or a single flat colour — has a deviation of zero, and a
// real desktop or a text console sits orders of magnitude above this.
const BlankStdDev = 0.02

// Blank reports whether the frame shows nothing rendered. A blank frame after
// a boot that reported success is the failure mode screenshots exist to catch:
// the guest is up, the compositor never painted.
func (f Frame) Blank() bool { return f.StdDev <= BlankStdDev }

// Capture writes the running VM's framebuffer to outPath as a PNG and reports
// what is in it. Screenshot is this without the verdict.
func Capture(name, outPath string) (Frame, error) {
	vmDir := filepath.Join(VMHome(), name)
	sockPath := filepath.Join(vmDir, "qmp.sock")
	if _, err := os.Stat(sockPath); err != nil {
		return Frame{}, fmt.Errorf("no QMP socket for %q — is it running? if it was created with an older corral, recreate it (corral create --force ...) to pick up QMP support", name)
	}

	conn, reader, err := qmpDial(sockPath)
	if err != nil {
		return Frame{}, err
	}
	defer func() { _ = conn.Close() }()

	// screendump writes a PPM file directly via the QEMU process — since the
	// QEMU backend and the corral CLI share a host, a path next to the VM's
	// other state is reachable from both sides.
	ppmPath := filepath.Join(vmDir, "screenshot.ppm")
	defer func() { _ = os.Remove(ppmPath) }()
	if _, err := qmpExecute(conn, reader, "screendump", map[string]any{"filename": ppmPath}); err != nil {
		return Frame{}, fmt.Errorf("screendump: %w", err)
	}

	img, err := decodePPM(ppmPath)
	if err != nil {
		return Frame{}, fmt.Errorf("decoding screendump: %w", err)
	}

	if outPath == "" {
		outPath = name + "-screenshot.png"
	}
	if dir := filepath.Dir(outPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Frame{}, err
		}
	}
	f, err := os.Create(outPath)
	if err != nil {
		return Frame{}, fmt.Errorf("creating %s: %w", outPath, err)
	}
	// Closed rather than deferred-and-ignored: the encode writes through a
	// buffer, so a full disk shows up in Close and nowhere else. A truncated
	// PNG that the result calls evidence is worse than no PNG.
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		return Frame{}, fmt.Errorf("encoding %s: %w", outPath, err)
	}
	if err := f.Close(); err != nil {
		return Frame{}, fmt.Errorf("writing %s: %w", outPath, err)
	}
	bounds := img.Bounds()
	return Frame{
		Path:   outPath,
		Width:  bounds.Dx(),
		Height: bounds.Dy(),
		StdDev: stdDev(img),
	}, nil
}

// stdDev returns the standard deviation of the frame's luminance, normalised
// to 0..1. Sampled on a grid rather than per pixel: a 1920x1080 frame captured
// every two seconds is 2M samples a frame, and every fourth row and column
// answers "did anything paint" identically.
func stdDev(img image.Image) float64 {
	bounds := img.Bounds()
	const step = 4
	var n, sum, sumSquares float64
	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			r, g, b, _ := img.At(x, y).RGBA()
			// Rec. 601 luma over 0..1; RGBA() returns 16-bit values.
			luma := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 65535
			n++
			sum += luma
			sumSquares += luma * luma
		}
	}
	if n == 0 {
		return 0
	}
	variance := sumSquares/n - (sum/n)*(sum/n)
	if variance <= 0 {
		return 0
	}
	return math.Sqrt(variance)
}
