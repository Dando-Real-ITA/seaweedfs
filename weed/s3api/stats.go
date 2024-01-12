package s3api

import (
	"github.com/seaweedfs/seaweedfs/weed/s3api/s3_constants"
	stats_collect "github.com/seaweedfs/seaweedfs/weed/stats"
	"net/http"
	"strconv"
	"time"

	"github.com/seaweedfs/seaweedfs/weed/util"
)

func track(f http.HandlerFunc, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bucket, _ := s3_constants.GetBucketAndObject(r)
		w.Header().Set("Server", "SeaweedFS S3 "+util.Version())
		recorder := stats_collect.NewStatusResponseWriter(w)
		start := time.Now()
		f(recorder, r)
		if recorder.Status == http.StatusForbidden {
			if m, _ := stats_collect.S3RequestCounter.GetMetricWithLabelValues(
				action, strconv.Itoa(http.StatusOK), bucket); m == nil {
				bucket = ""
			}
		}
		stats_collect.S3RequestHistogram.WithLabelValues(action, bucket).Observe(time.Since(start).Seconds())
		stats_collect.S3RequestCounter.WithLabelValues(action, strconv.Itoa(recorder.Status), bucket).Inc()
	}
}

func TimeToFirstByte(action string, start time.Time, r *http.Request) {
	bucket, _ := s3_constants.GetBucketAndObject(r)
	stats_collect.S3TimeToFirstByteHistogram.WithLabelValues(action, bucket).Observe(float64(time.Since(start).Milliseconds()))
}
