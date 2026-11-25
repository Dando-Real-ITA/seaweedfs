package weed_server

import (
	"bufio"
	"io"
	"net/http"

	"github.com/seaweedfs/seaweedfs/weed/glog"
)

// finishRangeWriter always returns a clean writer to the pool, even when
// aborting the response. A failed flush cannot be replayed safely on this socket.
func finishRangeWriter(bufferedWriter *bufio.Writer, discardBuffered bool) {
	defer func() {
		bufferedWriter.Reset(io.Discard)
		writePool.Put(bufferedWriter)
	}()
	if !discardBuffered {
		if err := bufferedWriter.Flush(); err != nil {
			glog.Errorf("ProcessRangeRequest flush: %v", err)
			panic(http.ErrAbortHandler)
		}
	}
}
