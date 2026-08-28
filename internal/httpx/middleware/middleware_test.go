package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/auth"
	"github.com/amagioss/opentams/internal/httpx/middleware"
)

func init() { gin.SetMode(gin.TestMode) }

type mockProvider struct {
	authenticateFn func(ctx context.Context, token string) (*auth.Principal, error)
}

func (m *mockProvider) Authenticate(ctx context.Context, token string) (*auth.Principal, error) {
	if m.authenticateFn != nil {
		return m.authenticateFn(ctx, token)
	}
	return nil, apperror.New(apperror.ErrUnauthorized, "unauthorized")
}

// --- Auth tests ---

func authRouter(provider auth.Provider) *gin.Engine {
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(middleware.Auth(provider))
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

// TC-MW-AUTH-01: absent Authorization header → 401.
func TestAuth_MissingHeader(t *testing.T) {
	w := httptest.NewRecorder()
	authRouter(&mockProvider{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// TC-MW-AUTH-02: non-Bearer scheme → 401.
func TestAuth_NonBearerScheme(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	authRouter(&mockProvider{}).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// TC-MW-AUTH-03: valid token → 200 and subject stored in context.
func TestAuth_ValidToken(t *testing.T) {
	var capturedSubject string
	provider := &mockProvider{
		authenticateFn: func(_ context.Context, _ string) (*auth.Principal, error) {
			return &auth.Principal{Subject: "user@example.com"}, nil
		},
	}
	r := gin.New()
	r.Use(middleware.Auth(provider))
	r.GET("/", func(c *gin.Context) {
		capturedSubject = middleware.AuthSubject(c)
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set("Authorization", "Bearer valid-token")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if capturedSubject != "user@example.com" {
		t.Errorf("expected subject user@example.com, got %q", capturedSubject)
	}
}

// TC-MW-AUTH-04: expired token → 401.
func TestAuth_ExpiredToken(t *testing.T) {
	provider := &mockProvider{
		authenticateFn: func(_ context.Context, _ string) (*auth.Principal, error) {
			return nil, apperror.New(apperror.ErrTokenExpired, "expired")
		},
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set("Authorization", "Bearer expired")
	authRouter(provider).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// TC-MW-AUTH-05: unauthorized token → 401.
func TestAuth_UnauthorizedToken(t *testing.T) {
	provider := &mockProvider{
		authenticateFn: func(_ context.Context, _ string) (*auth.Principal, error) {
			return nil, apperror.New(apperror.ErrUnauthorized, "bad token")
		},
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set("Authorization", "Bearer bad-token")
	authRouter(provider).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// TC-MW-AUTH-06: downstream handler does not run after auth failure.
func TestAuth_AbortsOnFailure(t *testing.T) {
	var handlerRan bool
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(middleware.Auth(&mockProvider{}))
	r.GET("/", func(c *gin.Context) {
		handlerRan = true
		c.Status(http.StatusOK)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if handlerRan {
		t.Error("handler must not run after auth failure")
	}
}

// --- ErrorHandler tests ---

func errRouter(handler gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.GET("/", handler)
	return r
}

// TC-MW-ERR-01: AppError → correct ProblemDetails status and content-type.
func TestErrorHandler_AppError(t *testing.T) {
	r := errRouter(func(c *gin.Context) {
		_ = c.Error(apperror.New(apperror.ErrNotFound, "flow not found"))
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("expected application/problem+json, got %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
}

// TC-MW-ERR-02: non-AppError → 500 with no detail.
func TestErrorHandler_NonAppError(t *testing.T) {
	r := errRouter(func(c *gin.Context) {
		_ = c.Error(errors.New("unexpected db failure"))
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if _, ok := body["detail"]; ok {
		t.Error("detail must be omitted for non-AppError to avoid leaking internals")
	}
}

// TC-MW-ERR-03: no errors → response not written by ErrorHandler.
func TestErrorHandler_NoErrors(t *testing.T) {
	r := errRouter(func(c *gin.Context) { c.Status(http.StatusNoContent) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
}

// TC-MW-ERR-04: handler already wrote response → ErrorHandler does not overwrite.
func TestErrorHandler_AlreadyWritten(t *testing.T) {
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		_ = c.Error(apperror.New(apperror.ErrNotFound, "ignored"))
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// --- PanicWriter tests ---

// TC-MW-PAN-01: PanicWriter writes 500 ProblemDetails with no type field.
func TestPanicWriter_Writes500(t *testing.T) {
	writer := middleware.PanicWriter()
	w := httptest.NewRecorder()
	writer(w, "boom", []byte("stack trace"))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("expected application/problem+json, got %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if _, ok := body["type"]; ok {
		t.Error("type field must be omitted for panic responses")
	}
}
