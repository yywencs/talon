package simulatorhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wen/opentalon/internal/platform"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	url     string
	http    *http.Client
	binding platform.RunConnection
}

var _ platform.ToolOpsPlatform = (*Client)(nil)
var _ platform.RunConnector = (*Client)(nil)

func NewClient(url string, timeout time.Duration) (*Client, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("positive HTTP timeout required")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("HTTP URL required")
	}
	return &Client{url: strings.TrimRight(url, "/"), http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) call(ctx context.Context, method string, input, output any, write bool) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.url+"/v1/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if write {
			return errors.Join(platform.ErrOutcomeUnknown, err)
		}
		return err
	}
	defer resp.Body.Close()
	var wire response
	dec := json.NewDecoder(io.LimitReader(resp.Body, 8<<20))
	dec.UseNumber()
	if err = dec.Decode(&wire); err != nil {
		if write {
			return errors.Join(platform.ErrOutcomeUnknown, err)
		}
		return err
	}
	if output != nil && len(wire.Result) > 0 {
		d := json.NewDecoder(bytes.NewReader(wire.Result))
		d.UseNumber()
		if err = d.Decode(output); err != nil {
			if write {
				return errors.Join(platform.ErrOutcomeUnknown, err)
			}
			return err
		}
	}
	if wire.Error != nil {
		return fmt.Errorf("%w: %s", errorFromCode(wire.Error.Code), wire.Error.Message)
	}
	if resp.StatusCode != 200 {
		if write {
			return platform.ErrOutcomeUnknown
		}
		return fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	return nil
}
func (c *Client) ConnectRun(ctx context.Context, b platform.RunConnection) (platform.ToolOpsPlatform, error) {
	err := c.call(ctx, "connect", b, nil, true)
	if err != nil {
		return nil, err
	}
	out := *c
	out.binding = b
	return &out, nil
}
func (c *Client) Info(ctx context.Context) (Info, error) {
	var out Info
	err := c.call(ctx, "info", nil, &out, false)
	return out, err
}
func (c *Client) GetOperationByKey(ctx context.Context, q platform.OperationKeyQuery) (platform.Operation, error) {
	var out platform.Operation
	err := c.invoke(ctx, "GetOperationByKey", q, &out, false)
	return out, err
}
func (c *Client) invoke(ctx context.Context, method string, q, out any, write bool) error {
	payload, err := json.Marshal(q)
	if err != nil {
		return err
	}
	return c.call(ctx, method, request{Binding: c.binding, Payload: payload}, out, write)
}
func (c *Client) QueryLogs(ctx context.Context, q platform.LogQuery) ([]platform.LogEntry, error) {
	var out []platform.LogEntry
	err := c.invoke(ctx, "QueryLogs", q, &out, false)
	return out, err
}
func (c *Client) QueryTraces(ctx context.Context, q platform.TraceQuery) ([]platform.TraceRecord, error) {
	var out []platform.TraceRecord
	err := c.invoke(ctx, "QueryTraces", q, &out, false)
	return out, err
}
func (c *Client) GetServices(ctx context.Context, q platform.StateQuery) ([]platform.Service, error) {
	var out []platform.Service
	err := c.invoke(ctx, "GetServices", q, &out, false)
	return out, err
}
func (c *Client) GetRoutes(ctx context.Context, q platform.StateQuery) ([]platform.Route, error) {
	var out []platform.Route
	err := c.invoke(ctx, "GetRoutes", q, &out, false)
	return out, err
}
func (c *Client) GetProviders(ctx context.Context, q platform.StateQuery) ([]platform.Provider, error) {
	var out []platform.Provider
	err := c.invoke(ctx, "GetProviders", q, &out, false)
	return out, err
}
func (c *Client) GetConfigVersions(ctx context.Context, q platform.StateQuery) ([]platform.ConfigVersion, error) {
	var out []platform.ConfigVersion
	err := c.invoke(ctx, "GetConfigVersions", q, &out, false)
	return out, err
}
func (c *Client) GetChangeRecords(ctx context.Context, q platform.ChangeQuery) ([]platform.ChangeRecord, error) {
	var out []platform.ChangeRecord
	err := c.invoke(ctx, "GetChangeRecords", q, &out, false)
	return out, err
}
func (c *Client) GetCredentialMetadata(ctx context.Context, q platform.StateQuery) ([]platform.CredentialMetadata, error) {
	var out []platform.CredentialMetadata
	err := c.invoke(ctx, "GetCredentialMetadata", q, &out, false)
	return out, err
}
func (c *Client) GetConnectionMetadata(ctx context.Context, q platform.StateQuery) ([]platform.ConnectionMetadata, error) {
	var out []platform.ConnectionMetadata
	err := c.invoke(ctx, "GetConnectionMetadata", q, &out, false)
	return out, err
}
func (c *Client) GetTasks(ctx context.Context, q platform.TaskQuery) ([]platform.ManagedTask, error) {
	var out []platform.ManagedTask
	err := c.invoke(ctx, "GetTasks", q, &out, false)
	return out, err
}
func (c *Client) GetRemediationCapabilities(ctx context.Context, q platform.StateQuery) ([]platform.RemediationCapability, error) {
	var out []platform.RemediationCapability
	err := c.invoke(ctx, "GetRemediationCapabilities", q, &out, false)
	return out, err
}
func (c *Client) GetRecoveryPolicies(ctx context.Context, q platform.StateQuery) ([]platform.RecoveryPolicy, error) {
	var out []platform.RecoveryPolicy
	err := c.invoke(ctx, "GetRecoveryPolicies", q, &out, false)
	return out, err
}
func (c *Client) GetProbeEscalationPolicy(ctx context.Context, q platform.StateQuery) (platform.ProbeEscalationPolicy, error) {
	var out platform.ProbeEscalationPolicy
	err := c.invoke(ctx, "GetProbeEscalationPolicy", q, &out, false)
	return out, err
}
func (c *Client) ExecuteRemediation(ctx context.Context, q platform.RemediationRequest) (platform.Operation, error) {
	var out platform.Operation
	err := c.invoke(ctx, "ExecuteRemediation", q, &out, true)
	return out, err
}
func (c *Client) RequestProbe(ctx context.Context, q platform.ProbeRequest) (platform.Operation, error) {
	var out platform.Operation
	err := c.invoke(ctx, "RequestProbe", q, &out, true)
	return out, err
}
func (c *Client) RequestRecovery(ctx context.Context, q platform.RecoveryRequest) (platform.Operation, error) {
	var out platform.Operation
	err := c.invoke(ctx, "RequestRecovery", q, &out, true)
	return out, err
}
func (c *Client) GetOperation(ctx context.Context, q platform.OperationQuery) (platform.Operation, error) {
	var out platform.Operation
	err := c.invoke(ctx, "GetOperation", q, &out, false)
	return out, err
}
func (c *Client) EscalateIncident(ctx context.Context, q platform.EscalationRequest) (platform.Operation, error) {
	var out platform.Operation
	err := c.invoke(ctx, "EscalateIncident", q, &out, true)
	return out, err
}

func (c *Client) QueryMetrics(ctx context.Context, q platform.MetricQuery) (platform.MetricResult, error) {
	var out platform.MetricResult
	err := c.invoke(ctx, "QueryMetrics", q, &out, false)
	return out, err
}

func (c *Client) EnvironmentMetadata(ctx context.Context) (platform.EnvironmentMetadata, error) {
	info, err := c.Info(ctx)
	if err != nil {
		return platform.EnvironmentMetadata{}, err
	}
	if strings.TrimSpace(info.DatasetVersion) == "" {
		return platform.EnvironmentMetadata{}, fmt.Errorf("Simulator info missing dataset_version")
	}
	return platform.EnvironmentMetadata{EnvironmentID: info.EnvironmentID, IncidentID: info.IncidentID, DatasetVersion: info.DatasetVersion}, nil
}
