package qemu

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStdDev_BlankAndPainted(t *testing.T) {
	// A screen that never painted is uniform. This is the whole basis of the
	// blank verdict, so both ends of it are pinned here.
	black := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			black.SetRGBA(x, y, color.RGBA{A: 255})
		}
	}
	if got := stdDev(black); got != 0 {
		t.Errorf("an all-black frame has deviation %v, want 0", got)
	}

	grey := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			grey.SetRGBA(x, y, color.RGBA{R: 90, G: 90, B: 90, A: 255})
		}
	}
	if got := stdDev(grey); got != 0 {
		t.Errorf("a uniform grey frame has deviation %v, want 0 — flat is flat, whatever the colour", got)
	}

	// Half black, half white: as painted as a frame gets.
	painted := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			shade := uint8(0)
			if x > 31 {
				shade = 255
			}
			painted.SetRGBA(x, y, color.RGBA{R: shade, G: shade, B: shade, A: 255})
		}
	}
	if got := stdDev(painted); got <= BlankStdDev {
		t.Errorf("a painted frame has deviation %v, which reads as blank", got)
	}
}

func TestFrameBlank(t *testing.T) {
	if !(Frame{StdDev: 0}).Blank() {
		t.Error("a deviation of zero is blank")
	}
	if !(Frame{StdDev: BlankStdDev}).Blank() {
		t.Error("the threshold itself counts as blank")
	}
	if (Frame{StdDev: 0.3}).Blank() {
		t.Error("a painted frame is not blank")
	}
}

func TestCapture(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	vmDir := filepath.Join(VMHome(), "capturevm")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeQMPServer(t, filepath.Join(vmDir, "qmp.sock"))

	// A directory that does not exist yet: the frame recorder writes into
	// artifacts/frames, which nothing else creates.
	out := filepath.Join(tmp, "frames", "f000000.png")
	frame, err := Capture("capturevm", out)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if frame.Path != out {
		t.Errorf("path = %q", frame.Path)
	}
	// The fake server's screendump is one flat pixel.
	if frame.Width != 1 || frame.Height != 1 {
		t.Errorf("size = %dx%d, want 1x1", frame.Width, frame.Height)
	}
	if !frame.Blank() {
		t.Errorf("a single flat pixel should read as blank, deviation %v", frame.StdDev)
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		t.Errorf("expected a PNG at %s: %v", out, err)
	}
}

func TestCapture_NoSocket(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := Capture("never-started", "")
	if err == nil || !strings.Contains(err.Error(), "QMP socket") {
		t.Errorf("expected a missing-socket error, got %v", err)
	}
}

func TestQcodesFor(t *testing.T) {
	for _, tc := range []struct {
		in   rune
		want []string
	}{
		{in: 'a', want: []string{"a"}},
		{in: 'Z', want: []string{"shift", "z"}},
		{in: '7', want: []string{"7"}},
		{in: ' ', want: []string{"spc"}},
		{in: '\n', want: []string{"ret"}},
		{in: '-', want: []string{"minus"}},
		{in: '_', want: []string{"shift", "minus"}},
		{in: '!', want: []string{"shift", "1"}},
	} {
		got, ok := qcodesFor(tc.in)
		if !ok {
			t.Errorf("%q has no mapping", tc.in)
			continue
		}
		if strings.Join(got, "+") != strings.Join(tc.want, "+") {
			t.Errorf("%q maps to %v, want %v", tc.in, got, tc.want)
		}
	}
	// A character with no US-keyboard mapping must be refused, not dropped: a
	// passphrase quietly missing a character fails like the guest's fault.
	if _, ok := qcodesFor('é'); ok {
		t.Error("é should have no mapping")
	}
}

func TestSendKeys_RefusesWhatItCannotType(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := SendKeys("gate", "påsswørd")
	if err == nil || !strings.Contains(err.Error(), "cannot type") {
		t.Errorf("expected a refusal naming the character, got %v", err)
	}
}

func TestSendKey_NoKeys(t *testing.T) {
	if err := SendKey("gate"); err == nil {
		t.Error("sending no keys is a programming error")
	}
}
