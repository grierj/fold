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
// one for each way the estimate adds up: plain and interlaced PNG, PNGs of 16
// bits a channel (which are written back a pixel at a time) and one whose
// transparency widens it, baseline JPEG that needs turning, progressive JPEG in
// each color layout, one whose Adobe segment comes after every scan, and a long
// GIF. Each is sized so its estimate is within a few percent of the budget
// (NearBudget); the measurement refuses one that has drifted below it.
func WorstCases() []Case {
	progressive := func(w, h int, ids, hv []byte, adobe int) func(*testing.T) []byte {
		return func(*testing.T) []byte { return ProgressiveJPEG(w, h, ids, hv, adobe) }
	}
	return []Case{
		{"png-rgba-3700", "image/png", func(t *testing.T) []byte {
			img := image.NewNRGBA(image.Rect(0, 0, 3700, 3700))
			for y := 0; y < 3700; y++ {
				for x := 0; x < 3700; x++ {
					img.SetNRGBA(x, y, color.NRGBA{R: uint8(x % 251), G: uint8(y % 251), B: 80, A: 255})
				}
			}
			var b bytes.Buffer
			if err := png.Encode(&b, img); err != nil {
				t.Fatal(err)
			}
			return b.Bytes()
		}},
		{"png-adam7-rgb-2600", "image/png", func(t *testing.T) []byte { return Adam7RGBPNG(t, 2600, 2600) }},
		{"png-gray16-3700", "image/png", func(t *testing.T) []byte { return Gray16PNG(t, 3700, 3700) }},
		{"png-gray16-trns-1800", "image/png", func(t *testing.T) []byte { return GrayTransparentPNG(t, 1800, 1800) }},
		{"png-nrgba64-1800", "image/png", func(t *testing.T) []byte { return SixteenBitPNG(t, 1800, 1800, false) }},
		{"jpeg-baseline-420-turned-3600x2700", "image/jpeg", func(t *testing.T) []byte {
			img := image.NewRGBA(image.Rect(0, 0, 3600, 2700))
			for y := 0; y < 2700; y++ {
				for x := 0; x < 3600; x++ {
					img.SetRGBA(x, y, color.RGBA{R: uint8(x >> 4), G: uint8(y >> 4), B: uint8((x + y) >> 5), A: 255})
				}
			}
			var b bytes.Buffer
			if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 75}); err != nil {
				t.Fatal(err)
			}
			return WithEXIF(b.Bytes(), ExifBlock(6))
		}},
		{"jpeg-progressive-420-3000x2400", "image/jpeg", progressive(3000, 2400, []byte{1, 2, 3}, []byte{0x22, 0x11, 0x11}, -1)},
		{"jpeg-progressive-444-1900", "image/jpeg", progressive(1900, 1900, []byte{1, 2, 3}, []byte{0x11, 0x11, 0x11}, -1)},
		{"jpeg-progressive-rgb-1500", "image/jpeg", progressive(1500, 1500, []byte("RGB"), []byte{0x11, 0x11, 0x11}, -1)},
		{"jpeg-progressive-444-late-adobe-1500", "image/jpeg", func(*testing.T) []byte {
			return WithLateAdobe(ProgressiveJPEG(1500, 1500, []byte{1, 2, 3}, []byte{0x11, 0x11, 0x11}, -1), 0)
		}},
		{"jpeg-progressive-rgb-ids-early-jfif-late-app0-1500", "image/jpeg", func(*testing.T) []byte {
			return WithLateAPP0(WithEarlyJFIF(ProgressiveJPEG(1500, 1500, []byte("RGB"), []byte{0x11, 0x11, 0x11}, -1)))
		}},
		{"jpeg-progressive-cmyk-1400", "image/jpeg", progressive(1400, 1400, []byte{1, 2, 3, 4}, []byte{0x11, 0x11, 0x11, 0x11}, 0)},
		{"gif-26-frames-1000", "image/gif", func(t *testing.T) []byte {
			g := &gif.GIF{}
			for i := 0; i < 26; i++ {
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

// NearBudget is the share of the budget a worst case's estimate must reach for
// the measurement to count it as one.
const NearBudget = 0.8

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
