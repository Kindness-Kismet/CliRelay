package openai

import (
	"strings"
	"sync"
	"time"
)

// Submission registry for asynchronous video jobs.
//
// A video request id belongs to the credential that created it: polling it with a
// different account returns 404. The proxy therefore has to remember which
// credential pool served each submission.
//
// This is deliberately in-memory. The alternative — a table — buys durability
// across restarts for a job that xAI itself expires, and every id here is
// worthless within the hour. A restart during a generation costs the caller a
// clear "not known to this server" answer, which is honest and recoverable by
// resubmitting; it is not silent data loss.
const videoJobTTL = time.Hour

type videoJob struct {
	Model    string
	Provider string
	TenantID string

	expiresAt time.Time
}

var videoJobRegistry sync.Map

func rememberVideoJob(requestID string, job videoJob) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	job.expiresAt = time.Now().Add(videoJobTTL)
	videoJobRegistry.Store(requestID, job)
	purgeExpiredVideoJobs()
}

func lookupVideoJob(requestID string) (videoJob, bool) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return videoJob{}, false
	}
	raw, ok := videoJobRegistry.Load(requestID)
	if !ok {
		return videoJob{}, false
	}
	job, _ := raw.(videoJob)
	if time.Now().After(job.expiresAt) {
		videoJobRegistry.Delete(requestID)
		return videoJob{}, false
	}
	return job, true
}

func forgetVideoJob(requestID string) {
	if requestID = strings.TrimSpace(requestID); requestID != "" {
		videoJobRegistry.Delete(requestID)
	}
}

// purgeExpiredVideoJobs keeps the map from growing without bound on a long-running
// process whose callers never poll to completion.
func purgeExpiredVideoJobs() {
	now := time.Now()
	videoJobRegistry.Range(func(key, value any) bool {
		if job, ok := value.(videoJob); ok && now.After(job.expiresAt) {
			videoJobRegistry.Delete(key)
		}
		return true
	})
}
