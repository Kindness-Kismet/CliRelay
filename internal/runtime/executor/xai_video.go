package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// Grok Imagine video generation.
//
// Unlike the image endpoints, video is asynchronous upstream: POST
// /videos/generations returns only a request id, and the clip is fetched by
// polling GET /videos/{request_id} until status leaves "pending". That shape is
// preserved rather than hidden behind a blocking call — a generation runs for
// minutes, well past any sane HTTP timeout on the client side — so callers get the
// same two-step flow xAI documents.
const (
	xaiVideoGenerationAlt = "videos/generations"
	xaiVideoStatusAlt     = "videos/status"

	xaiVideosGenerationsPath = "/videos/generations"
	xaiVideosPath            = "/videos/"
)

// xaiIsVideoAlt reports whether a request targets one of the video endpoints.
func xaiIsVideoAlt(alt string) bool {
	switch strings.TrimSpace(alt) {
	case xaiVideoGenerationAlt, xaiVideoStatusAlt:
		return true
	default:
		return false
	}
}

// executeVideoGeneration submits a generation request and returns the upstream
// body verbatim, which carries the request id the caller polls with.
func (e *XAIExecutor) executeVideoGeneration(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	execCtx := newExecutionContext(ctx, e.Identifier(), e.cfg, auth, req, opts, ExecutionOptions{})
	reporter := execCtx.Reporter()
	defer reporter.trackFailure(execCtx.Context, &err)

	token, _ := xaiCreds(auth)
	if strings.TrimSpace(token) == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "xai credential has no access token"}
	}

	payload := req.Payload
	if len(bytes.TrimSpace(payload)) == 0 {
		return resp, statusErr{code: http.StatusBadRequest, msg: "video request body is empty"}
	}

	payload, dropped := shapeXAIVideoRequest(payload)
	if len(dropped) > 0 {
		logWithRequestID(execCtx.Context).Debugf(
			"xai video request: dropped unsupported arguments %s", strings.Join(dropped, ", "),
		)
	}

	endpoint := strings.TrimSuffix(xaiMediaBaseURL(auth), "/") + xaiVideosGenerationsPath
	data, headers, err := e.doVideoRequest(execCtx, auth, http.MethodPost, endpoint, payload, req.Payload, reporter)
	if err != nil {
		return resp, err
	}

	// A submission that produced a request id has consumed the subscription's video
	// budget even though no clip exists yet, so it is reported as one call. xAI
	// returns no token counts for media, so this rides on ensurePublished, which
	// records the call itself; cost then comes from the model's per-call price.
	// The later status polls are free and must not be counted again.
	reporter.setInputContentBytes(req.Payload)
	reporter.appendOutputChunk(data)
	reporter.ensurePublished(execCtx.Context)

	return cliproxyexecutor.Response{Payload: data, Headers: headers}, nil
}

// executeVideoStatus polls one generation. The request id travels in the request
// model field, because the shared executor contract has no other slot for a path
// parameter and the video status call has no body.
func (e *XAIExecutor) executeVideoStatus(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	execCtx := newExecutionContext(ctx, e.Identifier(), e.cfg, auth, req, opts, ExecutionOptions{})

	token, _ := xaiCreds(auth)
	if strings.TrimSpace(token) == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "xai credential has no access token"}
	}

	requestID := strings.TrimSpace(gjson.GetBytes(req.Payload, "request_id").String())
	if requestID == "" {
		return resp, statusErr{code: http.StatusBadRequest, msg: "request_id is required"}
	}
	if !xaiIsSafeVideoRequestID(requestID) {
		return resp, statusErr{code: http.StatusBadRequest, msg: "request_id is malformed"}
	}

	endpoint := strings.TrimSuffix(xaiMediaBaseURL(auth), "/") + xaiVideosPath + requestID
	// Polling is not a billable call and must not emit a usage record: a two minute
	// generation polled every five seconds would otherwise log two dozen phantom
	// requests against the account.
	data, headers, err := e.doVideoRequest(execCtx, auth, http.MethodGet, endpoint, nil, req.Payload, nil)
	if err != nil {
		return resp, err
	}
	return cliproxyexecutor.Response{Payload: data, Headers: headers}, nil
}

// doVideoRequest performs one upstream call and returns its body.
//
// reporter is optional: status polls pass nil because they must not produce a
// usage record, not even a failure one.
func (e *XAIExecutor) doVideoRequest(
	execCtx *ExecutionContext,
	auth *cliproxyauth.Auth,
	method string,
	endpoint string,
	body []byte,
	originalPayload []byte,
	reporter *usageReporter,
) ([]byte, http.Header, error) {
	token, _ := xaiCreds(auth)

	var reader *bytes.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	httpReq, err := http.NewRequestWithContext(execCtx.Context, method, endpoint, reader)
	if err != nil {
		return nil, nil, err
	}
	// Media never goes to the CLI gateway, so the Grok CLI client headers that
	// gateway requires are deliberately not applied here.
	applyXAIHeaders(httpReq, e.cfg, auth, token, false)
	if method == http.MethodGet {
		httpReq.Header.Del("Content-Type")
	}

	recorder := execCtx.Recorder()
	recorder.RecordRequest(endpoint, method, httpReq.Header.Clone(), body)

	httpResp, err := execCtx.HTTPClient(0).Do(httpReq) //nolint:bodyclose // closed by the defer below.
	if err != nil {
		recorder.RecordResponseError(err)
		if reporter != nil {
			reporter.publishFailureWithContentBytes(execCtx.Context, originalPayload, err.Error())
		}
		return nil, nil, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("xai executor: close video response body error: %v", errClose)
		}
	}()

	recorder.RecordResponseMetadata(httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		errBody := readUpstreamErrorBody(e.Identifier(), httpResp.Body)
		recorder.AppendResponseChunk(errBody)
		logWithRequestID(execCtx.Context).Debugf(
			"xai video request error, status: %d, message: %s",
			httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), errBody),
		)
		if reporter != nil {
			reporter.publishFailureWithContentBytes(execCtx.Context, originalPayload, string(errBody))
		}
		// Reuse the chat status mapping so a 402 still lands in the weekly quota
		// cooldown path; a video call exhausts the same subscription balance.
		return nil, nil, newXAIStatusErr(httpResp.StatusCode, errBody, httpResp.Header)
	}

	data, err := readUpstreamResponseBody(e.Identifier(), httpResp.Body)
	if err != nil {
		recorder.RecordResponseError(err)
		return nil, nil, err
	}
	recorder.AppendResponseChunk(data)
	return data, httpResp.Header.Clone(), nil
}

// xaiIsSafeVideoRequestID guards the path parameter. The id is echoed into a URL,
// so anything that could escape the path segment is rejected rather than escaped:
// xAI ids are uuid-shaped and nothing legitimate needs the other characters.
func xaiIsSafeVideoRequestID(requestID string) bool {
	if len(requestID) > 128 {
		return false
	}
	for _, r := range requestID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_':
		default:
			return false
		}
	}
	return requestID != ""
}

// xaiVideoEndpointFor exposes endpoint resolution for tests and diagnostics.
func xaiVideoEndpointFor(auth *cliproxyauth.Auth, alt string, requestID string) (string, error) {
	base := strings.TrimSuffix(xaiMediaBaseURL(auth), "/")
	switch strings.TrimSpace(alt) {
	case xaiVideoGenerationAlt:
		return base + xaiVideosGenerationsPath, nil
	case xaiVideoStatusAlt:
		if !xaiIsSafeVideoRequestID(requestID) {
			return "", fmt.Errorf("request id %q is malformed", requestID)
		}
		return base + xaiVideosPath + requestID, nil
	default:
		return "", fmt.Errorf("alt %q is not an xai video endpoint", alt)
	}
}
