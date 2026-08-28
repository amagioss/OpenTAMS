package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/auth"
	"github.com/amagioss/opentams/internal/config"
	"github.com/amagioss/opentams/internal/httpx/health"
	"github.com/amagioss/opentams/internal/server"
)

func init() { gin.SetMode(gin.TestMode) }

// ---------- fakes ---------------------------------------------------------

// fakeSSI embeds api.StrictServerInterface (nil), so any method we don't
// explicitly override panics with a nil-pointer dereference. Function fields
// let us swap behaviour per test without writing 69 stubs.
type fakeSSI struct {
	api.StrictServerInterface

	getFlows         func(context.Context, api.GetFlowsRequestObject) (api.GetFlowsResponseObject, error)
	getFlow          func(context.Context, api.GetFlowRequestObject) (api.GetFlowResponseObject, error)
	getHealthDetails func(context.Context, api.GetHealthDetailsRequestObject) (api.GetHealthDetailsResponseObject, error)
}

func (f *fakeSSI) GetFlows(ctx context.Context, req api.GetFlowsRequestObject) (api.GetFlowsResponseObject, error) {
	return f.getFlows(ctx, req)
}
func (f *fakeSSI) GetFlow(ctx context.Context, req api.GetFlowRequestObject) (api.GetFlowResponseObject, error) {
	return f.getFlow(ctx, req)
}
func (f *fakeSSI) GetHealthDetails(ctx context.Context, req api.GetHealthDetailsRequestObject) (api.GetHealthDetailsResponseObject, error) {
	return f.getHealthDetails(ctx, req)
}

type fakeAuth struct {
	validate func(ctx context.Context, token string) (*auth.Principal, error)
}

func (f *fakeAuth) Authenticate(ctx context.Context, token string) (*auth.Principal, error) {
	return f.validate(ctx, token)
}

type mockChecker struct {
	ready   func(ctx context.Context) bool
	details func(ctx context.Context) health.Details
}

func (m *mockChecker) Ready(ctx context.Context) bool {
	if m.ready != nil {
		return m.ready(ctx)
	}
	return true
}

func (m *mockChecker) Details(ctx context.Context) health.Details {
	if m.details != nil {
		return m.details(ctx)
	}
	return health.Details{Status: health.StatusHealthy, Components: map[string]health.ComponentStatus{}}
}

// ---------- helpers -------------------------------------------------------

func baseCfg() *config.Config {
	return &config.Config{
		ServerPort:                   0,
		ServerGracefulShutdownPeriod: 5 * time.Second,
		AppEnv:                       "development",
	}
}

func allowAllAuth() *fakeAuth {
	return &fakeAuth{
		validate: func(context.Context, string) (*auth.Principal, error) {
			return &auth.Principal{Subject: "tester", IssuerType: "test"}, nil
		},
	}
}

func emptyFlowsSSI() *fakeSSI {
	return &fakeSSI{
		getFlows: func(context.Context, api.GetFlowsRequestObject) (api.GetFlowsResponseObject, error) {
			return api.GetFlows200JSONResponse{Body: []api.Flow{}}, nil
		},
	}
}

func baseDeps(t *testing.T) server.Deps {
	t.Helper()
	reg := prometheus.NewRegistry()
	return server.Deps{
		Config:                baseCfg(),
		Logger:                zap.NewNop(),
		Handlers:              emptyFlowsSSI(),
		Checker:               &mockChecker{},
		AuthProvider:          allowAllAuth(),
		Gatherer:              reg,
		HTTPMetricsRegisterer: reg,
	}
}

func mustNew(t *testing.T, deps server.Deps) *server.Server {
	t.Helper()
	s, err := server.New(deps)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return s
}

func serve(t *testing.T, s *server.Server, method, path string, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, http.NoBody)
	for k, vv := range headers {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// ---------- TC-SRV-01 / 02 : dep validation -------------------------------

func TestNew_NilHandlersReturnsSentinel(t *testing.T) {
	deps := baseDeps(t)
	deps.Handlers = nil
	s, err := server.New(deps)
	if !errors.Is(err, server.ErrMissingHandlers) {
		t.Fatalf("want ErrMissingHandlers, got %v", err)
	}
	if s != nil {
		t.Fatal("server should be nil when New errors")
	}
}

func TestNew_EachRequiredDepValidated(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(d *server.Deps)
		want   error
	}{
		{"Config nil", func(d *server.Deps) { d.Config = nil }, server.ErrMissingConfig},
		{"Logger nil", func(d *server.Deps) { d.Logger = nil }, server.ErrMissingLogger},
		{"Handlers nil", func(d *server.Deps) { d.Handlers = nil }, server.ErrMissingHandlers},
		{"Checker nil", func(d *server.Deps) { d.Checker = nil }, server.ErrMissingChecker},
		{"AuthProvider nil", func(d *server.Deps) { d.AuthProvider = nil }, server.ErrMissingAuth},
		{"Gatherer nil", func(d *server.Deps) { d.Gatherer = nil }, server.ErrMissingGatherer},
		{"HTTPMetricsRegisterer nil", func(d *server.Deps) { d.HTTPMetricsRegisterer = nil }, server.ErrMissingRegisterer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := baseDeps(t)
			tc.mutate(&d)
			_, err := server.New(d)
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

// ---------- TC-SRV-03 : TLS config validation -----------------------------

func TestNew_TLSCertWithoutKeyReturnsInvalidTLSConfig(t *testing.T) {
	d := baseDeps(t)
	d.Config.ServerTLSCertFile = "/tmp/cert.pem"
	d.Config.ServerTLSKeyFile = ""
	_, err := server.New(d)
	if !errors.Is(err, server.ErrInvalidTLSConfig) {
		t.Fatalf("want ErrInvalidTLSConfig, got %v", err)
	}
}

func TestNew_TLSKeyWithoutCertReturnsInvalidTLSConfig(t *testing.T) {
	d := baseDeps(t)
	d.Config.ServerTLSCertFile = ""
	d.Config.ServerTLSKeyFile = "/tmp/key.pem"
	_, err := server.New(d)
	if !errors.Is(err, server.ErrInvalidTLSConfig) {
		t.Fatalf("want ErrInvalidTLSConfig, got %v", err)
	}
}

// ---------- TC-SRV-04 / 05 / 06 : public routes (no auth) -----------------

func TestGetHealthz_PublicNoAuth(t *testing.T) {
	s := mustNew(t, baseDeps(t))
	rec := serve(t, s, http.MethodGet, "/healthz", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("healthz status=%d, want 200", rec.Code)
	}
}

func TestGetReadyz_PublicNoAuth(t *testing.T) {
	d := baseDeps(t)
	d.Checker = &mockChecker{ready: func(context.Context) bool { return true }}
	s := mustNew(t, d)
	rec := serve(t, s, http.MethodGet, "/readyz", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("readyz status=%d, want 200", rec.Code)
	}
}

func TestGetMetrics_PublicReturnsPrometheusBody(t *testing.T) {
	reg := prometheus.NewRegistry()
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "opentams_test_widget_total", Help: "test"})
	reg.MustRegister(counter)
	counter.Inc()

	d := baseDeps(t)
	d.Gatherer = reg
	d.HTTPMetricsRegisterer = reg
	s := mustNew(t, d)

	rec := serve(t, s, http.MethodGet, "/metrics", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status=%d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "opentams_test_widget_total") {
		t.Errorf("/metrics body missing registered counter; got:\n%s", rec.Body.String())
	}
}

// ---------- TC-SRV-17 : HTTP histogram uses TAMS-tuned buckets ------------
//
// Guards against a silent revert of the httpmetrics.WithBuckets option in
// server.New. Reverting to prometheus.DefBuckets would re-cap the histogram
// at 10s and lose tail visibility for slow requests up to WriteTimeout (60s),
// while also coarsening the sub-5ms region where idempotency-cache replays
// and segment GETs live. The TAMS profile spans 1ms..30s in 13 boundaries.
func TestNew_HTTPMetricsHistogramUsesTAMSBuckets(t *testing.T) {
	reg := prometheus.NewRegistry()
	d := baseDeps(t)
	d.Gatherer = reg
	d.HTTPMetricsRegisterer = reg
	s := mustNew(t, d)

	h := http.Header{}
	h.Set("Authorization", "Bearer any-token")
	rec := serve(t, s, http.MethodGet, "/tams/v1/flows", h)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup: GET /tams/v1/flows status=%d, body=%s", rec.Code, rec.Body.String())
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var got []float64
	found := false
	for _, fam := range families {
		if fam.GetName() != "http_request_duration_seconds" {
			continue
		}
		found = true
		if len(fam.GetMetric()) == 0 {
			t.Fatal("histogram has no metrics after one observation")
		}
		for _, b := range fam.GetMetric()[0].GetHistogram().GetBucket() {
			got = append(got, b.GetUpperBound())
		}
		break
	}
	if !found {
		t.Fatal("metric family http_request_duration_seconds not found in registry")
	}

	want := []float64{
		0.001, 0.005, 0.010, 0.025, 0.050, 0.100, 0.250, 0.500,
		1.0, 2.5, 5.0, 10.0, 30.0,
	}
	if len(got) != len(want) {
		t.Fatalf("bucket count: got %d %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bucket[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
}

// ---------- TC-SRV-18 / 19 : OapiRequestValidator middleware -------------
//
// These tests prove that BR-SRV-14 (spec-driven request validation) is wired
// into the authenticated group AFTER the auth middleware and BEFORE the
// strict handler stack. Both rely on the fact that the baseDeps fakeSSI
// embeds api.StrictServerInterface(nil) — meaning any call to a method that
// hasn't been explicitly stubbed (PostFlowSegments is not stubbed) will
// nil-pointer-panic and surface as a 500. So a 400 outcome here proves the
// validator rejected the request BEFORE the strict handler was ever
// invoked. A 500 would mean the validator was silently bypassed.

// TC-SRV-18: POST /tams/v1/flows/{id}/segments with an empty JSON object
// body MUST be rejected at the validator with a 400 ProblemDetails. The
// flow-segment-post.json schema marks `object_id` and `timerange` as
// required; without OapiRequestValidator wired into the chain, this request
// would slip past unmarshalling (empty struct fields) and into the handler
// where missing-field detection is hand-rolled and easy to forget.
func TestPostFlowSegments_EmptyBody_RejectedByValidator(t *testing.T) {
	d := baseDeps(t)
	s := mustNew(t, d)

	req := httptest.NewRequest(http.MethodPost,
		"/tams/v1/flows/11111111-1111-4111-8111-111111111111/segments",
		strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer any-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Idempotency-Key", "11111111-1111-4111-8111-111111111111")

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, body=%s — want 400 (validator rejection)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type=%q, want application/problem+json", ct)
	}
	var pd apperror.ProblemDetails
	if err := json.Unmarshal(rec.Body.Bytes(), &pd); err != nil {
		t.Fatalf("body not ProblemDetails: %v\nbody: %s", err, rec.Body.String())
	}
	if pd.Status != 400 {
		t.Errorf("pd.Status=%d, want 400", pd.Status)
	}
	if pd.Detail == "" {
		t.Error("pd.Detail must describe the validator failure")
	}
	if pd.RequestID == "" {
		t.Error("pd.RequestID must be populated by the request-id middleware")
	}
}

// TC-SRV-19: POST /tams/v1/flows/{id}/segments with a structurally-valid
// body but missing the required `X-Idempotency-Key` request header MUST be
// rejected with a 400 ProblemDetails. Honesty call: this rejection is
// already enforced by oapi-codegen's generated ServerInterfaceWrapper
// (siw.ErrorHandler → paramParseErrorHandler), so the test passes even if
// OapiRequestValidator is disabled. We keep it because: (a) it's a useful
// regression guard against the strict-server's required-header check ever
// regressing — and once the validator is wired, it's defence-in-depth at
// both layers; (b) verified by counterfactual disable that TC-SRV-18 above
// is genuinely validator-only (status 500 from handler panic when validator
// off, 400 when on), so the wiring is exercised. Pattern: TC-SRV-18 proves
// the validator runs; TC-SRV-19 proves the spec contract holds end-to-end.
func TestPostFlowSegments_MissingIdempotencyHeader_RejectedByValidator(t *testing.T) {
	d := baseDeps(t)
	s := mustNew(t, d)

	body := `{"object_id":"obj-a","timerange":"[0:0_5:0)"}`
	req := httptest.NewRequest(http.MethodPost,
		"/tams/v1/flows/11111111-1111-4111-8111-111111111111/segments",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer any-token")
	req.Header.Set("Content-Type", "application/json")
	// Deliberately omit X-Idempotency-Key.

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, body=%s — want 400 (validator rejection)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type=%q, want application/problem+json", ct)
	}
	var pd apperror.ProblemDetails
	if err := json.Unmarshal(rec.Body.Bytes(), &pd); err != nil {
		t.Fatalf("body not ProblemDetails: %v\nbody: %s", err, rec.Body.String())
	}
	if pd.Status != 400 {
		t.Errorf("pd.Status=%d, want 400", pd.Status)
	}
	// kin-openapi error messages aren't a stable contract, so we just
	// require that *some* detail is surfaced. Stronger assertions on the
	// exact message would couple the test to library internals.
	if pd.Detail == "" {
		t.Error("pd.Detail must describe the validator failure")
	}
}

// ---------- TC-SRV-07 / 08 : authenticated endpoints reject no-bearer -----

func TestGetHealthDetails_RequiresAuth(t *testing.T) {
	s := mustNew(t, baseDeps(t))
	rec := serve(t, s, http.MethodGet, "/health/details", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("health/details no-auth status=%d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type=%q, want application/problem+json", ct)
	}
}

func TestGetFlows_RequiresAuth(t *testing.T) {
	s := mustNew(t, baseDeps(t))
	rec := serve(t, s, http.MethodGet, "/tams/v1/flows", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("flows no-auth status=%d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type=%q, want application/problem+json", ct)
	}
	var pd apperror.ProblemDetails
	if err := json.Unmarshal(rec.Body.Bytes(), &pd); err != nil {
		t.Fatalf("body not ProblemDetails JSON: %v\nbody: %s", err, rec.Body.String())
	}
	if pd.Status != 401 {
		t.Errorf("pd.Status=%d, want 401", pd.Status)
	}
	if pd.RequestID == "" {
		t.Error("pd.RequestID should be populated by middleware chain")
	}
}

// ---------- TC-SRV-09 : authenticated happy path --------------------------

func TestGetFlows_WithBearerSucceeds(t *testing.T) {
	d := baseDeps(t)
	s := mustNew(t, d)
	h := http.Header{}
	h.Set("Authorization", "Bearer any-token")
	rec := serve(t, s, http.MethodGet, "/tams/v1/flows", h)
	if rec.Code != http.StatusOK {
		t.Fatalf("flows with auth status=%d, body=%s", rec.Code, rec.Body.String())
	}
	var flows []api.Flow
	if err := json.Unmarshal(rec.Body.Bytes(), &flows); err != nil {
		t.Fatalf("body not a Flow list: %v\nbody: %s", err, rec.Body.String())
	}
	if len(flows) != 0 {
		t.Errorf("want empty list, got %d flows", len(flows))
	}
}

// ---------- TC-SRV-10 : param parse error → ProblemDetails ----------------
//
// Note: the OpenAPI spec declares Uuid as a bare `type: string` alias, so
// oapi-codegen's generated binder accepts any string as a FlowId path
// parameter — UUID validity is enforced downstream by the handler (M12),
// not by the param parser. To exercise paramParseErrorHandler we hit a
// query parameter whose codegen type forces a parse (`limit` is int).
func TestParamParseError_ReturnsProblemDetails(t *testing.T) {
	s := mustNew(t, baseDeps(t))
	h := http.Header{}
	h.Set("Authorization", "Bearer any-token")
	rec := serve(t, s, http.MethodGet, "/tams/v1/flows?limit=notanumber", h)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad-limit status=%d, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type=%q, want application/problem+json (not gin.H msg body)", ct)
	}
	var pd apperror.ProblemDetails
	if err := json.Unmarshal(rec.Body.Bytes(), &pd); err != nil {
		t.Fatalf("body not ProblemDetails: %v\nbody: %s", err, rec.Body.String())
	}
	if pd.Status != 400 {
		t.Errorf("pd.Status=%d, want 400", pd.Status)
	}
	if pd.Title == "" {
		t.Error("pd.Title should be populated")
	}
	if pd.Detail == "" {
		t.Error("pd.Detail should describe the failed parameter")
	}
	if pd.RequestID == "" {
		t.Error("pd.RequestID should be populated by the request-id middleware")
	}
}

// ---------- TC-SRV-11 : handler apperror → ProblemDetails -----------------

func TestGetFlow_HandlerNotFound_Returns404ProblemDetails(t *testing.T) {
	d := baseDeps(t)
	d.Handlers = &fakeSSI{
		getFlow: func(context.Context, api.GetFlowRequestObject) (api.GetFlowResponseObject, error) {
			return nil, apperror.New(apperror.ErrNotFound, "flow does not exist")
		},
	}
	s := mustNew(t, d)
	h := http.Header{}
	h.Set("Authorization", "Bearer any-token")
	rec := serve(t, s, http.MethodGet, "/tams/v1/flows/11111111-1111-4111-8111-111111111111", h)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type=%q, want application/problem+json", ct)
	}
	var pd apperror.ProblemDetails
	if err := json.Unmarshal(rec.Body.Bytes(), &pd); err != nil {
		t.Fatalf("body not PD: %v\nbody: %s", err, rec.Body.String())
	}
	if pd.Title != "Not Found" {
		t.Errorf("pd.Title=%q, want Not Found", pd.Title)
	}
	if pd.Status != 404 {
		t.Errorf("pd.Status=%d, want 404", pd.Status)
	}
}

// ---------- TC-SRV-12 : panic in handler → 500 ProblemDetails -------------

func TestHandlerPanic_YieldsProblemDetails500(t *testing.T) {
	d := baseDeps(t)
	d.Handlers = &fakeSSI{
		getFlow: func(context.Context, api.GetFlowRequestObject) (api.GetFlowResponseObject, error) {
			panic("boom")
		},
	}
	s := mustNew(t, d)
	h := http.Header{}
	h.Set("Authorization", "Bearer any-token")
	rec := serve(t, s, http.MethodGet, "/tams/v1/flows/11111111-1111-4111-8111-111111111111", h)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type=%q, want application/problem+json", ct)
	}
	var pd apperror.ProblemDetails
	if err := json.Unmarshal(rec.Body.Bytes(), &pd); err != nil {
		t.Fatalf("body not PD: %v\nbody: %s", err, rec.Body.String())
	}
	if pd.Status != 500 {
		t.Errorf("pd.Status=%d, want 500", pd.Status)
	}
}

// ---------- TC-SRV-13 : X-Request-ID propagation --------------------------

func TestXRequestID_SetOnEveryResponse(t *testing.T) {
	s := mustNew(t, baseDeps(t))
	rec := serve(t, s, http.MethodGet, "/healthz", nil)
	if got := rec.Header().Get("X-Request-ID"); got == "" {
		t.Errorf("X-Request-ID header missing on /healthz response")
	}
}

// ---------- TC-SRV-14 : Start returns when ctx cancelled ------------------

func TestStart_ReturnsWhenContextCancelled(t *testing.T) {
	d := baseDeps(t)
	s := mustNew(t, d)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	waitForListen(t, s)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return within 2s after ctx cancel")
	}

	sctx, sc := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer sc()
	if err := s.Shutdown(sctx); err != nil {
		t.Errorf("Shutdown after ctx cancel returned %v, want nil", err)
	}
}

// ---------- TC-SRV-15 : bind error surfaces --------------------------------

func TestStart_BindErrorSurfacesNonServerClosed(t *testing.T) {
	// Pre-bind on the wildcard (matches what Start does: ":N"). Using
	// "127.0.0.1:0" instead would leave ":N" free on some OSes.
	lis, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("pre-bind: %v", err)
	}
	defer lis.Close() //nolint:errcheck // test fixture; close error is not actionable.
	port := lis.Addr().(*net.TCPAddr).Port

	d := baseDeps(t)
	d.Config.ServerPort = port
	s := mustNew(t, d)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err = s.Start(ctx)
	if err == nil {
		t.Fatal("Start on already-bound port should fail, got nil")
	}
	if errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("bind error misclassified as ErrServerClosed: %v", err)
	}
}

// ---------- TC-SRV-16 : Handler() is usable via httptest ------------------

func TestHandler_UsableViaHTTPTest(t *testing.T) {
	s := mustNew(t, baseDeps(t))
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("httptest GET: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // test asserts on resp.StatusCode; body close error is not actionable.
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("httptest /healthz status=%d, want 200", resp.StatusCode)
	}
}

// ---------- NFR-SRV-T3 : TLS branch exercised via injection seam ----------

func TestStart_TLSBranchInvoked(t *testing.T) {
	var invoked atomic.Bool
	var gotCert, gotKey string

	orig := server.ServeTLSFn
	server.ServeTLSFn = func(srv *http.Server, lis net.Listener, cert, key string) error {
		invoked.Store(true)
		gotCert, gotKey = cert, key
		return http.ErrServerClosed
	}
	t.Cleanup(func() { server.ServeTLSFn = orig })

	d := baseDeps(t)
	d.Config.ServerTLSCertFile = "/tmp/opentams-test-cert.pem"
	d.Config.ServerTLSKeyFile = "/tmp/opentams-test-key.pem"
	s := mustNew(t, d)

	err := s.Start(context.Background())
	if err != nil {
		t.Fatalf("Start TLS branch returned %v, want nil (ErrServerClosed swallowed)", err)
	}
	if !invoked.Load() {
		t.Fatal("ServeTLSFn seam was not invoked")
	}
	if gotCert != d.Config.ServerTLSCertFile || gotKey != d.Config.ServerTLSKeyFile {
		t.Errorf("seam received cert=%q key=%q, want %q / %q",
			gotCert, gotKey, d.Config.ServerTLSCertFile, d.Config.ServerTLSKeyFile)
	}
}

// ---------- helpers -------------------------------------------------------

// waitForListen polls Addr() until a real bound address appears (":0" → ":N").
// Used by lifecycle tests to synchronise with net.Listen in Start without a
// fixed sleep.
func waitForListen(t *testing.T, s *server.Server) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a := s.Addr()
		if a != "" && !strings.HasSuffix(a, ":0") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not start listening within 2s")
}

// compile-time guard: ensure we never silently break the fake's interface.
var _ api.StrictServerInterface = (*fakeSSI)(nil)

// sanity compile-check for the ErrInvalidTLSConfig formatting — keeps the
// linter happy by referencing the fmt package in a test-only context.
var _ = fmt.Sprintf
