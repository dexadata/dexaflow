package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/oidc"
)

// fakeDeviceStore is CLIDeviceStore in memory, with the semantics the SQL
// gives: one decision per sign-in, one redemption, denied answered once.
// outcome, when set, is what every poll answers instead.
type fakeDeviceStore struct {
	mu      sync.Mutex
	byID    map[string]*fakeDevice
	outcome *auth.DevicePollOutcome
	hashes  []string
	seq     int
	// lastStart is the request of the last start.
	lastStart auth.DeviceAuthorizationRequest
}

type fakeDevice struct {
	id, deviceHash, userHash string
	requesterHash, addr      string
	status                   string
	user                     auth.User
	created, expires         time.Time
}

func newFakeDeviceStore() *fakeDeviceStore {
	return &fakeDeviceStore{byID: map[string]*fakeDevice{}}
}

func (s *fakeDeviceStore) CreateCLIDeviceAuthorization(_ context.Context, req auth.DeviceAuthorizationRequest) (auth.DeviceAuthorization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastStart = req
	if len(s.byID) >= req.MaxLive {
		return auth.DeviceAuthorization{}, auth.ErrDeviceSignInsFull
	}
	fromRequester := 0
	for _, d := range s.byID {
		if d.userHash == req.UserCodeHash {
			return auth.DeviceAuthorization{}, domain.ErrConflict
		}
		if d.requesterHash == req.RequesterHash {
			fromRequester++
		}
	}
	if fromRequester >= req.MaxLivePerRequester {
		return auth.DeviceAuthorization{}, auth.ErrDeviceSignInsFromRequester
	}
	s.seq++
	id := fmt.Sprintf("0b6c9a52-3f0e-4c55-9d6f-%012d", s.seq)
	now := time.Now()
	s.byID[id] = &fakeDevice{
		id: id, deviceHash: req.DeviceCodeHash, userHash: req.UserCodeHash, requesterHash: req.RequesterHash, addr: req.RequesterAddr,
		status: "pending", created: now, expires: now.Add(req.TTL),
	}
	s.hashes = append(s.hashes, req.DeviceCodeHash, req.UserCodeHash, req.RequesterHash)
	return auth.DeviceAuthorization{ID: id, CreatedAt: now, ExpiresAt: now.Add(req.TTL), RequesterAddr: req.RequesterAddr}, nil
}

func (s *fakeDeviceStore) FindCLIDeviceByUserCode(_ context.Context, userHash string) (auth.DeviceAuthorization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.byID {
		if d.userHash == userHash && d.status == "pending" && time.Now().Before(d.expires) {
			return auth.DeviceAuthorization{ID: d.id, CreatedAt: d.created, ExpiresAt: d.expires, RequesterAddr: d.addr}, nil
		}
	}
	return auth.DeviceAuthorization{}, domain.ErrNotFound
}

func (s *fakeDeviceStore) DecideCLIDeviceAuthorization(_ context.Context, id string, approve bool, user auth.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.byID[id]
	if !ok || d.status != "pending" || !time.Now().Before(d.expires) {
		return domain.ErrNotFound
	}
	if approve {
		d.status, d.user = "approved", user
	} else {
		d.status = "denied"
	}
	return nil
}

func (s *fakeDeviceStore) PollCLIDeviceAuthorization(_ context.Context, deviceHash string) (auth.DevicePoll, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outcome != nil {
		return auth.DevicePoll{Outcome: *s.outcome}, nil
	}
	for id, d := range s.byID {
		if d.deviceHash != deviceHash {
			continue
		}
		switch {
		case !time.Now().Before(d.expires):
			return auth.DevicePoll{Outcome: auth.DevicePollExpired}, nil
		case d.status == "approved":
			delete(s.byID, id)
			return auth.DevicePoll{Outcome: auth.DevicePollApproved, User: d.user}, nil
		case d.status == "denied":
			delete(s.byID, id)
			return auth.DevicePoll{Outcome: auth.DevicePollDenied}, nil
		}
		return auth.DevicePoll{Outcome: auth.DevicePollPending}, nil
	}
	return auth.DevicePoll{Outcome: auth.DevicePollUnknown}, nil
}

func (s *fakeDeviceStore) onlyID(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.byID {
		return id
	}
	t.Fatal("the store holds no sign-in")
	return ""
}

func (s *fakeDeviceStore) status(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.byID) != 1 {
		t.Fatalf("the store holds %d sign-ins, want 1", len(s.byID))
	}
	for _, d := range s.byID {
		return d.status
	}
	return ""
}

// deviceServer is oidcServer with the device sign-in wired.
func deviceServer(t *testing.T, f *fakeIDP, cfg config.OIDCSection, users OIDCUserStore, audit AuthAuditWriter, devices CLIDeviceStore, logger *slog.Logger) *gin.Engine {
	t.Helper()
	flow, err := oidc.NewFlow(context.Background(), cfg, testHS256Secret)
	if err != nil {
		t.Fatalf("NewFlow: %v", err)
	}
	if logger == nil {
		logger = discardLogger()
	}
	return NewServer(Dependencies{
		Logger:        logger,
		Authenticator: &fakeAuthn{},
		RateLimiter:   auth.NewRateLimiter(50, time.Minute),
		HealthChecks:  map[string]HealthChecker{},
		CORSOrigins:   []string{"*"},
		TokenTTLSecs:  3600,
		OIDCFlow:      flow,
		OIDCEnabled:   true,
		OIDCSettings:  cfg,
		OIDCUsers:     users,
		AuthAudit:     audit,
		JWTSecret:     testHS256Secret,
		CLIDevices:    devices,
	})
}

type deviceFixture struct {
	srv     *gin.Engine
	idp     *fakeIDP
	cfg     config.OIDCSection
	devices *fakeDeviceStore
	audit   *fakeAuthAudit
	users   *fakeOIDCStore
}

func newDeviceFixture(t *testing.T) deviceFixture {
	t.Helper()
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	users := newFakeOIDCStore()
	users.seed(f.issuer, "subject-123", &auth.User{ID: "u-1", TenantID: "default", Email: "alice@corp.example"}, true)
	devices, audit := newFakeDeviceStore(), &fakeAuthAudit{}
	return deviceFixture{srv: deviceServer(t, f, cfg, users, audit, devices, nil), idp: f, cfg: cfg, devices: devices, audit: audit, users: users}
}

// deviceStart is what POST /api/v2/auth/cli/device answers.
type deviceStart struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// startFrom posts a start from addr ("" keeps httptest's 192.0.2.1).
func startFrom(srv http.Handler, addr string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, cliDevicePath, http.NoBody)
	if addr != "" {
		req.RemoteAddr = net.JoinHostPort(addr, "40000")
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func startDevice(t *testing.T, srv http.Handler) deviceStart {
	t.Helper()
	return startDeviceFrom(t, srv, "")
}

func startDeviceFrom(t *testing.T, srv http.Handler, addr string) deviceStart {
	t.Helper()
	rec := startFrom(srv, addr)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d %s, want 200", cliDevicePath, rec.Code, rec.Body.String())
	}
	var d deviceStart
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return d
}

func pollDevice(srv http.Handler, deviceCode string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"grant_type": deviceGrantType, "device_code": deviceCode})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, cliTokenPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// tokenError reads the OAuth error of a refused poll.
func tokenError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("poll = %d %s, want 400", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Error
}

// submitUserCode posts the verification form as the browser would.
func submitUserCode(srv http.Handler, code, fetchSite string) *httptest.ResponseRecorder {
	return submitUserCodeFrom(srv, code, fetchSite, "")
}

// submitUserCodeFrom is submitUserCode from addr ("" keeps 192.0.2.1).
func submitUserCodeFrom(srv http.Handler, code, fetchSite, addr string) *httptest.ResponseRecorder {
	form := url.Values{"user_code": {code}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, deviceVerifyPath, strings.NewReader(form.Encode()))
	if addr != "" {
		req.RemoteAddr = net.JoinHostPort(addr, "40000")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if fetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", fetchSite)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// verifyAndSignIn enters the user code and returns the IdP-side state of the
// sign-in it started.
func verifyAndSignIn(t *testing.T, srv http.Handler, code string) loginState {
	t.Helper()
	rec := submitUserCode(srv, code, "same-origin")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("verify = %d %s, want a %d redirect to the IdP", rec.Code, rec.Body.String(), http.StatusSeeOther)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	var cookie *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == oidcStateCookie {
			cookie = ck
		}
	}
	if cookie == nil {
		t.Fatal("verify set no state cookie")
	}
	q := loc.Query()
	return loginState{state: q.Get("state"), nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), cookie: cookie}
}

// deviceConsentOf reads the device consent page: its cookie and CSRF token.
func deviceConsentOf(t *testing.T, rec *httptest.ResponseRecorder) (cookie *http.Cookie, csrf string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("callback = %d (Location %q), want the 200 consent page", rec.Code, rec.Header().Get("Location"))
	}
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == cliDeviceConsentCookie && ck.Value != "" {
			cookie = ck
		}
	}
	if cookie == nil {
		t.Fatal("the device consent page set no consent cookie")
	}
	m := csrfField.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("the device consent page has no csrf field:\n%s", rec.Body.String())
	}
	return cookie, m[1]
}

func postDeviceConsent(srv http.Handler, cookie *http.Cookie, csrf, decision, confirm, fetchSite string) *httptest.ResponseRecorder {
	form := url.Values{"decision": {decision}, "csrf": {csrf}}
	if confirm != "" {
		form.Set("confirm", confirm)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, cliDeviceConsentPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if fetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", fetchSite)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// signInForDevice runs the browser half up to the consent page.
func (fx deviceFixture) signInForDevice(t *testing.T, userCode string, mutate func(jwt.MapClaims)) *httptest.ResponseRecorder {
	t.Helper()
	st := verifyAndSignIn(t, fx.srv, userCode)
	claims := baseClaims(fx.cfg, st.nonce)
	if mutate != nil {
		mutate(claims)
	}
	return completeCallback(fx.srv, fx.idp, st, fx.idp.signIDToken(claims))
}

var userCodeShape = regexp.MustCompile(`^[BCDFGHJKLMNPQRSTVWXZ]{4}-[BCDFGHJKLMNPQRSTVWXZ]{4}$`)

func TestCLIDeviceStartAnswersTheRFC8628Shape(t *testing.T) {
	fx := newDeviceFixture(t)
	d := startDevice(t, fx.srv)
	if !userCodeShape.MatchString(d.UserCode) {
		t.Errorf("user_code = %q, want two groups of four consonants", d.UserCode)
	}
	if len(d.DeviceCode) < 43 {
		t.Errorf("device_code %q is shorter than 256 bits of base64url", d.DeviceCode)
	}
	if d.VerificationURI != "https://app.example"+deviceVerifyPath {
		t.Errorf("verification_uri = %q, want the server's public origin from the OIDC redirect URL", d.VerificationURI)
	}
	if d.VerificationURIComplete != d.VerificationURI+"?user_code="+d.UserCode {
		t.Errorf("verification_uri_complete = %q", d.VerificationURIComplete)
	}
	if d.ExpiresIn != int(cliDeviceTTL.Seconds()) || d.Interval != int(cliDeviceInterval.Seconds()) {
		t.Errorf("expires_in %d, interval %d; want %v and %v", d.ExpiresIn, d.Interval, cliDeviceTTL, cliDeviceInterval)
	}
	for _, h := range fx.devices.hashes {
		if h == d.DeviceCode || strings.Contains(h, strings.ReplaceAll(d.UserCode, "-", "")) || len(h) != 64 {
			t.Errorf("the store received %q, want only 64-character hashes of the codes", h)
		}
	}
}

func TestCLIDeviceCodesAreDistinct(t *testing.T) {
	fx := newDeviceFixture(t)
	a, b := startDevice(t, fx.srv), startDevice(t, fx.srv)
	if a.DeviceCode == b.DeviceCode || a.UserCode == b.UserCode {
		t.Fatalf("two sign-ins share a code: %+v %+v", a, b)
	}
}

func TestCLIDeviceSignInIssuesTheBrowserFlowToken(t *testing.T) {
	fx := newDeviceFixture(t)
	d := startDevice(t, fx.srv)
	if got := tokenError(t, pollDevice(fx.srv, d.DeviceCode)); got != "authorization_pending" {
		t.Fatalf("poll before approval = %q, want authorization_pending", got)
	}
	rec := fx.signInForDevice(t, strings.ToLower(strings.ReplaceAll(d.UserCode, "-", " ")), nil)
	if ck := sessionCookie(rec); ck != nil {
		t.Errorf("a device sign-in set the browser session cookie %q", ck.Name)
	}
	body := rec.Body.String()
	for _, want := range []string{d.UserCode, "alice@corp.example", "default", `name="confirm"`, "required"} {
		if !strings.Contains(body, want) {
			t.Errorf("the consent page does not show %q:\n%s", want, body)
		}
	}
	if got := tokenError(t, pollDevice(fx.srv, d.DeviceCode)); got != "authorization_pending" {
		t.Fatalf("poll before the click = %q, want authorization_pending", got)
	}
	cookie, csrf := deviceConsentOf(t, rec)
	done := postDeviceConsent(fx.srv, cookie, csrf, consentApprove, "yes", "same-origin")
	if done.Code != http.StatusOK || !strings.Contains(done.Body.String(), "terminal") {
		t.Fatalf("approve = %d %s, want the page telling the person to return to the terminal", done.Code, done.Body.String())
	}
	token := accessToken(t, pollDevice(fx.srv, d.DeviceCode))
	claims := parseClaims(t, token)
	if claims["sub"] != "u-1" || claims["tenant_id"] != "default" || claims["email"] != "alice@corp.example" {
		t.Errorf("token claims = %v, want the signed-in user", claims)
	}
	if got := tokenRoles(t, token); len(got) != 1 || got[0] != "editor" {
		t.Errorf("roles = %v, want [editor] from the role mapping", got)
	}
	if got := tokenError(t, pollDevice(fx.srv, d.DeviceCode)); got != "invalid_grant" {
		t.Errorf("poll after redemption = %q, want invalid_grant", got)
	}
	if !fx.audit.has(auditOIDCLoginSuccess, "success") || !fx.audit.has(auditCLIConsent, "success") || !fx.audit.has(auditCLICode, "success") {
		t.Errorf("audit = %+v, want the login, the consent and the redemption", fx.audit.events)
	}
}

// The tenant pin, the role mapping and every other check of the browser flow
// apply: a refused sign-in goes back to the device page and leaves the
// sign-in pending, never approved.
func TestCLIDeviceSignInKeepsTheTenantPin(t *testing.T) {
	fx := newDeviceFixture(t)
	d := startDevice(t, fx.srv)
	rec := fx.signInForDevice(t, d.UserCode, func(c jwt.MapClaims) { c["tid"] = "other-tenant-guid" })
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), deviceVerifyPath+"?"+ssoErrorParam+"=") {
		t.Fatalf("refused sign-in = %d to %q, want the device page with the sso_error marker", rec.Code, rec.Header().Get("Location"))
	}
	if !fx.audit.hasReason(auditOIDCTenantReject, "tenant_not_allowed") {
		t.Errorf("audit = %+v, want the tenant pin rejection", fx.audit.events)
	}
	if s := fx.devices.status(t); s != "pending" {
		t.Errorf("sign-in status = %q, want pending", s)
	}
}

func TestCLIDeviceApprovalNeedsTheCodeConfirmed(t *testing.T) {
	fx := newDeviceFixture(t)
	d := startDevice(t, fx.srv)
	cookie, csrf := deviceConsentOf(t, fx.signInForDevice(t, d.UserCode, nil))
	rec := postDeviceConsent(fx.srv, cookie, csrf, consentApprove, "", "same-origin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("approve without confirming = %d, want 400", rec.Code)
	}
	if s := fx.devices.status(t); s != "pending" {
		t.Errorf("sign-in status = %q, want pending", s)
	}
	if !fx.audit.hasReason(auditCLIConsent, "code_not_confirmed") {
		t.Errorf("audit = %+v, want code_not_confirmed", fx.audit.events)
	}
}

func TestCLIDeviceCancelIsAccessDenied(t *testing.T) {
	fx := newDeviceFixture(t)
	d := startDevice(t, fx.srv)
	cookie, csrf := deviceConsentOf(t, fx.signInForDevice(t, d.UserCode, nil))
	if rec := postDeviceConsent(fx.srv, cookie, csrf, consentCancel, "", "same-origin"); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d, want 200", rec.Code)
	}
	if got := tokenError(t, pollDevice(fx.srv, d.DeviceCode)); got != "access_denied" {
		t.Fatalf("poll after cancel = %q, want access_denied", got)
	}
}

func TestCLIDeviceConsentRefusesForgedPosts(t *testing.T) {
	fx := newDeviceFixture(t)
	d := startDevice(t, fx.srv)
	cookie, csrf := deviceConsentOf(t, fx.signInForDevice(t, d.UserCode, nil))
	if rec := postDeviceConsent(fx.srv, cookie, csrf, consentApprove, "yes", "cross-site"); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site approve = %d, want 403", rec.Code)
	}
	if rec := postDeviceConsent(fx.srv, cookie, "wrong", consentApprove, "yes", "same-origin"); rec.Code != http.StatusBadRequest {
		t.Errorf("approve with a wrong csrf = %d, want 400", rec.Code)
	}
	if rec := postDeviceConsent(fx.srv, nil, csrf, consentApprove, "yes", "same-origin"); rec.Code != http.StatusBadRequest {
		t.Errorf("approve without the cookie = %d, want 400", rec.Code)
	}
	// A loopback consent is sealed for another purpose and is refused here,
	// even naming this very sign-in.
	codes := newCLICodes(testHS256Secret)
	loopback, err := codes.seal(cliPending{CSRF: "c", Device: fx.devices.onlyID(t), UserID: "u-1", Tenant: "default"}, cliConsentAAD, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if rec := postDeviceConsent(fx.srv, &http.Cookie{Name: cliDeviceConsentCookie, Value: loopback}, "c", consentApprove, "yes", "same-origin"); rec.Code != http.StatusBadRequest {
		t.Errorf("approve with a loopback consent = %d, want 400", rec.Code)
	}
	if s := fx.devices.status(t); s != "pending" {
		t.Errorf("sign-in status = %q after forged posts, want pending", s)
	}
}

// A replayed callback of a device sign-in cannot open a second consent.
func TestCLIDeviceSignInStateIsSingleUse(t *testing.T) {
	fx := newDeviceFixture(t)
	d := startDevice(t, fx.srv)
	st := verifyAndSignIn(t, fx.srv, d.UserCode)
	idToken := fx.idp.signIDToken(baseClaims(fx.cfg, st.nonce))
	deviceConsentOf(t, completeCallback(fx.srv, fx.idp, st, idToken))
	replay := completeCallback(fx.srv, fx.idp, st, idToken)
	if replay.Code != http.StatusFound || !strings.HasPrefix(replay.Header().Get("Location"), deviceVerifyPath+"?") {
		t.Fatalf("replayed callback = %d to %q, want the device page", replay.Code, replay.Header().Get("Location"))
	}
	if !fx.audit.hasReason(auditOIDCLoginFailure, "state_replayed") {
		t.Errorf("audit = %+v, want state_replayed", fx.audit.events)
	}
}

func TestCLIDeviceConsentIsSingleUse(t *testing.T) {
	fx := newDeviceFixture(t)
	d := startDevice(t, fx.srv)
	cookie, csrf := deviceConsentOf(t, fx.signInForDevice(t, d.UserCode, nil))
	if rec := postDeviceConsent(fx.srv, cookie, csrf, consentCancel, "", "same-origin"); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d", rec.Code)
	}
	if rec := postDeviceConsent(fx.srv, cookie, csrf, consentApprove, "yes", "same-origin"); rec.Code != http.StatusBadRequest {
		t.Fatalf("replayed consent = %d, want 400", rec.Code)
	}
}

// An unknown code and a malformed one get the same page, and neither starts
// a sign-in at the IdP.
func TestCLIDeviceUnknownUserCodesLookAlike(t *testing.T) {
	fx := newDeviceFixture(t)
	startDevice(t, fx.srv)
	unknown := submitUserCode(fx.srv, "BBBB-BBBB", "same-origin")
	malformed := submitUserCode(fx.srv, "not a code <script>", "same-origin")
	for name, rec := range map[string]*httptest.ResponseRecorder{"unknown": unknown, "malformed": malformed} {
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Errorf("%s code = %d to %q, want a 400 page", name, rec.Code, rec.Header().Get("Location"))
		}
	}
	strip := regexp.MustCompile(`value="[^"]*"`)
	if strip.ReplaceAllString(unknown.Body.String(), "") != strip.ReplaceAllString(malformed.Body.String(), "") {
		t.Error("an unknown and a malformed code get different pages")
	}
	if strings.Contains(malformed.Body.String(), "<script>") {
		t.Error("the page echoes the submitted text unescaped")
	}
	// An unknown code is tied to no sign-in: it is logged, never audited,
	// so anonymous guesses cannot flood the audit trail.
	if len(fx.audit.events) != 0 {
		t.Errorf("audit = %+v, want no rows for unknown codes", fx.audit.events)
	}
}

func TestCLIDeviceVerifyRefusesCrossSitePosts(t *testing.T) {
	fx := newDeviceFixture(t)
	d := startDevice(t, fx.srv)
	if rec := submitUserCode(fx.srv, d.UserCode, "cross-site"); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site verify = %d, want 403", rec.Code)
	}
}

func TestCLIDeviceVerifyIsRateLimited(t *testing.T) {
	fx := newDeviceFixture(t)
	var last *httptest.ResponseRecorder
	for range deviceVerifyRateLimitPerMinute + 1 {
		last = submitUserCode(fx.srv, "BBBB-BBBB", "same-origin")
	}
	if last.Code != http.StatusTooManyRequests || !strings.Contains(last.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("attempt %d in a minute = %d %q, want a 429 page", deviceVerifyRateLimitPerMinute+1, last.Code, last.Header().Get("Content-Type"))
	}
}

func TestCLIDeviceVerifyPagePrefillsOnlyAWellFormedCode(t *testing.T) {
	fx := newDeviceFixture(t)
	get := func(q string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, deviceVerifyPath+q, http.NoBody)
		rec := httptest.NewRecorder()
		fx.srv.ServeHTTP(rec, req)
		return rec
	}
	rec := get("?user_code=bcdf-ghjk")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `value="BCDF-GHJK"`) {
		t.Fatalf("page = %d, want the code prefilled as BCDF-GHJK:\n%s", rec.Code, rec.Body.String())
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q, want frame-ancestors 'none'", csp)
	}
	if body := get("?user_code=%22%3E%3Cb%3E").Body.String(); strings.Contains(body, "<b>") || strings.Contains(body, `value="&#34;`) {
		t.Errorf("a malformed code reached the page:\n%s", body)
	}
}

func TestCLIDevicePollOutcomesMapToRFC8628Errors(t *testing.T) {
	for outcome, want := range map[auth.DevicePollOutcome]string{
		auth.DevicePollUnknown:  "invalid_grant",
		auth.DevicePollPending:  "authorization_pending",
		auth.DevicePollSlowDown: "slow_down",
		auth.DevicePollExpired:  "expired_token",
		auth.DevicePollDenied:   "access_denied",
	} {
		fx := newDeviceFixture(t)
		o := outcome
		fx.devices.outcome = &o
		if got := tokenError(t, pollDevice(fx.srv, "some-device-code-that-is-long-enough-0123456789")); got != want {
			t.Errorf("outcome %v answered %q, want %q", outcome, got, want)
		}
	}
}

func TestCLIDevicePollRefusalsLookAlike(t *testing.T) {
	fx := newDeviceFixture(t)
	unknown := pollDevice(fx.srv, "an-unknown-device-code-of-a-plausible-length-012")
	malformed := pollDevice(fx.srv, "")
	if unknown.Body.String() != malformed.Body.String() {
		t.Errorf("unknown %s and malformed %s device codes get different answers", unknown.Body.String(), malformed.Body.String())
	}
	if got := tokenError(t, unknown); got != "invalid_grant" {
		t.Errorf("unknown device code = %q, want invalid_grant", got)
	}
}

// Polling every interval is what a CLI does, so polls get a budget of their
// own: spending the OIDC routes' budget on code exchanges does not stop a
// poll, and polls beyond theirs are refused.
func TestCLIDevicePollHasItsOwnBudget(t *testing.T) {
	fx := newDeviceFixture(t)
	d := startDevice(t, fx.srv)
	for range oidcRateLimitPerMinute + 1 {
		exchangeCLICode(fx.srv, "x", "y", testCLIRedirect)
	}
	if got := tokenError(t, pollDevice(fx.srv, d.DeviceCode)); got != "authorization_pending" {
		t.Fatalf("poll after the exchange budget is spent = %q, want authorization_pending", got)
	}
	var last int
	for range devicePollRateLimitPerMinute + 1 {
		last = pollDevice(fx.srv, d.DeviceCode).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("poll %d in a minute = %d, want 429", devicePollRateLimitPerMinute+1, last)
	}
}

func TestCLIDeviceStartIsCapped(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	devices := &fakeDeviceStore{byID: map[string]*fakeDevice{}}
	for i := range cliDeviceMaxLive {
		devices.byID[string(rune(i))] = &fakeDevice{status: "pending", expires: time.Now().Add(time.Minute)}
	}
	srv := deviceServer(t, f, cfg, newFakeOIDCStore(), &fakeAuthAudit{}, devices, nil)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, cliDevicePath, http.NoBody)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("start over the cap = %d %s, want 503", rec.Code, rec.Body.String())
	}
}

func TestCLIDeviceLogsNoCodeOrToken(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	users := newFakeOIDCStore()
	users.seed(f.issuer, "subject-123", &auth.User{ID: "u-1", TenantID: "default", Email: "alice@corp.example"}, true)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fx := deviceFixture{srv: deviceServer(t, f, cfg, users, &fakeAuthAudit{}, newFakeDeviceStore(), logger), idp: f, cfg: cfg}
	d := startDevice(t, fx.srv)
	submitUserCode(fx.srv, "BBBB-BBBB", "same-origin")
	cookie, csrf := deviceConsentOf(t, fx.signInForDevice(t, d.UserCode, nil))
	postDeviceConsent(fx.srv, cookie, csrf, consentApprove, "yes", "same-origin")
	token := accessToken(t, pollDevice(fx.srv, d.DeviceCode))
	pollDevice(fx.srv, d.DeviceCode)
	for name, secret := range map[string]string{"device code": d.DeviceCode, "user code": d.UserCode, "token": token, "consent": cookie.Value} {
		if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), strings.ReplaceAll(secret, "-", "")) {
			t.Errorf("the %s is in the server log", name)
		}
	}
}

func TestAuthMethodsAdvertiseTheDeviceSignIn(t *testing.T) {
	read := func(srv http.Handler) authMethods {
		t.Helper()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, authMethodsPath, http.NoBody)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		var m authMethods
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	fx := newDeviceFixture(t)
	if got := read(fx.srv); got != (authMethods{SSO: true, Device: true}) {
		t.Errorf("SSO with the device store = %+v, want sso and device", got)
	}
	f := newFakeIDP(t)
	if got := read(oidcServer(t, f, baseOIDCConfig(f), newFakeOIDCStore(), &fakeAuthAudit{}, nil)); got.Device {
		t.Errorf("SSO without the device store = %+v, want no device", got)
	}
}

func TestCLIDeviceRoutesNeedSSOAndTheStore(t *testing.T) {
	f := newFakeIDP(t)
	srv := oidcServer(t, f, baseOIDCConfig(f), newFakeOIDCStore(), &fakeAuthAudit{}, nil)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, cliDevicePath, http.NoBody)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("start without the store = %d, want 404", rec.Code)
	}
	if got := tokenError(t, pollDevice(srv, "some-device-code-that-is-long-enough-0123456789")); got != "unsupported_grant_type" {
		t.Errorf("device poll without the store = %q, want unsupported_grant_type", got)
	}
}

func TestUserCodeNormalization(t *testing.T) {
	for in, want := range map[string]string{
		"BCDF-GHJK":      "BCDFGHJK",
		"bcdf ghjk":      "BCDFGHJK",
		" bcdfghjk ":     "BCDFGHJK",
		"BCDF-GHJA":      "", // a vowel is never in a code
		"BCDF-GHJ":       "",
		"BCDF-GHJKL":     "",
		"BCDF\u00a0GHJK": "",
	} {
		if got := normalizeUserCode(in); got != want {
			t.Errorf("normalizeUserCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUserCodeIsUniform(t *testing.T) {
	// 25000 codes are 200000 letters, 10000 of each when uniform, with a
	// standard deviation of 100. A modulo bias would put four letters near
	// 9375, outside the 4.5% tolerance; a uniform draw stays within it.
	counts := map[rune]int{}
	for range 25000 {
		c, err := newUserCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != userCodeLen {
			t.Fatalf("code %q is not %d characters", c, userCodeLen)
		}
		for _, r := range c {
			counts[r]++
		}
	}
	if len(counts) != len(userCodeAlphabet) {
		t.Fatalf("codes used %d letters, want all %d", len(counts), len(userCodeAlphabet))
	}
	for r, n := range counts {
		if n < 9550 || n > 10450 {
			t.Errorf("letter %c appears %d times, want about 10000", r, n)
		}
	}
}

// Starts have a small budget per address of their own, counted by /64 for
// IPv6, so one address cannot fill the cluster's cap.
func TestCLIDeviceStartHasItsOwnBudget(t *testing.T) {
	fx := newDeviceFixture(t)
	for i := range deviceStartRateLimitPerMinute {
		startDeviceFrom(t, fx.srv, fmt.Sprintf("2001:db8:1:2::%x", i+1))
	}
	if rec := startFrom(fx.srv, "2001:db8:1:2:ffff::1"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("start %d from one /64 = %d, want 429", deviceStartRateLimitPerMinute+1, rec.Code)
	}
	startDeviceFrom(t, fx.srv, "2001:db8:1:3::1")
	if rec := exchangeCLICode(fx.srv, "x", "y", testCLIRedirect); rec.Code == http.StatusTooManyRequests {
		t.Error("starts spent the OIDC routes' budget")
	}
}

// The store caps live sign-ins per requester, keyed by a hash of the /64.
func TestCLIDeviceStartIsCappedPerRequester(t *testing.T) {
	fx := newDeviceFixture(t)
	startDeviceFrom(t, fx.srv, "2001:db8:1:2::1")
	first := fx.devices.lastStart
	if first.MaxLivePerRequester != cliDeviceMaxLivePerRequester || first.RequesterAddr != "2001:db8:1:2::1" || len(first.RequesterHash) != 64 {
		t.Fatalf("start = %+v, want the requester's address, its hash and the per-requester cap", first)
	}
	startDeviceFrom(t, fx.srv, "2001:db8:1:2::2")
	if fx.devices.lastStart.RequesterHash != first.RequesterHash {
		t.Error("two addresses of one /64 got different requester hashes")
	}
	startDeviceFrom(t, fx.srv, "2001:db8:1:3::1")
	if fx.devices.lastStart.RequesterHash == first.RequesterHash {
		t.Error("two /64s got the same requester hash")
	}
	if strings.Contains(first.RequesterHash, "2001") {
		t.Errorf("the requester hash %q holds the address", first.RequesterHash)
	}
	fx.devices.mu.Lock()
	for i := range cliDeviceMaxLivePerRequester {
		fx.devices.byID[fmt.Sprint("x", i)] = &fakeDevice{requesterHash: first.RequesterHash, status: "pending", expires: time.Now().Add(time.Minute)}
	}
	fx.devices.mu.Unlock()
	if rec := startFrom(fx.srv, "2001:db8:1:2::3"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("start over the per-requester cap = %d %s, want 429", rec.Code, rec.Body.String())
	}
}

// Only failed verifies spend the per-address budget.
func TestCLIDeviceVerifySuccessesSpendNoBudget(t *testing.T) {
	fx := newDeviceFixture(t)
	d := startDevice(t, fx.srv)
	for range deviceVerifyRateLimitPerMinute + 2 {
		if rec := submitUserCode(fx.srv, d.UserCode, "same-origin"); rec.Code != http.StatusSeeOther {
			t.Fatalf("verify of a live code = %d, want 303", rec.Code)
		}
	}
	if rec := submitUserCode(fx.srv, "BBBB-BBBB", "same-origin"); rec.Code != http.StatusBadRequest {
		t.Fatalf("a wrong code after successes = %d, want 400 (not rate limited)", rec.Code)
	}
}

// Failed verifies are counted by /64, and across all addresses the process
// allows deviceVerifyFailuresPerMinute, so many addresses cannot guess fast.
func TestCLIDeviceFailedVerifiesHaveAProcessWideBudget(t *testing.T) {
	fx := newDeviceFixture(t)
	for i := range deviceVerifyRateLimitPerMinute {
		submitUserCodeFrom(fx.srv, "BBBB-BBBB", "same-origin", fmt.Sprintf("2001:db8:9:9::%x", i+1))
	}
	if rec := submitUserCodeFrom(fx.srv, "BBBB-BBBB", "same-origin", "2001:db8:9:9:ffff::1"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a failure past the budget of one /64 = %d, want 429", rec.Code)
	}
	for i := range deviceVerifyFailuresPerMinute {
		submitUserCodeFrom(fx.srv, "BBBB-BBBB", "same-origin", fmt.Sprintf("10.%d.%d.1", i/250, i%250))
	}
	rec := submitUserCodeFrom(fx.srv, "BBBB-BBBB", "same-origin", "203.0.113.200")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a failure from a fresh address past the process budget = %d, want 429", rec.Code)
	}
}

// RFC 8628 section 5.4: the consent page says where and when the sign-in
// started, next to where this browser is.
func TestCLIDeviceConsentShowsWhereTheSignInStarted(t *testing.T) {
	fx := newDeviceFixture(t)
	d := startDeviceFrom(t, fx.srv, "198.51.100.7")
	body := fx.signInForDevice(t, d.UserCode, nil).Body.String()
	for _, want := range []string{"less than a minute ago from <strong>198.51.100.7</strong>", "This browser is at <strong>192.0.2.1</strong>"} {
		if !strings.Contains(body, want) {
			t.Errorf("the consent page does not say %q:\n%s", want, body)
		}
	}
}

func TestDeviceAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second: "less than a minute ago",
		70 * time.Second: "1 minute ago",
		9 * time.Minute:  "9 minutes ago",
	} {
		if got := deviceAge(d); got != want {
			t.Errorf("deviceAge(%v) = %q, want %q", d, got, want)
		}
	}
}

// A poll with an unknown device code is tied to no sign-in: logged, not
// audited.
func TestCLIDeviceUnknownPollsAreNotAudited(t *testing.T) {
	fx := newDeviceFixture(t)
	pollDevice(fx.srv, "an-unknown-device-code-of-a-plausible-length-012")
	pollDevice(fx.srv, "")
	if len(fx.audit.events) != 0 {
		t.Errorf("audit = %+v, want none for unknown device codes", fx.audit.events)
	}
}
