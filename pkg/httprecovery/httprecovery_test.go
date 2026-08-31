package httprecovery_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/amagioss/opentams/pkg/httprecovery"
)

func TestResponseWriter_Called(t *testing.T) {
	var called bool
	writer := httprecovery.ResponseWriter(func(w http.ResponseWriter, recovered any, stack []byte) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	})
	w := httptest.NewRecorder()
	writer(w, "oops", []byte("stack"))
	if !called {
		t.Error("ResponseWriter must be callable")
	}
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}
