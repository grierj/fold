package images

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"sync"
	"testing"
	"time"

	"fold/internal/images/imagetest"
)

func fillSlots(t *testing.T) []func() {
	t.Helper()
	var releases []func()
	t.Cleanup(func() {
		for _, r := range releases {
			r()
		}
	})
	for i := 0; i < MaxConcurrentProcessing; i++ {
		release, err := Admit()
		if err != nil {
			t.Fatalf("slot %d: %v", i, err)
		}
		releases = append(releases, release)
	}
	return releases
}

func TestAdmitRefusesAtOnceWhenFull(t *testing.T) {
	releases := fillSlots(t)
	if _, err := Admit(); !errors.Is(err, ErrBusy) {
		t.Fatalf("full: %v, want ErrBusy", err)
	}
	releases[0]()
	release, err := Admit()
	if err != nil {
		t.Fatalf("after a release: %v", err)
	}
	release()
}

// Releasing twice frees one slot, not two, so a deferred release after an
// explicit one can never let more than the limit run at once.
func TestAdmitReleaseIsOnce(t *testing.T) {
	releases := fillSlots(t)
	releases[0]()
	releases[0]()
	first, err := Admit()
	if err != nil {
		t.Fatalf("one slot was freed: %v", err)
	}
	defer first()
	if _, err := Admit(); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second release freed another holder's slot: %v", err)
	}
}

// memoryPeaks samples the Go heap while a measurement runs: the bytes held by
// heap objects (live ones and dead ones the collector has not yet freed) and
// the memory the Go runtime has mapped in all.
type memoryPeaks struct {
	stop       chan struct{}
	done       chan struct{}
	objects    uint64
	mapped     uint64
	baseObject uint64
}

func startMemoryPeaks() *memoryPeaks {
	p := &memoryPeaks{stop: make(chan struct{}), done: make(chan struct{})}
	samples := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/total:bytes"}}
	metrics.Read(samples)
	p.baseObject = samples[0].Value.Uint64()
	go func() {
		defer close(p.done)
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			metrics.Read(samples)
			p.objects, p.mapped = max(p.objects, samples[0].Value.Uint64()), max(p.mapped, samples[1].Value.Uint64())
			select {
			case <-p.stop:
				return
			case <-tick.C:
			}
		}
	}()
	return p
}

func (p *memoryPeaks) finish() (objects, mapped uint64) {
	close(p.stop)
	<-p.done
	return p.objects - min(p.objects, p.baseObject), p.mapped
}

// measureEstimate is the estimate Normalize applies to data: what the decode,
// the turn and the encode allocate, and the encoded buffers.
func measureEstimate(t *testing.T, mime string, data []byte) int64 {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	switch mime {
	case "image/jpeg":
		frame, ok := scanJPEGFrame(data)
		if !ok {
			t.Fatal("no JPEG frame")
		}
		return jpegWorkingBytes(frame, cfg, data, jpegOrientation(data)) + bufferBytes(len(data))
	case "image/png":
		decode, encode := pngBytes(data, cfg)
		return decode + encode + bufferBytes(len(data))
	}
	frames, pixels, _ := scanGIF(data)
	return gifBytes(frames, pixels) + bufferBytes(len(data))
}

// processFile copies src as ReadUpload does and normalizes the copy.
func processFile(src, mime string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp("", "upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := io.Copy(tmp, in); err != nil {
		return err
	}
	_, _, err = Normalize(tmp, mime)
	return err
}

// TestMeasureNormalize is the opt-in measurement of the work behind the
// processing limit (FOLD_UPLOAD_MEASURE=1, docs/agents/testing.md): for each
// worst-case picture, in a process of its own, waves of MaxConcurrentProcessing
// Normalize calls run at the same time while more attempts are refused, then
// every slot must be free again. It reports the estimate, peak heap objects,
// the Go runtime's mapped memory, the process high-water mark (VmHWM) over its
// baseline, and cumulative allocation per call, which are four different
// numbers. It fails when a case is not near the budget (so it is not a worst
// case), when a call allocates more than its estimate, or when the process
// peak passes ProcessAllowanceBytes. HTTP multipart ingress is not in it
// (TestMeasureUploadRequests).
func TestMeasureNormalize(t *testing.T) {
	c, ok := imagetest.MeasureCase(t, "TestMeasureNormalize")
	if !ok {
		return
	}
	data := c.Build(t)
	if len(data) > MaxUploadBytes {
		t.Fatalf("%s is %d bytes, over the upload limit", c.Name, len(data))
	}
	estimate := measureEstimate(t, c.Mime, data)
	src := filepath.Join(t.TempDir(), "picture")
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	inputBytes := len(data)
	data = nil
	t.Setenv("TMPDIR", t.TempDir()) // so a leaked temp file is seen

	const waves, extraAttempts = 4, 8
	if got := float64(estimate) / float64(maxWorkingBytes); got < imagetest.NearBudget || got > 1 {
		t.Fatalf("%s: the estimate is %.0f%% of the budget, want %.0f%% to 100%%: resize the case", c.Name, got*100, imagetest.NearBudget*100)
	}
	runtime.GC()
	debug.FreeOSMemory()
	resetOK := imagetest.ResetPeak()
	baseHWM := imagetest.ProcessKB("VmHWM")
	if !resetOK || baseHWM < 0 {
		t.Fatal("the process's peak resident memory cannot be reset or read here (Linux 4.0 or later with /proc): run the measurement there")
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	peaks := startMemoryPeaks()
	refused := 0
	for w := 0; w < waves; w++ {
		var wg sync.WaitGroup
		ready, start := make(chan struct{}, MaxConcurrentProcessing), make(chan struct{})
		errs := make(chan error, MaxConcurrentProcessing)
		for i := 0; i < MaxConcurrentProcessing; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				release, err := Admit()
				ready <- struct{}{}
				if err != nil {
					errs <- err
					return
				}
				defer release()
				<-start
				errs <- processFile(src, c.Mime)
			}()
		}
		for i := 0; i < MaxConcurrentProcessing; i++ {
			<-ready
		}
		// Every slot is held by a call that has not begun: more are refused at once.
		for i := 0; i < extraAttempts; i++ {
			release, err := Admit()
			switch {
			case errors.Is(err, ErrBusy):
				refused++
			case err == nil:
				release()
				t.Fatal("admitted past the limit")
			default:
				t.Fatal(err)
			}
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
		}
	}
	objects, mapped := peaks.finish()
	runtime.ReadMemStats(&after)
	// Every slot must be free again.
	for i := 0; i < MaxConcurrentProcessing; i++ {
		release, err := Admit()
		if err != nil {
			t.Fatalf("slot %d did not recover: %v", i+1, err)
		}
		defer release()
	}
	left, _ := filepath.Glob(filepath.Join(os.TempDir(), "upload-*"))
	peakKB := imagetest.ProcessKB("VmHWM") - baseHWM
	allocPerCall := int64(after.TotalAlloc-before.TotalAlloc) / (waves * MaxConcurrentProcessing)
	report, _ := json.Marshal(map[string]any{
		"case":                      c.Name,
		"inputBytes":                inputBytes,
		"estimateBytesPerCall":      estimate,
		"budgetBytesPerCall":        int64(maxWorkingBytes),
		"slots":                     MaxConcurrentProcessing,
		"waves":                     waves,
		"refusedWhileFull":          refused,
		"peakHeapObjectsBytes":      objects,
		"peakGoMappedBytes":         mapped,
		"vmHWMOverBaselineKB":       peakKB,
		"allowanceKB":               int64(ProcessAllowanceBytes) / 1024,
		"vmHWMResetWorked":          resetOK,
		"totalAllocBytesPerCall":    allocPerCall,
		"tempFilesLeft":             len(left),
		"goVersion":                 runtime.Version(),
		"estimateTimesSlotsInBytes": estimate * MaxConcurrentProcessing,
	})
	t.Logf("MEASURE %s", report)
	if len(left) != 0 {
		t.Errorf("%d temp files left", len(left))
	}
	if allocPerCall > estimate {
		t.Errorf("%s: a call allocated %d bytes, more than its estimate of %d", c.Name, allocPerCall, estimate)
	}
	if peakKB > int64(ProcessAllowanceBytes)/1024 {
		t.Errorf("%s: the process peak grew %d kB, over the %d kB allowance", c.Name, peakKB, int64(ProcessAllowanceBytes)/1024)
	}
}
