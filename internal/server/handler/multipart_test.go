package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/auth"
	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/limiter"
	"github.com/rfancn/file-transfer/internal/server/app"
)

func TestMultipartPartReturnsBusy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rt := config.Runtime{
		Config: config.DefaultServer(),
		Limits: config.Limits{
			Enabled:              true,
			MaxConcurrent:        1,
			MaxConcurrentPerUser: 1,
			QueueTimeout:         10 * time.Millisecond,
		},
	}
	lim := limiter.New(rt.Limits)
	defer lim.Close()

	release, err := lim.AcquireTransfer(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	token, err := auth.Sign(rt.Config.Auth.UploadSecret, auth.MultipartClaims{
		Sub:        "alice",
		Path:       "alice/outbound/a.bin",
		PartSize:   5 << 20,
		S3UploadID: "u1",
		Iat:        time.Now().Unix(),
		Exp:        time.Now().Add(time.Hour).Unix(),
		Kind:       "multipart",
	})
	if err != nil {
		t.Fatal(err)
	}

	h := &Handler{App: &app.App{Cfg: rt.Config, Limits: lim, RT: rt}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/v1/uploads/"+token+"/parts/1", strings.NewReader("x"))
	c.Set("username", "alice")
	c.Params = gin.Params{
		{Key: "upload_id", Value: token},
		{Key: "part", Value: "1"},
	}

	h.MultipartPart(c)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d，期望 429", w.Code)
	}
}
