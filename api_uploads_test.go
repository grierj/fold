package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
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
// each wave, no temp file left, and a valid upload must still work.
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

	measure := func(payload []byte, cancelHalf bool) map[string]any {
		runtime.GC()
		debug.FreeOSMemory()
		resetOK := imagetest.ResetPeak()
		baseHWM := imagetest.ProcessKB("VmHWM")
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
		return map[string]any{
			"answers":              answers,
			"peakHeapObjectsBytes": peakObjects - min(peakObjects, baseObjects),
			"peakGoMappedBytes":    peakMapped,
			"vmHWMOverBaselineKB":  imagetest.ProcessKB("VmHWM") - baseHWM,
			"vmHWMResetWorked":     resetOK,
			"totalAllocBytes":      after.TotalAlloc - before.TotalAlloc,
		}
	}
	report := map[string]any{
		"case":              c.Name,
		"inputBytes":        len(data),
		"slots":             images.MaxConcurrentProcessing,
		"waves":             waves,
		"requestsPerWave":   requests,
		"goVersion":         runtime.Version(),
		"controlNotPicture": measure(control, false),
		"pictures":          measure(data, false),
		"picturesCancelled": measure(data, true),
	}
	left, _ := filepath.Glob(filepath.Join(os.TempDir(), "upload-*"))
	report["tempFilesLeft"] = len(left)
	out, _ := json.Marshal(report)
	t.Logf("MEASURE %s", out)
	if len(left) != 0 {
		t.Errorf("%d temp files left", len(left))
	}
	requireFreeSlots(t)
	wantCode(t, "avatar after the waves", postUpload(t, f.memberC, f.avatarPath, "avatar", tinyPNG(t), uploadOptions{}), http.StatusOK, "")
	wantCode(t, "cover after the waves", postUpload(t, f.leaderC, f.coverPath, "cover", tinyPNG(t), uploadOptions{}), http.StatusOK, "")
}
