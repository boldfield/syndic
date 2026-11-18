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
	"sync"
	"syscall"

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
	profileHost := baseURL.Host

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

func (a *Adapter) Probe(ctx context.Context, endpoint, model string) (transport.CapabilityRecord, error) {
	return transport.CapabilityRecord{}, transport.ErrNotImplemented
}

func (a *Adapter) Complete(ctx context.Context, req transport.CompleteRequest) (transport.CompleteResult, error) {
	result := transport.CompleteResult{
		Envelope: req.Envelope,
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

	result.Identity = transport.NewUnknownIdentity()

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
