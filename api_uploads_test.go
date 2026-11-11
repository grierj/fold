package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// png16Header claims a 16-bit RGBA canvas of w x h and nothing else, the shape
// of the issue's small file that needs hundreds of megabytes to decode.
func png16Header(w, h uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 16, 6
	return append([]byte("\x89PNG\r\n\x1a\n"), imagetest.PNGChunk("IHDR", ihdr)...)
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
		"16-bit 5000x5000 PNG": {png16Header(5000, 5000), "image_too_large"},
		"aggregate-frame GIF":  {heavyGIF, "image_too_large"},
		"corrupt JPEG":         {[]byte("\xFF\xD8\xFF\xE0 not really a jpeg at all"), "file_unreadable"},
		"not an image":         {[]byte("<html>"), "file_type"},
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
