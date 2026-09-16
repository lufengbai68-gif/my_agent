package api

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// testRespWriter 包装 httptest.ResponseRecorder, 补齐 gin.ResponseWriter 要求的额外方法。
// 测试专用: 仅透传到下游, 不真的实现 hijack。
type testRespWriter struct {
	*httptest.ResponseRecorder
}

func (w *testRespWriter) CloseNotify() <-chan bool { return make(chan bool) }
func (w *testRespWriter) Flush()                   {}
func (w *testRespWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("hijack not supported in test")
}
func (w *testRespWriter) Pusher() http.Pusher { return nil }
func (w *testRespWriter) Size() int { return w.ResponseRecorder.Body.Len() }
func (w *testRespWriter) Status() int { return w.ResponseRecorder.Code }
func (w *testRespWriter) Written() bool { return w.ResponseRecorder.Code != 0 }
func (w *testRespWriter) WriteHeaderNow() {}

func newTestRespWriter() *testRespWriter { return &testRespWriter{httptest.NewRecorder()} }

func TestBodyLogWriter_TruncatesAtMax(t *testing.T) {
	rec := newTestRespWriter()
	blw := &bodyLogWriter{ResponseWriter: rec, max: 10}

	n, err := blw.Write([]byte("0123456789abcdefghijklmno"))
	if err != nil {
		t.Fatalf("write err: %v", err)
	}
	if n != 25 {
		t.Errorf("expected n=25, got %d", n)
	}
	if got := blw.buf.String(); got != "0123456789" {
		t.Errorf("buf = %q, want %q", got, "0123456789")
	}
	if !blw.truncated {
		t.Error("truncated should be true")
	}
	if got := rec.Body.String(); got != "0123456789abcdefghijklmno" {
		t.Errorf("underlying writer missed data: %q", got)
	}
}

func TestBodyLogWriter_NotTruncatedUnderMax(t *testing.T) {
	rec := newTestRespWriter()
	blw := &bodyLogWriter{ResponseWriter: rec, max: 100}
	_, _ = blw.Write([]byte("short"))
	if blw.truncated {
		t.Error("truncated should be false")
	}
	if blw.buf.String() != "short" {
		t.Errorf("buf = %q", blw.buf.String())
	}
}

func TestBodyLogWriter_WriteStringUsesWrite(t *testing.T) {
	rec := newTestRespWriter()
	blw := &bodyLogWriter{ResponseWriter: rec, max: 100}
	_, _ = blw.WriteString("hello")
	if blw.buf.String() != "hello" {
		t.Errorf("WriteString did not populate buf: %q", blw.buf.String())
	}
}

func TestPeekJSONRequestBody_RejectsNonJSON(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader([]byte("file content")))
	c.Request.Header.Set("Content-Type", "multipart/form-data; boundary=xxx")

	got, trunc := peekJSONRequestBody(c)
	if got != "" || trunc {
		t.Errorf("expected skip for multipart, got (%q, %v)", got, trunc)
	}
	rest, _ := io.ReadAll(c.Request.Body)
	if string(rest) != "file content" {
		t.Errorf("body was consumed: %q", rest)
	}
}

func TestPeekJSONRequestBody_ReadsJSON(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader([]byte(`{"k":"v"}`)))
	c.Request.Header.Set("Content-Type", "application/json")

	got, trunc := peekJSONRequestBody(c)
	if got != `{"k":"v"}` {
		t.Errorf("expected body content, got %q", got)
	}
	if trunc {
		t.Error("truncated should be false")
	}
	rest, _ := io.ReadAll(c.Request.Body)
	if string(rest) != `{"k":"v"}` {
		t.Errorf("body not replayable: %q", rest)
	}
}

func TestPeekJSONRequestBody_TruncatesLarge(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	big := bytes.Repeat([]byte("x"), maxBodyLogSize+100)
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(big))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.ContentLength = int64(len(big))

	got, trunc := peekJSONRequestBody(c)
	if got != "<body too large to log>" {
		t.Errorf("expected skip marker, got %q", got)
	}
	if !trunc {
		t.Error("truncated should be true")
	}
}

func TestAccessLog_EmitsRequestAndResponseBody(t *testing.T) {
	r := gin.New()
	r.Use(AccessLog())
	r.POST("/x", func(c *gin.Context) {
		b, _ := io.ReadAll(c.Request.Body)
		if string(b) != `{"a":1}` {
			t.Errorf("downstream saw %q", b)
		}
		c.JSON(200, gin.H{"echo": string(b)})
	})

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/x", strings.NewReader(`{"a":1}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"echo":"{\"a\":1}"`) {
		t.Errorf("downstream got unexpected body: %q", rec.Body.String())
	}
}
