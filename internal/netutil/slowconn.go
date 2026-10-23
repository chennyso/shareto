package netutil

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"
)

type MonitoredWriter struct {
	http.ResponseWriter
	writtenBytes atomic.Int64
	done         atomic.Bool
}

func (mw *MonitoredWriter) Write(p []byte) (int, error) {
	n, err := mw.ResponseWriter.Write(p)
	if n > 0 {
		mw.writtenBytes.Add(int64(n))
	}
	return n, err
}

func (mw *MonitoredWriter) Unwrap() http.ResponseWriter {
	return mw.ResponseWriter
}

// MarkDone signals that the transmission completed normally so the monitor stops.
func (mw *MonitoredWriter) MarkDone() {
	mw.done.Store(true)
}

// WrapSlowConn wraps ResponseWriter with transfer rate monitoring over 15-second windows.
// If the write throughput falls below minKBps in a full 15s window, the connection is aborted.
// Returns the wrapped ResponseWriter and a cleanup function to be deferred.
func WrapSlowConn(w http.ResponseWriter, ctx context.Context, minKBps int) (http.ResponseWriter, func()) {
	if minKBps <= 0 {
		return w, func() {}
	}

	mw := &MonitoredWriter{
		ResponseWriter: w,
	}

	window := 15 * time.Second
	minBytesPerWindow := int64(minKBps) * 1024 * 15

	monitorCtx, cancel := context.WithCancel(ctx)

	go func() {
		ticker := time.NewTicker(window)
		defer ticker.Stop()

		rc := http.NewResponseController(w)

		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
				if mw.done.Load() {
					return
				}
				// Check throughput in the past window
				bytesSent := mw.writtenBytes.Swap(0)
				if bytesSent < minBytesPerWindow {
					// Slow client detected! Force close the connection
					_ = rc.SetWriteDeadline(time.Now())
					return
				}
			}
		}
	}()

	cleanup := func() {
		mw.MarkDone()
		cancel()
	}

	return mw, cleanup
}
