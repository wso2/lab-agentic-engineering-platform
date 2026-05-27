// Package clustergatewayproxy is the asdlc-service HTTP client for the
// wso2cloud cluster-gateway-proxy (WS1.4). The proxy fronts the
// cloud-dp k8s API and serves un-authenticated requests to the
// CR allow-list (apigateways, restapis, configmaps, secrets, httproutes
// today; jobs / externalsecrets / serviceaccounts after the WS1.0 PR
// against wso2cloud-deployement-main lands).
//
// We use the same call shape as `wso2cloud/backend/core/internal/ou`'s
// `cpapi.go`:
//
//   - base URL ends `/cloud-dp-cgw`
//   - request URL is `<base>/cloud-dp-cgw/<k8s path>`
//   - no Authorization header — the proxy's middleware is logger-only
//     today; tracing rides `X-Correlation-ID`
//
// `Ensure*` methods are POST+409-tolerant (the proxy's pass-through
// returns the k8s 409 verbatim, which we map to "already exists"). The
// `Apply*` methods are upserts: POST first, on 409 fall back to PUT
// with the existing resourceVersion. Callers don't have to think
// about idempotency.
package clustergatewayproxy

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

	"github.com/wso2/asdlc/asdlc-service/middleware"
)

// ErrNotFound is returned by Get* methods when the proxy returns 404.
var ErrNotFound = errors.New("clustergatewayproxy: not found")

// Config holds connection settings.
type Config struct {
	// BaseURL is the proxy's base URL, e.g.
	// "http://cluster-gateway-proxy:8085" (compose) or
	// "https://cluster-gateway-proxy.openchoreo.dp.${cloud_base_domain}"
	// (cloud). The "/cloud-dp-cgw/" prefix is appended internally.
	BaseURL string
	// Timeout is the HTTP client timeout (default: 30s).
	Timeout time.Duration
}

// Client wraps the proxy HTTP calls.
type Client struct {
	baseURL    string
	httpClient *http.Client
	// streamClient is a separate http.Client with no per-request
	// timeout, used by StreamPodLog so `?follow=true` connections stay
	// open for the duration of the agent run. The request context is
	// the only cancellation signal — callers must cancel ctx to stop
	// the stream.
	streamClient *http.Client
}

// New constructs a Client; panics on empty BaseURL since main.go
// constructs this at boot and we want a loud failure if config is
// missing rather than silent skipped writes.
func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		panic("clustergatewayproxy: BaseURL is required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &Client{
		baseURL:      strings.TrimRight(cfg.BaseURL, "/"),
		httpClient:   &http.Client{Timeout: cfg.Timeout},
		streamClient: &http.Client{Timeout: 0},
	}
}

// ----- Namespace ------------------------------------------------------

// NamespaceMeta is the minimal manifest shape ApplyNamespace accepts.
type NamespaceMeta struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

// EnsureNamespace POSTs `/api/v1/namespaces`. 409 Conflict is treated as
// success — the namespace already exists.
func (c *Client) EnsureNamespace(ctx context.Context, meta NamespaceMeta) error {
	body := map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   meta,
	}
	return c.post(ctx, "/api/v1/namespaces", body, postOpts{tolerate409: true})
}

// NamespaceExists checks `/api/v1/namespaces/{name}`; returns (true, nil)
// on 200, (false, nil) on 404, (false, err) on anything else.
func (c *Client) NamespaceExists(ctx context.Context, name string) (bool, error) {
	resp, body, err := c.do(ctx, http.MethodGet, "/api/v1/namespaces/"+name, nil)
	if err != nil {
		return false, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("clustergatewayproxy: unexpected status %d checking namespace %q: %s",
			resp.StatusCode, name, string(body))
	}
}

// ----- ServiceAccount ------------------------------------------------

// EnsureServiceAccount POSTs a ServiceAccount; 409 is treated as success.
func (c *Client) EnsureServiceAccount(ctx context.Context, namespace, name string) error {
	body := map[string]any{
		"apiVersion": "v1",
		"kind":       "ServiceAccount",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
	}
	path := fmt.Sprintf("/api/v1/namespaces/%s/serviceaccounts", namespace)
	return c.post(ctx, path, body, postOpts{tolerate409: true})
}

// ----- ExternalSecret ------------------------------------------------

// ApplyExternalSecret POSTs the manifest; on 409 it falls back to PUT
// at the same name so the spec is reconciled to the desired shape.
// Callers pass the full manifest map (apiVersion/kind/metadata/spec) so
// this client doesn't have to model ESO's API surface.
func (c *Client) ApplyExternalSecret(ctx context.Context, namespace string, manifest map[string]any) error {
	name, err := manifestName(manifest)
	if err != nil {
		return err
	}
	listPath := fmt.Sprintf("/apis/external-secrets.io/v1/namespaces/%s/externalsecrets", namespace)
	itemPath := fmt.Sprintf("%s/%s", listPath, name)
	return c.upsert(ctx, listPath, itemPath, manifest)
}

// DeleteExternalSecret removes the ExternalSecret by name. 404 is
// treated as success so the watcher's per-run cleanup is idempotent
// across restarts and retries.
func (c *Client) DeleteExternalSecret(ctx context.Context, namespace, name string) error {
	path := fmt.Sprintf("/apis/external-secrets.io/v1/namespaces/%s/externalsecrets/%s", namespace, name)
	resp, body, err := c.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent, http.StatusNotFound:
		return nil
	}
	return fmt.Errorf("clustergatewayproxy: delete externalsecret %s/%s: status %d: %s",
		namespace, name, resp.StatusCode, string(body))
}

// ----- Job -----------------------------------------------------------

// ApplyJob POSTs the manifest; on 409 falls back to DELETE + POST since
// batch/v1 Jobs are immutable wrt spec changes (a new Job for a new
// dispatch is always a fresh object). Use GetJob to read status.
func (c *Client) ApplyJob(ctx context.Context, namespace string, manifest map[string]any) error {
	name, err := manifestName(manifest)
	if err != nil {
		return err
	}
	listPath := fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs", namespace)
	itemPath := fmt.Sprintf("%s/%s?propagationPolicy=Background", listPath, name)
	return c.post(ctx, listPath, manifest, postOpts{
		tolerate409:      false,
		on409RecreateVia: itemPath,
	})
}

// GetJob fetches the Job's status block. Returns ErrNotFound on 404.
func (c *Client) GetJob(ctx context.Context, namespace, name string) (*JobStatus, error) {
	path := fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs/%s", namespace, name)
	resp, body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clustergatewayproxy: get job %s/%s: status %d: %s",
			namespace, name, resp.StatusCode, string(body))
	}
	var out struct {
		Status JobStatus `json:"status"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("clustergatewayproxy: decode job status: %w", err)
	}
	return &out.Status, nil
}

// DeleteJob removes the Job (background propagation so the pods go too).
// 404 is treated as success.
func (c *Client) DeleteJob(ctx context.Context, namespace, name string) error {
	path := fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs/%s?propagationPolicy=Background", namespace, name)
	resp, body, err := c.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent, http.StatusNotFound:
		return nil
	}
	return fmt.Errorf("clustergatewayproxy: delete job %s/%s: status %d: %s",
		namespace, name, resp.StatusCode, string(body))
}

// JobStatus is the subset of batch/v1 Job status we read.
type JobStatus struct {
	Active         int                 `json:"active,omitempty"`
	Succeeded      int                 `json:"succeeded,omitempty"`
	Failed         int                 `json:"failed,omitempty"`
	StartTime      string              `json:"startTime,omitempty"`
	CompletionTime string              `json:"completionTime,omitempty"`
	Conditions     []JobStatusConditon `json:"conditions,omitempty"`
}

type JobStatusConditon struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// ----- Pod ----------------------------------------------------------

// GetJobPodName returns the name of the (first) pod owned by the named
// Job. Used by JobWatcher + ProgressService to translate a runName
// (= jobName) into the actual pod name for `pods/log` calls.
// Returns ErrNotFound if no pods match.
func (c *Client) GetJobPodName(ctx context.Context, namespace, jobName string) (string, error) {
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods?labelSelector=batch.kubernetes.io%%2Fjob-name%%3D%s",
		namespace, jobName)
	resp, body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("clustergatewayproxy: list pods %s/%s: status %d: %s",
			namespace, jobName, resp.StatusCode, string(body))
	}
	var out struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("clustergatewayproxy: decode pod list: %w", err)
	}
	if len(out.Items) == 0 {
		return "", ErrNotFound
	}
	return out.Items[0].Metadata.Name, nil
}

// ----- Pod log -------------------------------------------------------

// PodLogOptions mirrors the subset of K8s `pods/log` query params the
// BFF uses. Zero values map to "K8s default" (full log, no tail limit).
type PodLogOptions struct {
	// Container picks a specific container if the pod has multiple;
	// omit for single-container pods (the agent runner today).
	Container string
	// Follow=true keeps the connection open and streams new lines
	// as they're written (use with StreamPodLog).
	Follow bool
	// SinceSeconds returns only logs newer than now-N seconds. 0 = all.
	SinceSeconds int64
	// TailLines caps the response to the last N lines. 0 = all.
	// Use for TailPodLog snapshots to bound the captured size on
	// runaway-verbose agents.
	TailLines int64
	// Timestamps prefixes each line with the RFC3339Nano emit time.
	Timestamps bool
	// LimitBytes hard-caps the bytes K8s returns (server-side trim).
	// Zero = no cap.
	LimitBytes int64
}

func (o PodLogOptions) toQuery() string {
	var sb strings.Builder
	add := func(k, v string) {
		if sb.Len() > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(v)
	}
	if o.Container != "" {
		add("container", o.Container)
	}
	if o.Follow {
		add("follow", "true")
	}
	if o.SinceSeconds > 0 {
		add("sinceSeconds", fmt.Sprintf("%d", o.SinceSeconds))
	}
	if o.TailLines > 0 {
		add("tailLines", fmt.Sprintf("%d", o.TailLines))
	}
	if o.Timestamps {
		add("timestamps", "true")
	}
	if o.LimitBytes > 0 {
		add("limitBytes", fmt.Sprintf("%d", o.LimitBytes))
	}
	if sb.Len() == 0 {
		return ""
	}
	return "?" + sb.String()
}

// StreamPodLog opens a streaming connection to the pod's log endpoint
// and returns the response body for the caller to read + close. Closes
// must be called even on errors after this returns. Use Follow=true
// for live-tail; cancel ctx to stop the stream.
//
// On 404 returns ErrNotFound. On any other non-2xx status the response
// body is drained and discarded, returning a formatted error so the
// caller doesn't have to inspect the response.
func (c *Client) StreamPodLog(ctx context.Context, namespace, podName string, opts PodLogOptions) (io.ReadCloser, error) {
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log%s", namespace, podName, opts.toQuery())
	url := c.baseURL + "/cloud-dp-cgw" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("clustergatewayproxy: build request: %w", err)
	}
	req.Header.Set("X-Correlation-ID", middleware.GetCorrelationID(ctx))
	resp, err := c.streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clustergatewayproxy: GET %s: %w", url, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return nil, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("clustergatewayproxy: GET %s status %d: %s",
			path, resp.StatusCode, string(body))
	}
	return resp.Body, nil
}

// TailPodLog reads the pod log in one shot — used by JobWatcher to
// capture the final tail when a Job hits terminal state. opts.Follow
// is ignored (forced false). opts.LimitBytes is recommended on
// runaway-verbose agents to bound the captured snapshot size.
func (c *Client) TailPodLog(ctx context.Context, namespace, podName string, opts PodLogOptions) ([]byte, error) {
	opts.Follow = false
	body, err := c.StreamPodLog(ctx, namespace, podName, opts)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(body)
}

// ----- internals -----------------------------------------------------

type postOpts struct {
	tolerate409      bool
	on409RecreateVia string // if set, a 409 → DELETE this path → re-POST
}

func (c *Client) post(ctx context.Context, path string, body any, opts postOpts) error {
	resp, respBody, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusConflict {
		if opts.tolerate409 {
			return nil
		}
		if opts.on409RecreateVia != "" {
			if delResp, delBody, delErr := c.do(ctx, http.MethodDelete, opts.on409RecreateVia, nil); delErr != nil {
				return fmt.Errorf("clustergatewayproxy: 409 recovery delete failed: %w", delErr)
			} else if delResp.StatusCode >= 400 && delResp.StatusCode != http.StatusNotFound {
				return fmt.Errorf("clustergatewayproxy: 409 recovery delete status %d: %s",
					delResp.StatusCode, string(delBody))
			}
			reResp, reBody, reErr := c.do(ctx, http.MethodPost, path, body)
			if reErr != nil {
				return reErr
			}
			if reResp.StatusCode >= 200 && reResp.StatusCode < 300 {
				return nil
			}
			return fmt.Errorf("clustergatewayproxy: re-POST status %d: %s",
				reResp.StatusCode, string(reBody))
		}
	}
	return fmt.Errorf("clustergatewayproxy: POST %s status %d: %s",
		path, resp.StatusCode, string(respBody))
}

// upsert POSTs the manifest; if 409, it PUTs at itemPath instead. The
// resourceVersion is left to the proxy to inject — typed ApplyJob calls
// don't go through upsert because Jobs are immutable.
func (c *Client) upsert(ctx context.Context, listPath, itemPath string, body any) error {
	resp, respBody, err := c.do(ctx, http.MethodPost, listPath, body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusConflict {
		putResp, putBody, putErr := c.do(ctx, http.MethodPut, itemPath, body)
		if putErr != nil {
			return putErr
		}
		if putResp.StatusCode >= 200 && putResp.StatusCode < 300 {
			return nil
		}
		return fmt.Errorf("clustergatewayproxy: PUT %s status %d: %s",
			itemPath, putResp.StatusCode, string(putBody))
	}
	return fmt.Errorf("clustergatewayproxy: POST %s status %d: %s",
		listPath, resp.StatusCode, string(respBody))
}

// do performs a single proxy request. The body is JSON-marshalled when
// non-nil; the response body is fully read and returned for inspection.
func (c *Client) do(ctx context.Context, method, k8sPath string, body any) (*http.Response, []byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("clustergatewayproxy: marshal body: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	url := c.baseURL + "/cloud-dp-cgw" + k8sPath
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, nil, fmt.Errorf("clustergatewayproxy: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Correlation-ID", middleware.GetCorrelationID(ctx))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("clustergatewayproxy: %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out, nil
}

func manifestName(manifest map[string]any) (string, error) {
	meta, ok := manifest["metadata"].(map[string]any)
	if !ok {
		return "", errors.New("clustergatewayproxy: manifest missing metadata")
	}
	name, ok := meta["name"].(string)
	if !ok || name == "" {
		return "", errors.New("clustergatewayproxy: manifest metadata.name missing")
	}
	return name, nil
}
