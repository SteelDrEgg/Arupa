package main

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// logHTTPRequests emits exactly one structured access record after every HTTP
// request. It is the outermost application handler so it covers kernel,
// plugin, static, and Socket.IO HTTP traffic alike.
func logHTTPRequests(logger *slog.Logger, next http.Handler) http.Handler {
	log := logger.With("component", "kernel", "from", "http")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)

		args := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"bytes_written", recorder.bytes,
		}
		switch {
		case recorder.Status() >= http.StatusInternalServerError:
			log.Error("request completed", args...)
		case recorder.Status() >= http.StatusBadRequest:
			log.Warn("request completed", args...)
		default:
			log.Info("request completed", args...)
		}
	})
}

type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *responseRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseRecorder) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

func (w *responseRecorder) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *responseRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseRecorder) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

func (w *responseRecorder) Push(target string, opts *http.PushOptions) error {
	pusher, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

func (w *responseRecorder) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if readerFrom, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err := readerFrom.ReadFrom(r)
		w.bytes += n
		return n, err
	}
	return io.Copy(responseWriterOnly{w}, r)
}

type responseWriterOnly struct{ w *responseRecorder }

func (w responseWriterOnly) Write(p []byte) (int, error) { return w.w.Write(p) }
