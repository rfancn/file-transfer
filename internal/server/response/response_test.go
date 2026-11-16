package response

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/protocol"
)

func TestErrBusy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	ErrBusy(c, 10*time.Second)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d，期望 429", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "10" {
		t.Fatalf("Retry-After = %q，期望 10", got)
	}
	var body protocol.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "server_busy" {
		t.Fatalf("error = %q，期望 server_busy", body.Error)
	}
}
