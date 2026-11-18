package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
)

type Adapter struct {
	profile  *config.EndpointProfile
	resolver config.SecretResolver
	client   *http.Client

	mu           sync.Mutex
	lastUsage    *transport.Usage
	inFlightReqs map[string]context.CancelFunc
	maxBodySize  int64
}

const defaultMaxBodySize = 16 * 1024 * 1024

func New(profile *config.EndpointProfile, resolver config.SecretResolver) *Adapter {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 {
				return nil
			}
			prevHost := via[len(via)-1].URL.Host
			newHost := req.URL.Host
			if prevHost != newHost {
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
		inFlightReqs: make(map[string]context.CancelFunc),
		maxBodySize:  defaultMaxBodySize,
	}
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

	reqBody := buildRequestBody(req, a.profile)
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return result, err
	}

	baseURL, err := url.Parse(a.profile.BaseURL)
	if err != nil {
		return result, err
	}

	endpoint := baseURL.JoinPath("/chat/completions")

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return result, err
	}

	httpReq.Header = headers
	httpReq.Header.Set("Content-Type", "application/json")

	a.mu.Lock()
	cancelFunc, cancel := context.WithCancel(ctx)
	a.inFlightReqs[req.Envelope.AttemptID] = cancel
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		delete(a.inFlightReqs, req.Envelope.AttemptID)
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

		if isUnreachableErr(err) {
			result.Status = transport.StatusError
			result.TransportStatus = transport.TransportStatusUnreachable
			return result, nil
		}

		result.Status = transport.StatusError
		result.TransportStatus = transport.TransportStatusUnreachable
		return result, nil
	}

	defer httpResp.Body.Close()

	limitedBody := io.LimitReader(httpResp.Body, a.maxBodySize+1)
	rawBody, err := io.ReadAll(limitedBody)
	if err != nil {
		if isConnectionResetErr(err) {
			result.Status = transport.StatusInterrupted
			result.TransportStatus = transport.TransportStatusReset
			return result, nil
		}
		result.Status = transport.StatusError
		result.TransportStatus = transport.TransportStatusUnreachable
		return result, nil
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

	if respData.Choices[0].Message.Content == "" && respData.Choices[0].FinishReason == "" {
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
		a.lastUsage = result.Usage
		a.mu.Unlock()
	}

	result.Identity = transport.NewUnknownIdentity()

	return result, nil
}

func (a *Adapter) Cancel(ctx context.Context, attemptID string) (transport.CancelStatus, error) {
	a.mu.Lock()
	cancel, found := a.inFlightReqs[attemptID]
	a.mu.Unlock()

	status := transport.CancelStatus{
		Requested:       found,
		ServerConfirmed: transport.TriUnknown,
	}

	if found {
		cancel()
	}

	return status, nil
}

func (a *Adapter) Usage(ctx context.Context) (*transport.Usage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.lastUsage == nil {
		return nil, transport.ErrUsageUnavailable
	}

	return a.lastUsage, nil
}

type RequestBody struct {
	Model               string         `json:"model"`
	Messages            []MessageInput `json:"messages"`
	Stream              bool           `json:"stream"`
	MaxTokens           int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens int            `json:"max_completion_tokens,omitempty"`
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

func buildRequestBody(req transport.CompleteRequest, profile *config.EndpointProfile) RequestBody {
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

	body := RequestBody{
		Model:    req.Model,
		Messages: messages,
		Stream:   false,
	}

	if maxTokensField == "max_tokens" {
		body.MaxTokens = req.MaxOutputTokens
	} else {
		body.MaxCompletionTokens = req.MaxOutputTokens
	}

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

func isUnreachableErr(err error) bool {
	if err == nil {
		return false
	}
	if bytes.Contains([]byte(err.Error()), []byte("connection refused")) ||
		bytes.Contains([]byte(err.Error()), []byte("no such host")) ||
		bytes.Contains([]byte(err.Error()), []byte("dial tcp")) ||
		bytes.Contains([]byte(err.Error()), []byte("network is unreachable")) {
		return true
	}

	return false
}
