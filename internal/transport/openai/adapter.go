package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
)

type inFlightReq struct {
	cancel    context.CancelFunc
	requestID int64
}

type Adapter struct {
	profile     config.EndpointProfile
	resolver    config.SecretResolver
	client      *http.Client
	profileHost string
	maxBodySize int64

	mu           sync.Mutex
	lastUsage    *transport.Usage
	inFlightReqs map[string]inFlightReq
	requestIDGen int64
}

const defaultMaxBodySize = 16 * 1024 * 1024

func New(profile config.EndpointProfile, resolver config.SecretResolver) *Adapter {
	baseURL, _ := url.Parse(profile.BaseURL)
	profileHost := ""
	if baseURL != nil {
		profileHost = baseURL.Host
	}

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			newHost := req.URL.Host
			if newHost != profileHost {
				return http.ErrUseLastResponse
			}
			return nil
		},
		Timeout: 0,
	}

	return &Adapter{
		profile:      profile,
		resolver:     resolver,
		client:       client,
		profileHost:  profileHost,
		maxBodySize:  defaultMaxBodySize,
		inFlightReqs: make(map[string]inFlightReq),
	}
}

type AdapterOption func(*Adapter)

func WithMaxBodySize(size int64) AdapterOption {
	return func(a *Adapter) {
		if size > 0 {
			a.maxBodySize = size
		}
	}
}

func NewWithOptions(profile config.EndpointProfile, resolver config.SecretResolver, opts ...AdapterOption) *Adapter {
	a := New(profile, resolver)
	for _, opt := range opts {
		opt(a)
	}
	return a
}

type ProbeOptions struct {
	Timeout           time.Duration
	DisableOverLength bool
	AssumedContextLen int
}

func (a *Adapter) Probe(ctx context.Context, endpoint, model string) (transport.CapabilityRecord, error) {
	opts := ProbeOptions{
		Timeout:           60 * time.Second,
		DisableOverLength: false,
		AssumedContextLen: 131072,
	}
	return a.ProbeWithOptions(ctx, endpoint, model, opts)
}

func (a *Adapter) ProbeWithOptions(ctx context.Context, endpoint, model string, opts ProbeOptions) (transport.CapabilityRecord, error) {
	if endpoint != a.profile.Name {
		return transport.CapabilityRecord{}, fmt.Errorf("endpoint %q does not match profile name %q", endpoint, a.profile.Name)
	}

	record := transport.CapabilityRecord{
		Endpoint:                 endpoint,
		Model:                    model,
		AuthMode:                 "unknown",
		Identity:                 transport.NewUnknownIdentity(),
		ReportedModel:            "",
		ContextBound:             nil,
		OutputBound:              nil,
		Streaming:                transport.TriUnknown,
		CancelObservable:         transport.TriNo,
		UsageAvailable:           transport.TriUnknown,
		ModelsEndpoint:           transport.TriUnknown,
		ModelListed:              transport.TriUnknown,
		SystemRoleHonored:        transport.TriUnknown,
		EligibleRequiredReviewer: transport.TriUnknown,
		MaxTokensField:           "",
		FinishReasons:            make(map[string]transport.CompletionStatus),
		OverLength:               "not_run",
		LatencyMillis:            nil,
		ToolRestrictions:         []string{"no tool definitions sent"},
		ServerVersion:            "unknown",
		AdapterVersion:           "openai-v1",
		ObservedAt:               time.Now(),
		Raw:                      json.RawMessage("{}"),
	}

	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var resolvedSecrets []string

	if a.profile.Auth != nil {
		authVal, err := a.resolver.Resolve(*a.profile.Auth)
		if err != nil {
			return record, err
		}
		resolvedSecrets = append(resolvedSecrets, authVal)
	}

	for _, secretRef := range a.profile.ExtraHeaders {
		headerVal, err := a.resolver.Resolve(secretRef)
		if err != nil {
			return record, err
		}
		resolvedSecrets = append(resolvedSecrets, headerVal)
	}

	baseURL, err := url.Parse(a.profile.BaseURL)
	if err != nil {
		return record, err
	}

	probeFinishReasons := make(map[string]transport.CompletionStatus)
	rawSteps := make(map[string]any)

	if err := a.probeModels(probeCtx, baseURL, &record, resolvedSecrets, rawSteps, opts.Timeout); err != nil {
		return record, err
	}

	step2Err, step2Body, step2Latency := a.probeMaxTokens(probeCtx, baseURL, &record, model, resolvedSecrets, rawSteps, opts.Timeout)

	if step2Err != nil {
		if errors.Is(step2Err, transport.ErrUnreachable) {
			scrubAndStore(&record, rawSteps, resolvedSecrets)
			return record, step2Err
		}
		if errors.Is(step2Err, transport.ErrUnauthorized) {
			scrubAndStore(&record, rawSteps, resolvedSecrets)
			return record, step2Err
		}
		if errors.Is(step2Err, transport.ErrModelUnavailable) {
			scrubAndStore(&record, rawSteps, resolvedSecrets)
			return record, step2Err
		}
	}

	if step2Body != nil {
		if step2Body.Usage != nil {
			record.UsageAvailable = transport.TriYes
		} else {
			record.UsageAvailable = transport.TriNo
		}

		if len(step2Body.Choices) > 0 {
			finishReason := step2Body.Choices[0].FinishReason
			if finishReason != "" {
				probeFinishReasons[finishReason] = a.normalizeFinishReason(finishReason)
			}

			if step2Body.Model != "" {
				record.ReportedModel = step2Body.Model
			}
		}

		if step2Err == nil {
			record.LatencyMillis = &step2Latency
			record.AuthMode = "api-key"
			if a.profile.Auth == nil {
				record.AuthMode = "none"
			}
		}
	}

	if err := a.probeSystemRole(probeCtx, baseURL, &record, model, resolvedSecrets, rawSteps, opts.Timeout, probeFinishReasons); err != nil {
		return record, err
	}

	if !opts.DisableOverLength {
		a.probeOverLength(probeCtx, baseURL, &record, model, resolvedSecrets, rawSteps, opts.Timeout, opts.AssumedContextLen, probeFinishReasons)
	}

	if len(probeFinishReasons) > 0 {
		record.FinishReasons = probeFinishReasons
	}

	if record.FinishReasons == nil || len(record.FinishReasons) == 0 {
		record.FinishReasons = make(map[string]transport.CompletionStatus)
	}

	if record.OverLength == "silent_truncation_likely" {
		record.EligibleRequiredReviewer = transport.TriNo
	}

	if err := scrubAndStore(&record, rawSteps, resolvedSecrets); err != nil {
		return record, err
	}

	return record, nil
}

func scrubAndStore(record *transport.CapabilityRecord, rawSteps map[string]any, resolvedSecrets []string) error {
	rawJSON, err := json.Marshal(rawSteps)
	if err != nil {
		return err
	}
	scrubbed, _ := transport.Scrub(string(rawJSON), resolvedSecrets)
	record.Raw = json.RawMessage(scrubbed)
	return nil
}

func (a *Adapter) Complete(ctx context.Context, req transport.CompleteRequest) (transport.CompleteResult, error) {
	result := transport.CompleteResult{
		Envelope: req.Envelope,
		Identity: transport.NewUnknownIdentity(),
	}

	for _, msg := range req.Messages {
		if msg.Role != "system" && msg.Role != "user" && msg.Role != "assistant" {
			return result, fmt.Errorf("invalid message role: %s", msg.Role)
		}
	}

	var resolvedSecrets []string
	headers := make(http.Header)

	if a.profile.Auth != nil {
		authVal, err := a.resolver.Resolve(*a.profile.Auth)
		if err != nil {
			return result, err
		}
		resolvedSecrets = append(resolvedSecrets, authVal)
		headers.Set("Authorization", "Bearer "+authVal)
	}

	for headerName, secretRef := range a.profile.ExtraHeaders {
		headerVal, err := a.resolver.Resolve(secretRef)
		if err != nil {
			return result, err
		}
		resolvedSecrets = append(resolvedSecrets, headerVal)
		headers.Set(headerName, headerVal)
	}

	reqBody := buildRequestBody(req, &a.profile)
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return result, err
	}

	baseURL, err := url.Parse(a.profile.BaseURL)
	if err != nil {
		return result, err
	}

	endpoint := baseURL.JoinPath("/chat/completions")

	reqCtx := ctx
	if !req.Deadline.IsZero() {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithDeadline(ctx, req.Deadline)
		defer cancel()
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, "POST", endpoint.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return result, err
	}

	httpReq.Header = headers
	httpReq.Header.Set("Content-Type", "application/json")

	a.mu.Lock()
	cancelFunc, cancel := context.WithCancel(reqCtx)
	a.requestIDGen++
	requestID := a.requestIDGen
	a.inFlightReqs[req.Envelope.AttemptID] = inFlightReq{cancel: cancel, requestID: requestID}
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		if entry, found := a.inFlightReqs[req.Envelope.AttemptID]; found && entry.requestID == requestID {
			delete(a.inFlightReqs, req.Envelope.AttemptID)
		}
		a.mu.Unlock()
		cancel()
	}()

	httpReq = httpReq.WithContext(cancelFunc)

	httpResp, err := a.client.Do(httpReq)
	if err != nil {
		if isDeadlineExceededErr(err) {
			result.Status = transport.StatusInterrupted
			result.TransportStatus = transport.TransportStatusTimeout
			return result, nil
		}

		if cancelFunc.Err() != nil {
			result.Status = transport.StatusInterrupted
			result.TransportStatus = transport.TransportStatusCancelled
			return result, nil
		}

		if isConnectionResetErr(err) {
			result.Status = transport.StatusInterrupted
			result.TransportStatus = transport.TransportStatusReset
			return result, nil
		}

		if isConnectionRefusedOrDNSErr(err) {
			result.Status = transport.StatusError
			result.TransportStatus = transport.TransportStatusUnreachable
			return result, nil
		}

		return result, err
	}

	defer func() {
		_ = httpResp.Body.Close()
	}()

	limitedBody := io.LimitReader(httpResp.Body, a.maxBodySize+1)
	rawBody, err := io.ReadAll(limitedBody)
	if err != nil {
		if isDeadlineExceededErr(err) {
			result.Status = transport.StatusInterrupted
			result.TransportStatus = transport.TransportStatusTimeout
			return result, nil
		}

		if cancelFunc.Err() != nil {
			result.Status = transport.StatusInterrupted
			result.TransportStatus = transport.TransportStatusCancelled
			return result, nil
		}

		if isConnectionResetErr(err) {
			result.Status = transport.StatusInterrupted
			result.TransportStatus = transport.TransportStatusReset
			return result, nil
		}

		return result, err
	}

	if int64(len(rawBody)) > a.maxBodySize {
		result.Status = transport.StatusError
		result.TransportStatus = transport.TransportStatusBodyTooLarge
		result.RawOutput = ""
		result.Text = ""
		return result, nil
	}

	rawOutput := string(rawBody)
	result.RawOutput, result.ScrubCount = transport.Scrub(rawOutput, resolvedSecrets)

	if httpResp.StatusCode == 401 || httpResp.StatusCode == 403 {
		result.Status = transport.StatusError
		result.TransportStatus = transport.TransportStatusUnauthorized
		return result, nil
	}

	if httpResp.StatusCode == 404 {
		result.Status = transport.StatusError
		result.TransportStatus = transport.TransportStatusModelUnavailable
		return result, nil
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result.Status = transport.StatusError
		result.TransportStatus = transport.HTTPStatusString(httpResp.StatusCode)
		return result, nil
	}

	var respData ResponseBody
	if err := json.Unmarshal(rawBody, &respData); err != nil {
		result.Status = transport.StatusError
		result.TransportStatus = transport.TransportStatusMalformedBody
		return result, nil
	}

	if len(respData.Choices) == 0 {
		result.Status = transport.StatusError
		result.TransportStatus = transport.TransportStatusMalformedBody
		return result, nil
	}

	textContent := respData.Choices[0].Message.Content
	scrubbedText, _ := transport.Scrub(textContent, resolvedSecrets)
	result.Text = scrubbedText

	result.ReportedModel = respData.Model

	finishReason := respData.Choices[0].FinishReason
	result.NativeFinish = finishReason

	result.TransportStatus = transport.TransportStatusOK

	switch finishReason {
	case "stop":
		result.Status = transport.StatusComplete
	case "length":
		result.Status = transport.StatusTruncated
	case "content_filter":
		result.Status = transport.StatusRefused
	default:
		result.Status = transport.StatusError
	}

	if respData.Usage != nil {
		result.Usage = &transport.Usage{
			Prompt:     respData.Usage.PromptTokens,
			Completion: respData.Usage.CompletionTokens,
		}
		a.mu.Lock()
		a.lastUsage = &transport.Usage{
			Prompt:     result.Usage.Prompt,
			Completion: result.Usage.Completion,
		}
		a.mu.Unlock()
	}

	return result, nil
}

func (a *Adapter) Cancel(ctx context.Context, attemptID string) (transport.CancelStatus, error) {
	a.mu.Lock()
	req, found := a.inFlightReqs[attemptID]
	a.mu.Unlock()

	status := transport.CancelStatus{
		Requested:       found,
		ServerConfirmed: transport.TriUnknown,
	}

	if found {
		req.cancel()
	}

	return status, nil
}

func (a *Adapter) Usage(ctx context.Context) (*transport.Usage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.lastUsage == nil {
		return nil, transport.ErrUsageUnavailable
	}

	return &transport.Usage{
		Prompt:     a.lastUsage.Prompt,
		Completion: a.lastUsage.Completion,
	}, nil
}

type MessageInput struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ResponseBody struct {
	Model   string       `json:"model"`
	Choices []ChoiceBody `json:"choices"`
	Usage   *UsageData   `json:"usage"`
}

type ChoiceBody struct {
	Message      ChoiceMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type ChoiceMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type UsageData struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

func buildRequestBody(req transport.CompleteRequest, profile *config.EndpointProfile) map[string]any {
	messages := make([]MessageInput, len(req.Messages))
	for i, msg := range req.Messages {
		messages[i] = MessageInput{
			Role:    msg.Role,
			Content: msg.Content,
		}
	}

	maxTokensField := profile.MaxTokensField
	if maxTokensField == "" {
		maxTokensField = "max_tokens"
	}

	body := map[string]any{
		"model":    req.Model,
		"messages": messages,
		"stream":   false,
	}
	body[maxTokensField] = req.MaxOutputTokens

	return body
}

func isDeadlineExceededErr(err error) bool {
	if err == context.DeadlineExceeded {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	type timeoutError interface {
		Timeout() bool
	}
	if te, ok := err.(timeoutError); ok && te.Timeout() {
		return true
	}
	return false
}

func isConnectionResetErr(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF {
		return true
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	errStr := err.Error()
	if bytes.Contains([]byte(errStr), []byte("connection reset")) ||
		bytes.Contains([]byte(errStr), []byte("broken pipe")) ||
		bytes.Contains([]byte(errStr), []byte("EOF")) {
		return true
	}
	return false
}

func isConnectionRefusedOrDNSErr(err error) bool {
	if err == nil {
		return false
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Op == "dial" && opErr.Err != nil {
			var errno syscall.Errno
			if errors.As(opErr.Err, &errno) && errno == syscall.ECONNREFUSED {
				return true
			}
		}
	}

	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

func (a *Adapter) normalizeFinishReason(reason string) transport.CompletionStatus {
	switch reason {
	case "stop":
		return transport.StatusComplete
	case "length":
		return transport.StatusTruncated
	case "content_filter":
		return transport.StatusRefused
	default:
		return transport.StatusError
	}
}

func (a *Adapter) probeModels(ctx context.Context, baseURL *url.URL, record *transport.CapabilityRecord, secrets []string, rawSteps map[string]any, timeout time.Duration) error {
	modelsURL := baseURL.JoinPath("/models")

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, "GET", modelsURL.String(), nil)
	if err != nil {
		return err
	}

	a.addAuthHeaders(httpReq)

	httpResp, err := a.client.Do(httpReq)
	if err != nil {
		if isConnectionRefusedOrDNSErr(err) {
			record.ModelsEndpoint = transport.TriNo
		}
		return nil
	}
	if err := httpResp.Body.Close(); err != nil {
		return err
	}

	body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 1*1024*1024))

	if httpResp.StatusCode == 404 {
		record.ModelsEndpoint = transport.TriNo
		return nil
	}

	if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
		record.ModelsEndpoint = transport.TriYes

		var modelList map[string]any
		if err := json.Unmarshal(body, &modelList); err == nil {
			record.ModelListed = transport.TriYes
		} else {
			record.ModelListed = transport.TriNo
		}
	} else {
		record.ModelsEndpoint = transport.TriUnknown
	}

	return nil
}

func (a *Adapter) probeMaxTokens(ctx context.Context, baseURL *url.URL, record *transport.CapabilityRecord, model string, secrets []string, rawSteps map[string]any, timeout time.Duration) (error, *ResponseBody, int) {
	endpoint := baseURL.JoinPath("/chat/completions")

	var maxTokensBody *ResponseBody
	var latency int

	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		messages := []MessageInput{
			{Role: "user", Content: "test"},
		}

		requestBody := map[string]any{
			"model":    model,
			"messages": messages,
			"stream":   false,
			field:      1,
		}

		bodyBytes, _ := json.Marshal(requestBody)

		httpReq, err := http.NewRequestWithContext(reqCtx, "POST", endpoint.String(), bytes.NewReader(bodyBytes))
		if err != nil {
			return err, nil, 0
		}

		httpReq.Header.Set("Content-Type", "application/json")
		a.addAuthHeaders(httpReq)

		startTime := time.Now()
		httpResp, err := a.client.Do(httpReq)
		if err != nil {
			if isConnectionRefusedOrDNSErr(err) {
				return transport.ErrUnreachable, nil, 0
			}
			return err, nil, 0
		}

		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 1*1024*1024))
		if err := httpResp.Body.Close(); err != nil {
			return err, nil, 0
		}
		latency = int(time.Since(startTime).Milliseconds())

		if httpResp.StatusCode == 401 || httpResp.StatusCode == 403 {
			record.AuthMode = "unknown"
			return transport.ErrUnauthorized, nil, latency
		}

		if httpResp.StatusCode == 404 {
			return transport.ErrModelUnavailable, nil, latency
		}

		if httpResp.StatusCode == 400 {
			if field == "max_tokens" {
				continue
			}
			return nil, nil, latency
		}

		if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
			record.MaxTokensField = field

			var respData ResponseBody
			if err := json.Unmarshal(body, &respData); err == nil {
				maxTokensBody = &respData
			}

			return nil, maxTokensBody, latency
		}
	}

	return nil, maxTokensBody, latency
}

func (a *Adapter) probeSystemRole(ctx context.Context, baseURL *url.URL, record *transport.CapabilityRecord, model string, secrets []string, rawSteps map[string]any, timeout time.Duration, finishReasons map[string]transport.CompletionStatus) error {
	endpoint := baseURL.JoinPath("/chat/completions")

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	messages := []MessageInput{
		{Role: "system", Content: "Reply with the single word PING"},
		{Role: "user", Content: "hello"},
	}

	maxTokensField := record.MaxTokensField
	if maxTokensField == "" {
		maxTokensField = "max_tokens"
	}

	requestBody := map[string]any{
		"model":        model,
		"messages":     messages,
		"stream":       false,
		maxTokensField: 10,
	}

	bodyBytes, _ := json.Marshal(requestBody)

	httpReq, err := http.NewRequestWithContext(reqCtx, "POST", endpoint.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	a.addAuthHeaders(httpReq)

	httpResp, err := a.client.Do(httpReq)
	if err != nil {
		record.SystemRoleHonored = transport.TriUnknown
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 1*1024*1024))
	if err := httpResp.Body.Close(); err != nil {
		return err
	}

	if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
		var respData ResponseBody
		if err := json.Unmarshal(body, &respData); err == nil {
			if len(respData.Choices) > 0 {
				content := respData.Choices[0].Message.Content
				if strings.Contains(content, "PING") {
					record.SystemRoleHonored = transport.TriYes
				} else {
					record.SystemRoleHonored = transport.TriNo
				}

				finishReason := respData.Choices[0].FinishReason
				if finishReason != "" {
					finishReasons[finishReason] = a.normalizeFinishReason(finishReason)
				}
			}
		}
	}

	return nil
}

func (a *Adapter) probeOverLength(ctx context.Context, baseURL *url.URL, record *transport.CapabilityRecord, model string, secrets []string, rawSteps map[string]any, timeout time.Duration, assumedContextLen int, finishReasons map[string]transport.CompletionStatus) {
	endpoint := baseURL.JoinPath("/chat/completions")

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	headMarker := fmt.Sprintf("<<<HEAD_%d>>>", time.Now().UnixNano())
	tailMarker := fmt.Sprintf("<<<TAIL_%d>>>", time.Now().UnixNano())

	promptSizeChars := (assumedContextLen * 3 / 2) * 4
	fillerSize := promptSizeChars - len(headMarker) - len(tailMarker) - len("Please respond with both markers: HEAD and TAIL. ")
	if fillerSize < 0 {
		fillerSize = 0
	}

	filler := strings.Repeat("x", fillerSize)
	promptContent := headMarker + filler + tailMarker + " Please respond with both markers: HEAD and TAIL."

	messages := []MessageInput{
		{Role: "user", Content: promptContent},
	}

	maxTokensField := record.MaxTokensField
	if maxTokensField == "" {
		maxTokensField = "max_tokens"
	}

	requestBody := map[string]any{
		"model":        model,
		"messages":     messages,
		"stream":       false,
		maxTokensField: 500,
	}

	bodyBytes, _ := json.Marshal(requestBody)

	httpReq, err := http.NewRequestWithContext(reqCtx, "POST", endpoint.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		record.OverLength = "errored"
		return
	}

	httpReq.Header.Set("Content-Type", "application/json")
	a.addAuthHeaders(httpReq)

	httpResp, err := a.client.Do(httpReq)
	if err != nil {
		record.OverLength = "errored"
		return
	}

	body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 10*1024*1024))
	if err := httpResp.Body.Close(); err != nil {
		record.OverLength = "errored"
		return
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		record.OverLength = "errored"
		return
	}

	var respData ResponseBody
	if err := json.Unmarshal(body, &respData); err != nil || len(respData.Choices) == 0 {
		record.OverLength = "errored"
		return
	}

	content := respData.Choices[0].Message.Content
	hasHeadMarker := strings.Contains(content, headMarker)
	hasTailMarker := strings.Contains(content, tailMarker)

	if hasHeadMarker && hasTailMarker {
		record.OverLength = "both_markers_returned"
	} else {
		record.OverLength = "silent_truncation_likely"
	}

	finishReason := respData.Choices[0].FinishReason
	if finishReason != "" {
		finishReasons[finishReason] = a.normalizeFinishReason(finishReason)
	}
}

func (a *Adapter) addAuthHeaders(req *http.Request) {
	if a.profile.Auth != nil {
		authVal, err := a.resolver.Resolve(*a.profile.Auth)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+authVal)
		}
	}

	for headerName, secretRef := range a.profile.ExtraHeaders {
		headerVal, err := a.resolver.Resolve(secretRef)
		if err == nil {
			req.Header.Set(headerName, headerVal)
		}
	}
}
