package push

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// decrypt is the receiver's side of RFC 8291, written separately from
// encrypt so the fake push service checks Fold's output with its own code.
func decrypt(body []byte, ua *ecdh.PrivateKey, authSecret []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("short body")
	}
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs < 18 || len(body) < 21+idlen {
		return nil, errors.New("bad header")
	}
	asPublic := body[21 : 21+idlen]
	ciphertext := body[21+idlen:]
	sender, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		return nil, err
	}
	shared, err := ua.ECDH(sender)
	if err != nil {
		return nil, err
	}
	cek, nonce, err := deriveKeys(shared, authSecret, salt, ua.PublicKey().Bytes(), asPublic)
	if err != nil {
		return nil, err
	}
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}
	// Strip padding back to the last-record delimiter.
	i := len(plain) - 1
	for i >= 0 && plain[i] == 0 {
		i--
	}
	if i < 0 || plain[i] != 0x02 {
		return nil, errors.New("no last-record delimiter")
	}
	return plain[:i], nil
}

// received is one request the fake push service decrypted.
type received struct {
	Path    string
	Header  http.Header
	Payload Payload
	Claims  map[string]any
}

// fakeService is a push service on httptest: it holds the RFC 8291
// subscriber keys, decrypts each request, verifies the VAPID token against
// the server key it was told to expect, and answers whatever the test
// sets (201 by default).
type fakeService struct {
	t      *testing.T
	srv    *httptest.Server
	ua     *ecdh.PrivateKey
	auth   []byte
	vapid  *ecdsa.PrivateKey
	status atomic.Int64
	retry  atomic.Value // string Retry-After
	mu     sync.Mutex
	got    []received
}

func newFakeService(t *testing.T) *fakeService {
	t.Helper()
	ua, err := ecdh.P256().NewPrivateKey(b64(t, rfcUAPrivate))
	if err != nil {
		t.Fatal(err)
	}
	vapid, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeService{t: t, ua: ua, auth: b64(t, rfcAuth), vapid: vapid}
	f.status.Store(http.StatusCreated)
	f.retry.Store("")
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeService) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec := received{Path: r.URL.Path, Header: r.Header.Clone()}
	plain, err := decrypt(body, f.ua, f.auth)
	if err != nil {
		f.t.Errorf("fake push service: decrypt: %v", err)
	} else {
		_ = json.Unmarshal(plain, &rec.Payload) // tests that care check it
	}
	claims, err := verifyVAPID(r.Header.Get("Authorization"), &f.vapid.PublicKey)
	if err != nil {
		f.t.Errorf("fake push service: authorization: %v", err)
	}
	rec.Claims = claims
	f.mu.Lock()
	f.got = append(f.got, rec)
	f.mu.Unlock()
	if v := f.retry.Load().(string); v != "" {
		w.Header().Set("Retry-After", v)
	}
	w.WriteHeader(int(f.status.Load()))
	_, _ = w.Write([]byte(strings.Repeat("x", 10000))) // more than Fold reads
}

func (f *fakeService) requests() []received {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]received{}, f.got...)
}

// client sends to the fake: the endpoint names a real push host (so the
// allow-list passes), and the transport dials the fake instead.
func (f *fakeService) client() *Client {
	tr := f.srv.Client().Transport.(*http.Transport).Clone()
	addr := f.srv.Listener.Addr().String()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	tr.TLSClientConfig.ServerName = "example.com" // httptest's certificate
	hc := NewHTTPClient()
	hc.Transport = tr
	return &Client{HTTP: hc, Subject: "mailto:admin@church.example",
		Key: func(context.Context) (*ecdsa.PrivateKey, error) { return f.vapid, nil }}
}

const testEndpoint = "https://fcm.googleapis.com/fcm/send/secret-device-token-123"

func testSub() Subscription {
	return Subscription{Endpoint: testEndpoint, P256dh: rfcUAPublic, Auth: rfcAuth}
}

// verifyVAPID parses "vapid t=<jwt>, k=<key>", checks k is want and the
// ES256 signature, and returns the claims.
func verifyVAPID(header string, want *ecdsa.PublicKey) (map[string]any, error) {
	rest, ok := strings.CutPrefix(header, "vapid ")
	if !ok {
		return nil, errors.New("not a vapid header")
	}
	var token, k string
	for _, part := range strings.Split(rest, ",") {
		name, value, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch name {
		case "t":
			token = value
		case "k":
			k = value
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(k)
	if err != nil {
		return nil, err
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		return nil, err
	}
	if !pub.Equal(want) {
		return nil, errors.New("k is not the server's key")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWT")
	}
	var head map[string]string
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if json.Unmarshal(hb, &head) != nil || head["alg"] != "ES256" || head["typ"] != "JWT" {
		return nil, errors.New("bad JWT header")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return nil, errors.New("bad signature encoding")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return nil, errors.New("signature does not verify")
	}
	var claims map[string]any
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if err := json.Unmarshal(cb, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func TestFakeServiceDecryptsWhatFoldSends(t *testing.T) {
	f := newFakeService(t)
	c := f.client()
	now := time.Now()
	payload, err := MarshalPayload(NewPayload(Notification{Title: "Cancelled: Youth night", Body: "Tonight", Navigate: "https://church.example/events/4", Tag: "n9"}))
	if err != nil {
		t.Fatal(err)
	}
	r := c.Send(context.Background(), testSub(), payload, Options{TTL: 6 * time.Hour, Urgency: UrgencyHigh, Topic: "n9"})
	if r.Status != Accepted || r.Code != http.StatusCreated || r.Err != nil {
		t.Fatalf("result %+v", r)
	}
	got := f.requests()
	if len(got) != 1 {
		t.Fatalf("%d requests", len(got))
	}
	g := got[0]
	if g.Path != "/fcm/send/secret-device-token-123" {
		t.Errorf("path %q", g.Path)
	}
	for name, want := range map[string]string{
		"TTL": "21600", "Urgency": "high", "Topic": "n9", "Content-Encoding": "aes128gcm", "Content-Type": "application/octet-stream",
	} {
		if v := g.Header.Get(name); v != want {
			t.Errorf("%s: %q, want %q", name, v, want)
		}
	}
	if g.Payload.WebPush != 8030 || g.Payload.Notification.Title != "Cancelled: Youth night" || g.Payload.Notification.Navigate != "https://church.example/events/4" {
		t.Errorf("payload %+v", g.Payload)
	}
	if g.Claims["aud"] != "https://fcm.googleapis.com" || g.Claims["sub"] != "mailto:admin@church.example" {
		t.Errorf("claims %v", g.Claims)
	}
	exp := time.Unix(int64(g.Claims["exp"].(float64)), 0)
	if exp.Before(now) || exp.After(now.Add(24*time.Hour)) {
		t.Errorf("exp %v is not within 24 hours", exp)
	}
}

func TestSendResultsByStatus(t *testing.T) {
	f := newFakeService(t)
	c := f.client()
	for _, tc := range []struct {
		code  int
		retry string // sent by the service; the jobs backoff decides instead
		want  Status
	}{
		{201, "", Accepted},
		{404, "", Gone},
		{410, "", Gone},
		{413, "", TooLarge},
		{429, "120", RateLimited},
		{500, "", Temporary},
		{503, "7", Temporary},
		{403, "", Failed},
		{400, "", Failed},
	} {
		f.status.Store(int64(tc.code))
		f.retry.Store(tc.retry)
		r := c.Send(context.Background(), testSub(), []byte(`{"web_push":8030}`), Options{TTL: time.Hour})
		if r.Status != tc.want || r.Code != tc.code {
			t.Errorf("%d: %+v, want %s", tc.code, r, tc.want)
		}
		if tc.want != Accepted && (r.Err == nil || strings.Contains(r.Err.Error(), "secret-device-token")) {
			t.Errorf("%d: error %v", tc.code, r.Err)
		}
	}
}

func TestSendRefusesBeforeConnecting(t *testing.T) {
	f := newFakeService(t)
	c := f.client()
	big := []byte(strings.Repeat("a", MaxPlaintext+1))
	if r := c.Send(context.Background(), testSub(), big, Options{}); r.Status != TooLarge || !errors.Is(r.Err, ErrTooLarge) {
		t.Errorf("over the cap: %+v", r)
	}
	if r := c.Send(context.Background(), testSub(), []byte(strings.Repeat("a", MaxPlaintext)), Options{}); r.Status != Accepted {
		t.Errorf("at the cap: %+v", r)
	}
	off := testSub()
	off.Endpoint = "https://push.evil.example/x"
	if r := c.Send(context.Background(), off, []byte("{}"), Options{}); r.Status != Failed || !errors.Is(r.Err, ErrNotAllowed) {
		t.Errorf("off the allow-list: %+v", r)
	}
	for _, topic := range []string{"n:1", strings.Repeat("a", 33), "a b"} {
		if r := c.Send(context.Background(), testSub(), []byte("{}"), Options{Topic: topic}); r.Status != Failed {
			t.Errorf("topic %q: %+v", topic, r)
		}
	}
	if n := len(f.requests()); n != 1 {
		t.Errorf("%d requests reached the push service, want only the one at the cap", n)
	}
}

func TestSendDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Store(true) }))
	defer target.Close()
	f := newFakeService(t)
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/internal", http.StatusTemporaryRedirect)
	})
	r := f.client().Send(context.Background(), testSub(), []byte("{}"), Options{})
	if r.Status != Failed || r.Code != http.StatusTemporaryRedirect || followed.Load() {
		t.Fatalf("redirect: %+v followed=%v", r, followed.Load())
	}
}

func TestPayloadShape(t *testing.T) {
	badge := 3
	b, err := MarshalPayload(NewPayload(Notification{Title: "T", Navigate: "https://x.example/", AppBadge: &badge}))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"web_push":8030,"notification":{"title":"T","navigate":"https://x.example/","app_badge":3}}` {
		t.Fatalf("%s", b)
	}
	if _, err := MarshalPayload(NewPayload(Notification{Title: strings.Repeat("a", MaxPlaintext)})); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the cap: %v", err)
	}
}

func TestAudienceLeavesOutTheDefaultPort(t *testing.T) {
	for in, want := range map[string]string{
		"https://fcm.googleapis.com/fcm/send/x":     "https://fcm.googleapis.com",
		"https://fcm.googleapis.com:443/fcm/send/x": "https://fcm.googleapis.com",
		"https://push.example.org:8443/x":           "https://push.example.org:8443",
	} {
		if got, err := audience(in); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", in, got, err, want)
		}
	}
}

func TestClientMakesItsTransportOnce(t *testing.T) {
	c := &Client{}
	off := testSub()
	off.Endpoint = "https://push.evil.example/x" // refused before any connection
	c.Send(context.Background(), off, []byte("{}"), Options{})
	first, hosts := c.HTTP, c.Hosts
	c.Send(context.Background(), off, []byte("{}"), Options{})
	if first == nil || c.HTTP != first || c.Hosts != hosts {
		t.Fatal("the client made a new transport or allow-list for a second send")
	}
}

func TestTokenLeavesOutAnEmptySubject(t *testing.T) {
	key, _ := GenerateKey()
	h, err := vapidHeader(key, testEndpoint, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifyVAPID(h, &key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := claims["sub"]; ok {
		t.Fatalf("claims %v", claims)
	}
}
