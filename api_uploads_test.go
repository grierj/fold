package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fold/internal/content"
	"fold/storage"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"sync"
	"testing"
	"time"

	"fold/internal/images"
	"fold/internal/images/imagetest"
	"fold/models"
)

// occupyAllSlots takes every processing slot through the real limiter and
// returns a function that frees one.
func occupyAllSlots(t *testing.T) (freeOne func()) {
	t.Helper()
	var releases []func()
	for i := 0; i < images.MaxConcurrentProcessing; i++ {
		release, err := images.Admit()
		if err != nil {
			t.Fatalf("slot %d was already taken: %v", i, err)
		}
		releases = append(releases, release)
	}
	t.Cleanup(func() {
		for _, r := range releases {
			r()
		}
	})
	return func() {
		releases[len(releases)-1]()
		releases = releases[:len(releases)-1]
	}
}

func requireFreeSlots(t *testing.T) {
	t.Helper()
	var releases []func()
	defer func() {
		for _, r := range releases {
			r()
		}
	}()
	for i := 0; i < images.MaxConcurrentProcessing; i++ {
		release, err := images.Admit()
		if err != nil {
			t.Fatalf("slot %d of %d is still held: an upload leaked it", i+1, images.MaxConcurrentProcessing)
		}
		releases = append(releases, release)
	}
}

func uploadTempFiles(t *testing.T) int {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(os.TempDir(), "upload-*"))
	if err != nil {
		t.Fatal(err)
	}
	return len(m)
}

// uploadOptions shapes a raw upload request: the CSRF token goes in the header
// (the default), in the csrf_token form field, or is replaced.
type uploadOptions struct {
	formToken string // sent as the csrf_token field instead of the header
	badToken  bool
	ctx       context.Context
}

func postUpload(t *testing.T, c *apiClient, path, field string, data []byte, o uploadOptions) *httptest.ResponseRecorder {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	if o.formToken != "" {
		if err := mw.WriteField("csrf_token", o.formToken); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := mw.CreateFormFile(field, "picture.img")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", path, &b)
	if o.ctx != nil {
		req = req.WithContext(o.ctx)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for _, ck := range c.jar {
		req.AddCookie(ck)
	}
	switch {
	case o.badToken:
		req.Header.Set("X-CSRF-Token", "not-the-token")
	case o.formToken == "":
		req.Header.Set("X-CSRF-Token", c.token)
	}
	rec := httptest.NewRecorder()
	c.router.ServeHTTP(rec, req)
	return rec
}

func wantCode(t *testing.T, label string, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status || (code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`)) {
		t.Errorf("%s: got %d %s, want %d %s", label, w.Code, w.Body.String(), status, code)
	}
}

type uploadFixture struct {
	app        *App
	member     *models.User
	leader     *models.User
	group      *models.Group
	memberC    *apiClient
	leaderC    *apiClient
	avatarPath string
	coverPath  string
}

func newUploadFixture(t *testing.T) *uploadFixture {
	t.Helper()
	app := testApp(t)
	f := &uploadFixture{app: app}
	f.member = newTestUser(t, app, "member", "password1234", models.RoleMember)
	f.leader = newTestUser(t, app, "leader", "password1234", models.RoleMember)
	f.group = newTestGroup(t, app, f.leader, "Band", models.GroupActive)
	f.memberC = loginAPI(t, app, "member", "password1234")
	f.leaderC = loginAPI(t, app, "leader", "password1234")
	f.avatarPath = "/api/v1/me/avatar"
	f.coverPath = "/api/v1/groups/" + itoa(f.group.ID) + "/cover"
	return f
}

func (f *uploadFixture) nothingStored(t *testing.T) {
	t.Helper()
	var u models.User
	if err := f.app.DB.First(&u, f.member.ID).Error; err != nil {
		t.Fatal(err)
	}
	var g models.Group
	if err := f.app.DB.First(&g, f.group.ID).Error; err != nil {
		t.Fatal(err)
	}
	if u.AvatarKey != "" || g.ImageURL != "" {
		t.Errorf("a refused upload stored something: avatar %q, cover %q", u.AvatarKey, g.ImageURL)
	}
}

// Both routes draw on one set of slots, a full set answers at once with the
// declared 503 and saves nothing, and a freed slot is usable again.
func TestUploadRoutesShareOneProcessingLimit(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	f := newUploadFixture(t)
	tmp := uploadTempFiles(t)
	freeOne := occupyAllSlots(t)

	// A corrupt file would answer 400 file_unreadable if it were read, so the
	// 503 shows the upload was refused before it was.
	wantCode(t, "avatar, slots full", postUpload(t, f.memberC, f.avatarPath, "avatar", []byte("not an image"), uploadOptions{}), http.StatusServiceUnavailable, "upload_busy")
	wantCode(t, "cover, slots full", postUpload(t, f.leaderC, f.coverPath, "cover", []byte("not an image"), uploadOptions{}), http.StatusServiceUnavailable, "upload_busy")
	wantCode(t, "valid avatar, slots full", postUpload(t, f.memberC, f.avatarPath, "avatar", tinyPNG(t), uploadOptions{}), http.StatusServiceUnavailable, "upload_busy")
	f.nothingStored(t)
	if got := uploadTempFiles(t); got != tmp {
		t.Errorf("a refused upload left temp files: %d, was %d", got, tmp)
	}

	freeOne()
	wantCode(t, "avatar, one slot free", postUpload(t, f.memberC, f.avatarPath, "avatar", tinyPNG(t), uploadOptions{}), http.StatusOK, "")
	// The avatar's slot came back, so the other route can have it.
	wantCode(t, "cover, after the avatar finished", postUpload(t, f.leaderC, f.coverPath, "cover", tinyPNG(t), uploadOptions{}), http.StatusOK, "")
}

// A request that may not upload is refused by auth, CSRF or its own policy
// check without reaching admission: with every slot taken it still gets its
// own answer, not 503. (csrfMiddleware has already parsed the body by then; the
// slot bounds the work after it, not that parse.)
func TestUploadRefusalsComeBeforeAdmission(t *testing.T) {
	f := newUploadFixture(t)
	teen := newTestUser(t, f.app, "teen", "password1234", models.RoleMember)
	f.app.DB.Model(teen).Update("account_type", models.AccountTeen)
	teenC := loginAPI(t, f.app, "teen", "password1234")
	other := newTestUser(t, f.app, "other", "password1234", models.RoleMember)
	_ = other
	otherC := loginAPI(t, f.app, "other", "password1234")
	anon := newAPIClient(t, f.app)
	anon.fetchCSRF()
	occupyAllSlots(t)
	png := tinyPNG(t)

	wantCode(t, "signed out avatar", postUpload(t, anon, f.avatarPath, "avatar", png, uploadOptions{}), http.StatusUnauthorized, "")
	wantCode(t, "signed out cover", postUpload(t, anon, f.coverPath, "cover", png, uploadOptions{}), http.StatusUnauthorized, "")
	wantCode(t, "bad CSRF header", postUpload(t, f.memberC, f.avatarPath, "avatar", png, uploadOptions{badToken: true}), http.StatusForbidden, "")
	wantCode(t, "bad CSRF form field", postUpload(t, f.leaderC, f.coverPath, "cover", png, uploadOptions{formToken: "not-the-token"}), http.StatusForbidden, "")
	wantCode(t, "teen avatar", postUpload(t, teenC, f.avatarPath, "avatar", png, uploadOptions{}), http.StatusForbidden, "teen_avatar")
	wantCode(t, "non-leader cover", postUpload(t, otherC, f.coverPath, "cover", png, uploadOptions{}), http.StatusForbidden, "")
	f.nothingStored(t)
}

// Both CSRF token forms still reach the upload, and a declared length over the
// body cap keeps its 413.
func TestUploadAcceptsEitherCSRFFormAndKeepsBodyCap(t *testing.T) {
	f := newUploadFixture(t)
	wantCode(t, "header token", postUpload(t, f.memberC, f.avatarPath, "avatar", tinyPNG(t), uploadOptions{}), http.StatusOK, "")
	wantCode(t, "form token", postUpload(t, f.leaderC, f.coverPath, "cover", tinyPNG(t), uploadOptions{formToken: f.leaderC.token}), http.StatusOK, "")

	big := bytes.Repeat([]byte("x"), maxRequestBytes+1)
	req := httptest.NewRequest("POST", f.avatarPath, bytes.NewReader(big))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	req.ContentLength = int64(len(big))
	for _, ck := range f.memberC.jar {
		req.AddCookie(ck)
	}
	req.Header.Set("X-CSRF-Token", f.memberC.token)
	rec := httptest.NewRecorder()
	f.memberC.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("declared length over the cap: got %d, want 413", rec.Code)
	}
	requireFreeSlots(t)
}

// Heavy inputs are refused from their header, and every way an upload can fail
// gives its slot and temp file back, so more failures than slots leave the
// next valid upload working.
func TestFailedUploadsReleaseTheirSlots(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	f := newUploadFixture(t)
	tmp := uploadTempFiles(t)
	var heavyGIF []byte
	heavyGIF = append(heavyGIF, "GIF89a"...)
	heavyGIF = binary.LittleEndian.AppendUint16(heavyGIF, 5000)
	heavyGIF = binary.LittleEndian.AppendUint16(heavyGIF, 5000)
	heavyGIF = append(heavyGIF, 0, 0, 0)
	for i := 0; i < 4; i++ { // 100 million pixels: inside the pixel cap, over the memory estimate
		heavyGIF = append(heavyGIF, 0x2C, 0, 0, 0, 0)
		heavyGIF = binary.LittleEndian.AppendUint16(heavyGIF, 5000)
		heavyGIF = binary.LittleEndian.AppendUint16(heavyGIF, 5000)
		heavyGIF = append(heavyGIF, 0, 8, 2, 0x4C, 0x01, 0)
	}
	heavyGIF = append(heavyGIF, 0x3B)

	cases := map[string]struct {
		data []byte
		code string
	}{
		// The first four are small files: none would be refused by its pixel
		// count or byte size, only by what the decoder would allocate for it.
		"16-bit 5000x5000 PNG":        {imagetest.PNGHeader(5000, 5000, 16, 6, 0), "image_too_large"},
		"16-bit gray PNG with tRNS":   {append(append(imagetest.PNGHeader(5000, 5000, 16, 0, 0), imagetest.PNGChunk("tRNS", []byte{0, 1})...), imagetest.PNGChunk("IDAT", []byte("x"))...), "image_too_large"},
		"Adam7 PNG, 4700x4700":        {append(imagetest.PNGHeader(4700, 4700, 8, 2, 1), imagetest.PNGChunk("IDAT", []byte("x"))...), "image_too_large"},
		"progressive JPEG, 5000x5000": {imagetest.ProgressiveJPEG(5000, 5000, []byte{1, 2, 3}, []byte{0x11, 0x11, 0x11}, -1), "image_too_large"},
		"progressive CMYK JPEG":       {imagetest.ProgressiveJPEG(5000, 5000, []byte{1, 2, 3, 4}, []byte{0x11, 0x11, 0x11, 0x11}, 0), "image_too_large"},
		"aggregate-frame GIF":         {heavyGIF, "image_too_large"},
		"corrupt JPEG":                {[]byte("\xFF\xD8\xFF\xE0 not really a jpeg at all"), "file_unreadable"},
		"not an image":                {[]byte("<html>"), "file_type"},
	}
	for round := 0; round <= images.MaxConcurrentProcessing; round++ {
		for name, tc := range cases {
			wantCode(t, name+" avatar", postUpload(t, f.memberC, f.avatarPath, "avatar", tc.data, uploadOptions{}), http.StatusBadRequest, tc.code)
			wantCode(t, name+" cover", postUpload(t, f.leaderC, f.coverPath, "cover", tc.data, uploadOptions{}), http.StatusBadRequest, tc.code)
		}
	}
	// A request whose client had gone before reading began.
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	wantCode(t, "cancelled", postUpload(t, f.memberC, f.avatarPath, "avatar", tinyPNG(t), uploadOptions{ctx: gone}), http.StatusRequestTimeout, "upload_canceled")

	requireFreeSlots(t)
	if got := uploadTempFiles(t); got != tmp {
		t.Errorf("failed uploads left temp files: %d, was %d", got, tmp)
	}
	f.nothingStored(t)
	wantCode(t, "valid avatar after failures", postUpload(t, f.memberC, f.avatarPath, "avatar", tinyPNG(t), uploadOptions{}), http.StatusOK, "")
	wantCode(t, "valid cover after failures", postUpload(t, f.leaderC, f.coverPath, "cover", tinyPNG(t), uploadOptions{}), http.StatusOK, "")
}

// Concurrent requests never hold more slots than the limit, and once they have
// all answered every slot is free. Each one either finishes or is told to try
// again; none waits.
func TestConcurrentUploadsStayWithinTheLimit(t *testing.T) {
	f := newUploadFixture(t)
	png := imagetest.PNGWithText(t, imagetest.BlockImage(2, 3, imagetest.GridColors))
	const n = 12
	codes := make(chan int, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			<-start
			codes <- postUpload(t, f.memberC, f.avatarPath, "avatar", png, uploadOptions{}).Code
		}()
	}
	close(start)
	ok, busy := 0, 0
	for i := 0; i < n; i++ {
		switch code := <-codes; code {
		case http.StatusOK:
			ok++
		case http.StatusServiceUnavailable:
			busy++
		default:
			t.Errorf("concurrent upload answered %d, want 200 or 503", code)
		}
	}
	if ok == 0 {
		t.Error("no concurrent upload succeeded")
	}
	t.Logf("%d finished, %d were told to try again", ok, busy)
	requireFreeSlots(t)
}

// A multipart body of no declared length is bounded by the body cap while it is
// parsed (the CSRF check does that first). Once the parse has failed on the cap
// the handler finds no file, so nothing is read from it or normalized: the
// picture inside is valid, and the file is over the size limit besides, so
// reaching ReadUpload would have answered file_too_large instead.
func TestUploadWithoutLengthIsBoundedByTheBodyCap(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	f := newUploadFixture(t)
	tmp := uploadTempFiles(t)
	for _, tc := range []struct {
		name, path, field string
		c                 *apiClient
	}{
		{"avatar", f.avatarPath, "avatar", f.memberC},
		{"cover", f.coverPath, "cover", f.leaderC},
	} {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		fw, err := mw.CreateFormFile(tc.field, "picture.png")
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range [][]byte{tinyPNG(t), make([]byte, maxRequestBytes+1<<20)} { // padding after IEND, past the cap
			if _, err := fw.Write(part); err != nil {
				t.Fatal(err)
			}
		}
		if err := mw.Close(); err != nil {
			t.Fatal(err)
		}
		// A body that is not a bytes.Buffer, Reader or strings.Reader has no length.
		req := httptest.NewRequest("POST", tc.path, io.NopCloser(&b))
		if req.ContentLength != -1 {
			t.Fatalf("test body has a declared length: %d", req.ContentLength)
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
		for _, ck := range tc.c.jar {
			req.AddCookie(ck)
		}
		req.Header.Set("X-CSRF-Token", tc.c.token)
		rec := httptest.NewRecorder()
		tc.c.router.ServeHTTP(rec, req)
		wantCode(t, tc.name+", no declared length, over the cap", rec, http.StatusBadRequest, "file_unreadable")
	}
	requireFreeSlots(t)
	if got := uploadTempFiles(t); got != tmp {
		t.Errorf("an oversized body left temp files: %d, was %d", got, tmp)
	}
	f.nothingStored(t)
}

// waitFor polls until cond holds, or fails the test.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Requests cancelled while their pictures are being processed keep their slots
// until the decoders return (they cannot be stopped), so a third request is
// still told to wait, and when the work is over every slot is free, no temp
// file is left, nothing is stored and the next upload works. The pictures are
// real, large enough to take a good while to decode and encode again.
func TestCancelledDuringProcessingKeepsTheSlotsUntilItIsDone(t *testing.T) {
	f := newUploadFixture(t)
	var heavy bytes.Buffer
	if err := png.Encode(&heavy, image.NewGray(image.Rect(0, 0, 4000, 4000))); err != nil {
		t.Fatal(err)
	}
	// Only the pictures' own temp files are in this directory, so seeing one is
	// seeing a request inside ReadUpload, which is where its slot is held.
	t.Setenv("TMPDIR", t.TempDir())
	if uploadTempFiles(t) != 0 {
		t.Fatal("the temp directory is not empty")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	avatar, cover := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go func() {
		avatar <- postUpload(t, f.memberC, f.avatarPath, "avatar", heavy.Bytes(), uploadOptions{ctx: ctx})
	}()
	go func() {
		cover <- postUpload(t, f.leaderC, f.coverPath, "cover", heavy.Bytes(), uploadOptions{ctx: ctx})
	}()
	// Each request copies its upload into an input file (upload-<digits>) before
	// decoding it and keeps it until it is done; the output file comes later.
	waitFor(t, "both pictures to be in processing", func() bool {
		inputs, err := filepath.Glob(filepath.Join(os.TempDir(), "upload-[0-9]*"))
		if err != nil {
			t.Fatal(err)
		}
		return len(inputs) >= images.MaxConcurrentProcessing
	})

	cancel()
	// Both slots are still taken by work that cannot be stopped.
	wantCode(t, "a third upload while the cancelled ones run", postUpload(t, f.memberC, f.avatarPath, "avatar", tinyPNG(t), uploadOptions{}), http.StatusServiceUnavailable, "upload_busy")

	wantCode(t, "cancelled avatar", <-avatar, http.StatusRequestTimeout, "upload_canceled")
	wantCode(t, "cancelled cover", <-cover, http.StatusRequestTimeout, "upload_canceled")
	requireFreeSlots(t)
	if got := uploadTempFiles(t); got != 0 {
		t.Errorf("cancelled uploads left %d temp files", got)
	}
	f.nothingStored(t)
	wantCode(t, "avatar after the cancelled ones", postUpload(t, f.memberC, f.avatarPath, "avatar", tinyPNG(t), uploadOptions{}), http.StatusOK, "")
	wantCode(t, "cover after the cancelled ones", postUpload(t, f.leaderC, f.coverPath, "cover", tinyPNG(t), uploadOptions{}), http.StatusOK, "")
}

// uploadWave sends requests uploads at once, alternating the avatar and cover
// routes, and cancels every other one while it is in flight at staggered
// moments. It returns how many answered each status and fails unless every
// slot is free again afterwards.
func uploadWave(t *testing.T, f *uploadFixture, payload []byte, requests int, cancelHalf bool) map[int]int {
	t.Helper()
	codes := make(chan int, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		ctx, cancel := context.Background(), context.CancelFunc(func() {})
		if cancelHalf && i%2 == 1 {
			ctx, cancel = context.WithTimeout(ctx, time.Duration(50+40*i)*time.Millisecond)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer cancel()
			if i%2 == 0 {
				codes <- postUpload(t, f.memberC, f.avatarPath, "avatar", payload, uploadOptions{ctx: ctx}).Code
			} else {
				codes <- postUpload(t, f.leaderC, f.coverPath, "cover", payload, uploadOptions{ctx: ctx}).Code
			}
		}()
	}
	wg.Wait()
	close(codes)
	byStatus := map[int]int{}
	for code := range codes {
		byStatus[code]++
	}
	requireFreeSlots(t)
	return byStatus
}

// TestMeasureUploadRequests is the opt-in measurement of whole upload requests
// (FOLD_UPLOAD_MEASURE=1 with DATABASE_URL, docs/agents/testing.md): for each
// worst-case picture, in a process of its own, waves of concurrent avatar and
// cover requests, some cancelled while they run, through the real router. A
// control run sends bodies of the same size that are not pictures, so they take
// the same multipart parse and copy but are refused before any decode; what the
// picture adds over the control is the processing. Every slot must be free after
// each wave, no temp file left, and a valid upload must still work. The
// process peak with pictures, cancelled or not, must stay within
// images.ProcessAllowanceBytes plus the control's own peak, or it fails.
//
// Limits: the process is the whole test binary (database client, app and test
// client included), and each request's body also exists once more in the test
// client, so ingress is overstated, and VmHWM is a high-water mark of resident
// memory, not of what Go has mapped.
func TestMeasureUploadRequests(t *testing.T) {
	c, ok := imagetest.MeasureCase(t, "TestMeasureUploadRequests")
	if !ok {
		return
	}
	f := newUploadFixture(t)
	data := c.Build(t)
	if len(data) > images.MaxUploadBytes {
		t.Fatalf("%s is %d bytes, over the upload limit", c.Name, len(data))
	}
	control := bytes.Repeat([]byte("x"), len(data))
	t.Setenv("TMPDIR", t.TempDir()) // so a leaked temp file is seen
	const waves, requests = 3, 8

	measure := func(payload []byte, cancelHalf bool) (map[string]any, int64) {
		runtime.GC()
		debug.FreeOSMemory()
		resetOK := imagetest.ResetPeak()
		baseHWM := imagetest.ProcessKB("VmHWM")
		if !resetOK || baseHWM < 0 {
			t.Fatal("the process's peak resident memory cannot be reset or read here (Linux 4.0 or later with /proc): run the measurement there")
		}
		samples := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/total:bytes"}}
		metrics.Read(samples)
		baseObjects := samples[0].Value.Uint64()
		var peakObjects, peakMapped uint64
		stop, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			for {
				metrics.Read(samples)
				peakObjects, peakMapped = max(peakObjects, samples[0].Value.Uint64()), max(peakMapped, samples[1].Value.Uint64())
				select {
				case <-stop:
					return
				case <-time.After(time.Millisecond):
				}
			}
		}()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		answers := map[int]int{}
		for w := 0; w < waves; w++ {
			for code, n := range uploadWave(t, f, payload, requests, cancelHalf) {
				answers[code] += n
			}
		}
		close(stop)
		<-done
		runtime.ReadMemStats(&after)
		peakKB := imagetest.ProcessKB("VmHWM") - baseHWM
		return map[string]any{
			"answers":              answers,
			"peakHeapObjectsBytes": peakObjects - min(peakObjects, baseObjects),
			"peakGoMappedBytes":    peakMapped,
			"vmHWMOverBaselineKB":  peakKB,
			"vmHWMResetWorked":     resetOK,
			"totalAllocBytes":      after.TotalAlloc - before.TotalAlloc,
		}, peakKB
	}
	controlReport, controlKB := measure(control, false)
	picturesReport, picturesKB := measure(data, false)
	cancelledReport, cancelledKB := measure(data, true)
	// What a picture may add over the same bytes that are not one: the
	// processing allowance. The control's own peak is the ingress (bodies,
	// the test client's copies, the database client) and is allowed on top.
	allowanceKB := int64(images.ProcessAllowanceBytes)/1024 + controlKB
	report := map[string]any{
		"case":              c.Name,
		"inputBytes":        len(data),
		"slots":             images.MaxConcurrentProcessing,
		"waves":             waves,
		"requestsPerWave":   requests,
		"goVersion":         runtime.Version(),
		"allowanceKB":       allowanceKB,
		"controlNotPicture": controlReport,
		"pictures":          picturesReport,
		"picturesCancelled": cancelledReport,
	}
	left, _ := filepath.Glob(filepath.Join(os.TempDir(), "upload-*"))
	report["tempFilesLeft"] = len(left)
	out, _ := json.Marshal(report)
	t.Logf("MEASURE %s", out)
	if len(left) != 0 {
		t.Errorf("%d temp files left", len(left))
	}
	for name, kb := range map[string]int64{"pictures": picturesKB, "cancelled pictures": cancelledKB} {
		if kb > allowanceKB {
			t.Errorf("%s: the process peak grew %d kB, over the %d kB allowance (processing allowance plus the control's %d kB)", name, kb, allowanceKB, controlKB)
		}
	}
	requireFreeSlots(t)
	wantCode(t, "avatar after the waves", postUpload(t, f.memberC, f.avatarPath, "avatar", tinyPNG(t), uploadOptions{}), http.StatusOK, "")
	wantCode(t, "cover after the waves", postUpload(t, f.leaderC, f.coverPath, "cover", tinyPNG(t), uploadOptions{}), http.StatusOK, "")
}

// messageImageFixture is a leader with a saved draft in a group they lead,
// someone else's draft, and a client for a member who can't compose.
type messageImageFixture struct {
	composeFixture
	draft, other uint
}

func newMessageImageFixture(t *testing.T) messageImageFixture {
	t.Helper()
	f := messageImageFixture{composeFixture: newComposeFixture(t)}
	mk := func(c *apiClient, groupID uint) uint {
		var out struct {
			Message messageJSON `json:"message"`
		}
		w := c.do("POST", "/api/v1/messages", map[string]any{"subject": "Pictures", "bodyHtml": "<p>Hi</p>", "audience": map[string]any{"type": "group", "groupId": groupID}}, true)
		if w.Code != http.StatusCreated {
			t.Fatalf("draft: %d %s", w.Code, w.Body.String())
		}
		decodeJSON(t, w, &out)
		return out.Message.ID
	}
	f.draft = mk(f.leader, f.group.ID)
	f.other = mk(f.host, f.otherGroup.ID)
	return f
}

func (f messageImageFixture) imagesPath(id uint) string {
	return "/api/v1/messages/" + itoa(id) + "/images"
}

func (f messageImageFixture) upload(t *testing.T, c *apiClient, id uint, data []byte) messageImageResponseJSON {
	t.Helper()
	w := postUpload(t, c, f.imagesPath(id), "image", data, uploadOptions{})
	if w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	var out messageImageResponseJSON
	decodeJSON(t, w, &out)
	return out
}

// saveBody puts body in a draft the way the composer does.
func (f messageImageFixture) saveBody(t *testing.T, c *apiClient, id uint, groupID uint, body string) *httptest.ResponseRecorder {
	t.Helper()
	return c.do("PUT", "/api/v1/messages/"+itoa(id), map[string]any{"subject": "Pictures", "bodyHtml": body, "audience": map[string]any{"type": "group", "groupId": groupID}}, true)
}

func (f messageImageFixture) folder(t *testing.T, id uint) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.app.Uploads.(*storage.LocalStore).Dir, "messages", itoa(id)))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// A leader adds a picture to their own draft: it is stored under the
// message's folder, re-encoded (no EXIF, turned upright), served without a
// session, and answered as {key, url}. Nothing limits how many (Grier,
// Nov 14 2026).
func TestMessageImageUploadStoresReencodedPicture(t *testing.T) {
	f := newMessageImageFixture(t)
	in := imagetest.WithEXIF(imagetest.JPEGBytes(t, imagetest.BlockImage(2, 3, imagetest.GridColors)), imagetest.ExifBlock(6))
	got := f.upload(t, f.leader, f.draft, in)
	if !strings.HasPrefix(got.Key, "messages/"+itoa(f.draft)+"/") || !strings.HasSuffix(got.Key, ".jpg") || got.URL != "/uploads/"+got.Key {
		t.Fatalf("answer = %+v", got)
	}
	if _, ok := content.ImageMessageID(got.URL); !ok {
		t.Fatalf("the sanitizer would not keep %q", got.URL)
	}
	stored := uploadedBytes(t, f.app, got.URL)
	for name, pat := range map[string][]byte{"Exif marker": []byte("Exif"), "maker string": imagetest.GPSMarker, "GPS latitude": imagetest.GPSLatitude} {
		if bytes.Contains(stored, pat) {
			t.Errorf("stored picture still contains the %s", name)
		}
	}
	if _, err := jpeg.Decode(bytes.NewReader(stored)); err != nil {
		t.Fatalf("stored picture is not a JPEG: %v", err)
	}
	// Served to anyone with the address: an inbox has no session.
	req := httptest.NewRequest("GET", got.URL, nil)
	rec := httptest.NewRecorder()
	f.leader.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("signed-out read: %d %v", rec.Code, rec.Header())
	}
	// Admins may add to anyone's draft, and there is no count limit.
	for range 12 {
		f.upload(t, f.leader, f.draft, tinyPNG(t))
	}
	f.upload(t, f.admin, f.draft, tinyPNG(t))
	if n := len(f.folder(t, f.draft)); n != 14 {
		t.Fatalf("folder holds %d, want 14", n)
	}
}

func TestMessageImageUploadRefusals(t *testing.T) {
	f := newMessageImageFixture(t)
	tmp := uploadTempFiles(t)
	pic := tinyPNG(t)
	post := func(c *apiClient, id uint, data []byte, o uploadOptions) *httptest.ResponseRecorder {
		return postUpload(t, c, f.imagesPath(id), "image", data, o)
	}
	wantCode(t, "not an image", post(f.leader, f.draft, []byte("just words, not a picture"), uploadOptions{}), http.StatusBadRequest, "file_type")
	wantCode(t, "html", post(f.leader, f.draft, []byte("<svg onload=alert(1)>"), uploadOptions{}), http.StatusBadRequest, "file_type")
	wantCode(t, "empty", post(f.leader, f.draft, nil, uploadOptions{}), http.StatusBadRequest, "file_unreadable")
	wantCode(t, "over 5MB", post(f.leader, f.draft, append(append([]byte(nil), pic...), bytes.Repeat([]byte{0}, images.MaxUploadBytes)...), uploadOptions{}), http.StatusBadRequest, "file_too_large")
	wantCode(t, "no csrf", post(f.leader, f.draft, pic, uploadOptions{badToken: true}), http.StatusForbidden, "")
	wantCode(t, "someone else's draft", post(f.leader, f.other, pic, uploadOptions{}), http.StatusNotFound, "")
	wantCode(t, "not a composer", post(f.member, f.draft, pic, uploadOptions{}), http.StatusNotFound, "")
	wantCode(t, "no such message", post(f.leader, 999999, pic, uploadOptions{}), http.StatusNotFound, "")
	// A missing field is a missing file.
	wantCode(t, "wrong field", postUpload(t, f.leader, f.imagesPath(f.draft), "avatar", pic, uploadOptions{}), http.StatusBadRequest, "file_unreadable")
	// A message that is sending, sent or cancelled can't take another.
	for _, status := range []string{models.MessageSending, models.MessageSent, models.MessageFailed, models.MessageCancelled} {
		f.app.DB.Model(&models.Message{}).Where("id = ?", f.draft).Update("status", status)
		wantCode(t, status, post(f.leader, f.draft, pic, uploadOptions{}), http.StatusConflict, "message_locked")
	}
	// An automatic reminder is read only.
	f.app.DB.Model(&models.Message{}).Where("id = ?", f.draft).Updates(map[string]any{"status": models.MessageDraft, "category": models.MessageCategoryReminder})
	wantCode(t, "reminder", post(f.admin, f.draft, pic, uploadOptions{}), http.StatusConflict, "message_reminder")
	if n := len(f.folder(t, f.draft)) + len(f.folder(t, f.other)); n != 0 {
		t.Fatalf("a refused upload stored %d files", n)
	}
	if got := uploadTempFiles(t); got != tmp {
		t.Errorf("a refused upload left temp files: %d, was %d", got, tmp)
	}
	requireFreeSlots(t)
}

// A body may show only pictures uploaded to its own message.
func TestMessageSaveRefusesAnotherMessagesPicture(t *testing.T) {
	f := newMessageImageFixture(t)
	mine := f.upload(t, f.leader, f.draft, tinyPNG(t))
	theirs := f.upload(t, f.host, f.other, tinyPNG(t))
	img := func(url string) string { return `<p>Hi</p><img src="` + url + `" alt="A picture">` }

	w := f.saveBody(t, f.leader, f.draft, f.group.ID, img(mine.URL))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), mine.URL) {
		t.Fatalf("own picture: %d %s", w.Code, w.Body.String())
	}
	wantCode(t, "another message's", f.saveBody(t, f.leader, f.draft, f.group.ID, img(theirs.URL)), http.StatusBadRequest, "message_image")
	wantCode(t, "next message's id", f.saveBody(t, f.leader, f.draft, f.group.ID, img(strings.Replace(mine.URL, "/messages/"+itoa(f.draft)+"/", "/messages/"+itoa(f.draft+1)+"/", 1))), http.StatusBadRequest, "message_image")
	// A new message has no folder yet.
	w = f.leader.do("POST", "/api/v1/messages", map[string]any{"subject": "New", "bodyHtml": img(mine.URL), "audience": map[string]any{"type": "group", "groupId": f.group.ID}}, true)
	wantCode(t, "create with a picture", w, http.StatusBadRequest, "message_image")
	// An outside picture is dropped by the sanitizer, not refused.
	w = f.saveBody(t, f.leader, f.draft, f.group.ID, `<p>Hi</p><img src="https://evil.example/p.gif" alt="x">`)
	var out struct {
		Message messageJSON `json:"message"`
	}
	decodeJSON(t, w, &out)
	if w.Code != http.StatusOK || out.Message.BodyHTML != "<p>Hi</p>" {
		t.Fatalf("outside picture: %d %s", w.Code, w.Body.String())
	}
	var row models.Message
	f.app.DB.First(&row, f.draft)
	if strings.Contains(row.BodyText, "Image") {
		t.Fatalf("plain text: %q", row.BodyText)
	}
	// A picture with no description is dropped too; the plain text names the rest.
	f.saveBody(t, f.leader, f.draft, f.group.ID, `<p>Hi</p><img src="`+mine.URL+`" alt="  ">`)
	f.app.DB.First(&row, f.draft)
	if strings.Contains(row.BodyHTML, "<img") {
		t.Fatalf("blank description kept: %q", row.BodyHTML)
	}
	f.saveBody(t, f.leader, f.draft, f.group.ID, img(mine.URL))
	f.app.DB.First(&row, f.draft)
	if row.BodyText != "Hi\n\n[Image: A picture]" {
		t.Fatalf("plain text: %q", row.BodyText)
	}
}

// Pictures a draft's body no longer shows are removed when it is sent, and a
// discarded draft's all go; one another message holds stays.
func TestMessageImagesArePrunedAtSendAndDiscard(t *testing.T) {
	f := newMessageImageFixture(t)
	used := f.upload(t, f.leader, f.draft, tinyPNG(t))
	unused := f.upload(t, f.leader, f.draft, tinyPNG(t))
	elsewhere := f.upload(t, f.host, f.other, tinyPNG(t))
	if w := f.saveBody(t, f.leader, f.draft, f.group.ID, `<p>Hi</p><img src="`+used.URL+`" alt="Used">`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	// Saving never prunes: a picture is uploaded before the body that shows it.
	if n := len(f.folder(t, f.draft)); n != 2 {
		t.Fatalf("after save: %d files", n)
	}
	if w := f.leader.do("POST", "/api/v1/messages/"+itoa(f.draft)+"/schedule", map[string]any{}, true); w.Code != http.StatusOK {
		t.Fatalf("schedule: %d %s", w.Code, w.Body.String())
	}
	runMessageJobs(t, f.app)
	left := f.folder(t, f.draft)
	if len(left) != 1 || "messages/"+itoa(f.draft)+"/"+left[0] != used.Key {
		t.Fatalf("after send: %v, want only %s (not %s)", left, used.Key, unused.Key)
	}
	if len(f.folder(t, f.other)) != 1 || elsewhere.Key == "" {
		t.Fatal("another message's picture was touched")
	}
	// Discarding a draft removes every picture it has.
	if w := f.host.do("POST", "/api/v1/messages/"+itoa(f.other)+"/cancel", nil, true); w.Code != http.StatusOK {
		t.Fatalf("discard: %d %s", w.Code, w.Body.String())
	}
	if n := len(f.folder(t, f.other)); n != 0 {
		t.Fatalf("discarded draft keeps %d files", n)
	}
	if len(f.folder(t, f.draft)) != 1 {
		t.Fatal("a sent message's picture was removed")
	}
}

// pausingUploads holds each Put until released, so a test can run another
// request while a picture is half stored.
type pausingUploads struct {
	*storage.LocalStore
	entered chan struct{}
	release chan struct{}
}

func (p pausingUploads) Put(ctx context.Context, key string, src io.Reader, size int64, mime string) error {
	p.entered <- struct{}{}
	<-p.release
	return p.LocalStore.Put(ctx, key, src, size, mime)
}

// A send or a discard can't pass an upload that is storing its file: the
// upload holds the message's row, so the prune that follows sees the file and
// removes it. Without that the file would outlive the message with nothing to
// remove it.
func TestMessageImageUploadHoldsOffDiscardUntilStored(t *testing.T) {
	f := newMessageImageFixture(t)
	local := f.app.Uploads.(*storage.LocalStore)
	pause := pausingUploads{LocalStore: local, entered: make(chan struct{}, 1), release: make(chan struct{})}
	f.app.Uploads = pause
	uploaded := make(chan *httptest.ResponseRecorder, 1)
	pic := tinyPNG(t)
	go func() { uploaded <- postUpload(t, f.leader, f.imagesPath(f.draft), "image", pic, uploadOptions{}) }()
	awaitDBSignal(t, pause.entered)
	discard := bgDo(f.leader, "POST", "/api/v1/messages/"+itoa(f.draft)+"/cancel", nil)
	select {
	case r := <-discard:
		t.Fatalf("the discard finished while a picture was being stored: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
	close(pause.release)
	if w := <-uploaded; w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if r := awaitResult(t, discard); r.code != http.StatusOK {
		t.Fatalf("discard: %+v", r)
	}
	f.app.Uploads = local
	if n := len(f.folder(t, f.draft)); n != 0 {
		t.Fatalf("a discarded draft keeps %d files", n)
	}
	wantCode(t, "after discard", postUpload(t, f.leader, f.imagesPath(f.draft), "image", pic, uploadOptions{}), http.StatusConflict, "message_locked")
}
