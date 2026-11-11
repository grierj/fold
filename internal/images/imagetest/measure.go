package imagetest

import (
	"bytes"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// The opt-in upload memory measurements (docs/agents/testing.md) run each
// worst-case picture in a process of its own, so a process's high-water mark
// (VmHWM) belongs to one picture.
const (
	// MeasureEnv set to 1 turns the measurements on; they are skipped without it.
	MeasureEnv = "FOLD_UPLOAD_MEASURE"
	// CaseEnv names the one picture a measurement child runs.
	CaseEnv = "FOLD_UPLOAD_MEASURE_CASE"
)

// Case is a picture near the most the upload path accepts for its format: its
// estimated working memory is within a few percent of images' budget. Build
// makes it from scratch (some take seconds).
type Case struct {
	Name  string
	Mime  string
	Build func(t *testing.T) []byte
}

// WorstCases are the accepted pictures that need the most memory to process,
// one for each way the estimate adds up: plain and interlaced PNG, a PNG whose
// transparency widens it, baseline JPEG that needs turning, progressive JPEG in
// each color layout, and a long GIF.
func WorstCases() []Case {
	progressive := func(w, h int, ids, hv []byte, adobe int) func(*testing.T) []byte {
		return func(*testing.T) []byte { return ProgressiveJPEG(w, h, ids, hv, adobe) }
	}
	return []Case{
		{"png-rgba-25mp", "image/png", func(t *testing.T) []byte {
			img := image.NewNRGBA(image.Rect(0, 0, 5000, 5000))
			for y := 0; y < 5000; y++ {
				for x := 0; x < 5000; x++ {
					img.SetNRGBA(x, y, color.NRGBA{R: uint8(x % 251), G: uint8(y % 251), B: 80, A: 255})
				}
			}
			var b bytes.Buffer
			if err := png.Encode(&b, img); err != nil {
				t.Fatal(err)
			}
			return b.Bytes()
		}},
		{"png-adam7-rgb-3900", "image/png", func(t *testing.T) []byte { return Adam7RGBPNG(t, 3900, 3900) }},
		{"png-gray16-trns-3900", "image/png", func(t *testing.T) []byte { return GrayTransparentPNG(t, 3900, 3900) }},
		{"jpeg-baseline-420-12mp-turned", "image/jpeg", func(t *testing.T) []byte {
			img := image.NewRGBA(image.Rect(0, 0, 4000, 3000))
			for y := 0; y < 3000; y++ {
				for x := 0; x < 4000; x++ {
					img.SetRGBA(x, y, color.RGBA{R: uint8(x >> 4), G: uint8(y >> 4), B: uint8((x + y) >> 5), A: 255})
				}
			}
			var b bytes.Buffer
			if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 75}); err != nil {
				t.Fatal(err)
			}
			return WithEXIF(b.Bytes(), ExifBlock(6))
		}},
		{"jpeg-progressive-420-12mp", "image/jpeg", progressive(4000, 3000, []byte{1, 2, 3}, []byte{0x22, 0x11, 0x11}, -1)},
		{"jpeg-progressive-444-2800", "image/jpeg", progressive(2800, 2800, []byte{1, 2, 3}, []byte{0x11, 0x11, 0x11}, -1)},
		{"jpeg-progressive-rgb-2500", "image/jpeg", progressive(2500, 2500, []byte("RGB"), []byte{0x11, 0x11, 0x11}, -1)},
		{"jpeg-progressive-cmyk-2200", "image/jpeg", progressive(2200, 2200, []byte{1, 2, 3, 4}, []byte{0x11, 0x11, 0x11, 0x11}, 0)},
		{"gif-60-frames-1000", "image/gif", func(t *testing.T) []byte {
			g := &gif.GIF{}
			for i := 0; i < 60; i++ {
				g.Image = append(g.Image, image.NewPaletted(image.Rect(0, 0, 1000, 1000), palette.Plan9))
				g.Delay = append(g.Delay, 5)
			}
			var b bytes.Buffer
			if err := gif.EncodeAll(&b, g); err != nil {
				t.Fatal(err)
			}
			return b.Bytes()
		}},
	}
}

// MeasureCase is how a measurement test starts. Without MeasureEnv it skips.
// Without CaseEnv it runs testName again once for every worst case, each in a
// fresh process, and returns false (the children did the work). With CaseEnv it
// returns that case and true.
func MeasureCase(t *testing.T, testName string) (Case, bool) {
	t.Helper()
	if os.Getenv(MeasureEnv) != "1" {
		t.Skipf("set %s=1 to run the upload memory measurement (opt-in, not run in CI)", MeasureEnv)
	}
	cases := WorstCases()
	if name := os.Getenv(CaseEnv); name != "" {
		for _, c := range cases {
			if c.Name == name {
				return c, true
			}
		}
		t.Fatalf("no worst case named %q", name)
	}
	for _, c := range cases {
		cmd := exec.Command(os.Args[0], "-test.run=^"+testName+"$", "-test.count=1", "-test.v", "-test.timeout=30m")
		cmd.Env = append(os.Environ(), CaseEnv+"="+c.Name)
		out, err := cmd.CombinedOutput()
		t.Logf("%s:\n%s", c.Name, out)
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
		}
	}
	return Case{}, false
}

// ProcessKB reads a field of /proc/self/status in kB (VmHWM, VmRSS), or -1.
func ProcessKB(field string) int64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, field+":"); ok {
			n, err := strconv.ParseInt(strings.Fields(rest)[0], 10, 64)
			if err != nil {
				return -1
			}
			return n
		}
	}
	return -1
}

// ResetPeak sets the process's VmHWM back to its current resident size (Linux
// 4.0 and later); it reports whether that worked. Without it VmHWM keeps the
// peak of everything the process did before, fixtures included.
func ResetPeak() bool {
	return os.WriteFile("/proc/self/clear_refs", []byte("5"), 0o200) == nil
}
