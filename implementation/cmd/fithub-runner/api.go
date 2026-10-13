package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// apiClient speaks the runner protocol. It holds the runner token and the job
// token of the job it is currently executing, and it never sends a user token.
type apiClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newAPIClient(baseURL, token string) *apiClient {
	return &apiClient{
		baseURL: baseURL,
		token:   token,
		// A long timeout is set per request; this bounds connection reuse so a
		// stuck server cannot hang the agent forever.
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// claimedJob is the response of a successful claim.
type claimedJob struct {
	AttemptID    string            `json:"attemptId"`
	Attempt      int               `json:"attempt"`
	JobToken     string            `json:"jobToken"`
	JobID        string            `json:"jobId"`
	JobName      string            `json:"jobName"`
	RunID        string            `json:"runId"`
	RunNumber    int64             `json:"runNumber"`
	WorkflowPath string            `json:"workflowPath"`
	Event        string            `json:"event"`
	HeadSHA      string            `json:"headSha"`
	Ref          string            `json:"ref"`
	Candidate    bool              `json:"candidate"`
	Trusted      bool              `json:"trusted"`
	Definition   json.RawMessage   `json:"definition"`
	Secrets      map[string]string `json:"secrets"`
}

// errNoWork means the queue is empty.
var errNoWork = errors.New("no work available")

// errLeaseLost means the control plane no longer recognises this attempt, so the
// agent must stop immediately rather than keep running the job.
var errLeaseLost = errors.New("job lease lost")

// claim asks for one job. A 204 response is the empty-queue case.
func (c *apiClient) claim(ctx context.Context) (claimedJob, error) {
	var job claimedJob
	status, err := c.do(ctx, http.MethodPost, "/api/v1/ci/jobs/claim", "", nil, &job)
	if errors.Is(err, errNoWork) {
		return claimedJob{}, errNoWork
	}
	if err != nil {
		return claimedJob{}, err
	}
	if status != http.StatusOK {
		return claimedJob{}, fmt.Errorf("claim returned status %d", status)
	}
	if job.AttemptID == "" || job.JobToken == "" {
		return claimedJob{}, errors.New("claim response is missing an attempt or job token")
	}
	return job, nil
}

// start marks the attempt running.
func (c *apiClient) start(ctx context.Context, jobToken string) error {
	_, err := c.do(ctx, http.MethodPost, "/api/v1/ci/jobs/start", jobToken, nil, nil)
	return err
}

// heartbeat renews the lease. A lost lease is reported distinctly so the agent
// can stop the running container.
func (c *apiClient) heartbeat(ctx context.Context, jobToken string) error {
	_, err := c.do(ctx, http.MethodPost, "/api/v1/ci/jobs/heartbeat", jobToken, nil, nil)
	return err
}

// complete reports the terminal result of the attempt.
func (c *apiClient) complete(ctx context.Context, jobToken, state string, exitCode *int, message string) error {
	body := map[string]any{"state": state}
	if exitCode != nil {
		body["exitCode"] = *exitCode
	}
	if message != "" {
		// Keep the message bounded; the server rejects anything larger.
		if len(message) > 4000 {
			message = message[:4000]
		}
		body["errorMessage"] = message
	}
	_, err := c.do(ctx, http.MethodPost, "/api/v1/ci/jobs/complete", jobToken, body, nil)
	return err
}

// appendLog uploads one log chunk.
func (c *apiClient) appendLog(ctx context.Context, jobToken string, sequence int64, content string) error {
	_, err := c.do(ctx, http.MethodPost, "/api/v1/ci/jobs/log", jobToken,
		map[string]any{"sequence": sequence, "content": content}, nil)
	return err
}

// do performs one protocol request. It maps the protocol's conflict status to a
// typed lease error so callers can stop cleanly, and an empty claim to errNoWork.
func (c *apiClient) do(ctx context.Context, method, path, jobToken string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if jobToken != "" {
		request.Header.Set("X-FitHub-Job-Token", jobToken)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	// The response body is bounded: the control plane never sends an unbounded
	// payload to a runner, and a compromised server must not exhaust agent memory.
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, err
	}
	switch {
	case response.StatusCode == http.StatusNoContent:
		return response.StatusCode, errNoWork
	case response.StatusCode == http.StatusConflict && strings.Contains(string(payload), "LEASE_LOST"):
		return response.StatusCode, errLeaseLost
	case response.StatusCode >= 400:
		return response.StatusCode, fmt.Errorf("%s %s failed with %d: %s", method, path, response.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, out); err != nil {
			return response.StatusCode, fmt.Errorf("decode %s response: %w", path, err)
		}
	}
	return response.StatusCode, nil
}
