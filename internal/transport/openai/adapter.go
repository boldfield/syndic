package openai

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"unicode/utf8"

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

// AdapterVersion identifies this adapter in capability records.
const AdapterVersion = "openai-v1"

const defaultMaxBodySize = 16 * 1024 * 1024

// calibrationBaselineNonEmpty is the fallback baseline content, sent only when
// the endpoint refuses an empty message.
const calibrationBaselineNonEmpty = "."

// calibrationText is a fixed 4096-byte text used for token calibration.
// It contains a mix of English prose, source code, and multi-byte UTF-8 characters.
const calibrationText = `The quick brown fox jumps over the lazy dog. Pack my box with five dozen liquor jugs.
How vexingly quick daft zebras jump! Sphinx of black quartz, judge my vow.

func fibonacci(n int) int {
	if n <= 1 {
		return n
	}
	return fibonacci(n-1) + fibonacci(n-2)
}

class HelloWorld {
	public static void main(String[] args) {
		System.out.println("Hello, World!");
		System.out.println("The number of arguments: " + args.length);
	}
}

SELECT user_id, COUNT(*) as count FROM events GROUP BY user_id ORDER BY count DESC LIMIT 10;
INSERT INTO users (name, email) VALUES ('Alice Johnson', 'alice@example.com');
UPDATE products SET price = price * 1.1 WHERE category = 'electronics';
DELETE FROM logs WHERE created_at < NOW() - INTERVAL '30 days';

Document processing in natural language requires careful tokenization.
The tokenizer must handle punctuation, whitespace, and special characters correctly.
Unicode normalization ensures consistent representation across different platforms.

Multi-byte UTF-8 characters: café, naïve, résumé, cliché, façade, élève, 🚀, 🎉, 🌟, ✨, ⚡.

Configuration example:
server:
  host: localhost
  port: 8080
  ssl: true
  timeout: 30s
database:
  url: postgresql://user:pass@localhost:5432/mydb
  pool_size: 20
  max_connections: 100

The Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed do eiusmod tempor incididunt
ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud exercitation ullamco
laboris nisi ut aliquip ex ea commodo consequat.

API Response Example:
{
  "status": "success",
  "data": {
    "id": "12345",
    "name": "John Doe",
    "email": "john@example.com",
    "roles": ["admin", "user"],
    "last_login": "2024-11-18T10:30:00Z"
  },
  "timestamp": "2024-11-18T10:35:45Z"
}

Performance considerations for token estimation:
- Accurate tokenization is essential for cost estimation
- Different models use different tokenizers
- UTF-8 multi-byte characters affect token counts
- Whitespace handling varies by tokenizer implementation

Error handling pattern:
try {
	const result = await fetchData(url);
	processResult(result);
} catch (error) {
	logger.error('Request failed', { error, url });
	handleError(error);
} finally {
	cleanup();
}

The evolution of programming languages has been marked by increasing abstraction levels.
Assembly gave way to procedural languages, which gave way to object-oriented design.
Now we see functional programming paradigms gaining prominence in modern development.

Mathematical expressions: x² + y² = r², ∫₀^∞ e^(-x²) dx = √π/2, ∮ F·ds = ∬(∇×F)·dA

Network protocol overview:
TCP/IP provides reliable, ordered delivery of data.
UDP offers faster, connectionless communication without guarantees.
QUIC combines advantages of both TCP and UDP.

International characters: 日本語, 中文, 한국어, Русский, العربية, עברית, Ελληνικά.

Code review checklist:
✓ All tests pass
✓ Code follows style guidelines
✓ Documentation is updated
✓ No hardcoded secrets or passwords
✓ Performance impact assessed
✓ Backward compatibility maintained

Another paragraph about the importance of proper encoding and character handling.
The transition from ASCII to Unicode was a major milestone in computing history.
Today, UTF-8 has become the dominant encoding for web and application development.

Regular expression patterns:
Email: ^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$
Phone: ^\+?1?\d{9,15}$
URL: ^https?://[^\s/$.?#].[^\s]*$

The future of artificial intelligence includes better tokenization methods that can handle
complex multilingual text more efficiently. This will improve both accuracy and performance
in natural language processing applications across diverse languages and scripts.

Summary: This calibration text is designed for token calibration. It includes code, prose,
UTF-8 chars, and mathematical expressions to represent typical API documentation content used
in natural language processing for token estimation and cost prediction accuracy assurance. 123456789012345678901234567890xyz`

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

func (a *Adapter) Probe(ctx context.Context, endpoint, model string, opts transport.ProbeOptions) (transport.CapabilityRecord, error) {
	return a.ProbeWithOptions(ctx, endpoint, model, opts)
}

func (a *Adapter) ProbeWithOptions(ctx context.Context, endpoint, model string, opts transport.ProbeOptions) (transport.CapabilityRecord, error) {
	if endpoint != a.profile.Name {
		return transport.CapabilityRecord{}, fmt.Errorf("endpoint %q does not match profile name %q", endpoint, a.profile.Name)
	}

	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
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
		ToolCalls:                transport.TriUnknown,
		MaxTokensField:           "",
		FinishReasons:            make(map[string]transport.CompletionStatus),
		OverLength:               "not_run:skipped",
		OverLengthClassifier:     overLengthClassificationRule,
		LatencyMillis:            nil,
		ToolRestrictions:         []string{},
		ServerVersion:            "unknown",
		AdapterVersion:           AdapterVersion,
		ObservedAt:               time.Now(),
		Raw:                      json.RawMessage("{}"),
	}
	var serverVersion string

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

	if err := a.probeModels(probeCtx, baseURL, &record, &resolvedSecrets, rawSteps, opts); err != nil {
		return record, stepError(err, rawSteps, "models")
	}

	step2Body, step2Latency, step2ServerVersion, step2Err := a.probeMaxTokens(probeCtx, baseURL, &record, model, &resolvedSecrets, rawSteps, opts)
	if step2ServerVersion != "" {
		serverVersion = step2ServerVersion
	}

	if errors.Is(step2Err, transport.ErrNotSent) || errors.Is(step2Err, transport.ErrUncertain) || errors.Is(step2Err, transport.ErrUnreachable) || errors.Is(step2Err, transport.ErrUnauthorized) || errors.Is(step2Err, transport.ErrModelUnavailable) {
		if err := scrubAndStore(&record, rawSteps, resolvedSecrets); err != nil {
			return record, err
		}
		return record, stepError(step2Err, rawSteps, "max_completion_tokens", "max_tokens")
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
				probeFinishReasons[finishReason] = mapFinishReason(finishReason)
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

	if err := a.probeSystemRole(probeCtx, baseURL, &record, model, &resolvedSecrets, rawSteps, opts, probeFinishReasons); err != nil {
		if err := scrubAndStore(&record, rawSteps, resolvedSecrets); err != nil {
			return record, err
		}
		return record, stepError(err, rawSteps, "system_role")
	}

	if err := a.probeToolCalls(probeCtx, baseURL, &record, model, &resolvedSecrets, rawSteps, opts, probeFinishReasons); err != nil {
		if err := scrubAndStore(&record, rawSteps, resolvedSecrets); err != nil {
			return record, err
		}
		return record, stepError(err, rawSteps, "tool_calls")
	}

	if !opts.DisableCalibration {
		if err := a.probeCalibration(probeCtx, baseURL, &record, model, &resolvedSecrets, rawSteps, opts); err != nil {
			if err := scrubAndStore(&record, rawSteps, resolvedSecrets); err != nil {
				return record, err
			}
			return record, stepError(err, rawSteps, "calibration", "calibration_baseline_nonempty", "calibration_baseline")
		}
	}

	if !opts.DisableOverLength {
		if err := a.probeOverLength(probeCtx, baseURL, &record, model, &resolvedSecrets, rawSteps, opts); err != nil {
			if err := scrubAndStore(&record, rawSteps, resolvedSecrets); err != nil {
				return record, err
			}
			return record, stepError(err, rawSteps, "over_length")
		}
	}

	if len(probeFinishReasons) > 0 {
		record.FinishReasons = probeFinishReasons
	}

	if len(record.FinishReasons) == 0 {
		record.FinishReasons = make(map[string]transport.CompletionStatus)
	}

	if record.OverLength == "rejected:context" {
		record.EligibleRequiredReviewer = transport.TriYes
	}

	if serverVersion != "" {
		record.ServerVersion = serverVersion
	}

	// An incomplete probe is a failed probe per design v5 delta 1a.
	// A probe that captures no reported model, or finds no working max-tokens field,
	// writes its raw record and export for diagnosis but no usable journal record.
	if record.ReportedModel == "" || record.MaxTokensField == "" {
		if err := scrubAndStore(&record, rawSteps, resolvedSecrets); err != nil {
			return record, err
		}
		return record, fmt.Errorf("%w: missing ReportedModel or MaxTokensField", transport.ErrIncompleteProbe)
	}

	if err := scrubAndStore(&record, rawSteps, resolvedSecrets); err != nil {
		return record, err
	}

	return record, nil
}

// stepError tags a probe phase's failure with the step it failed on and that
// step's recorded HTTP status. steps lists the phase's raw-evidence step names,
// preferred first; the first one recorded is the step reported. An error that
// already carries a step is returned unchanged.
func stepError(err error, rawSteps map[string]any, steps ...string) error {
	var pe *transport.ProbeStepError
	if errors.As(err, &pe) {
		return err
	}
	step := steps[0]
	var status int
	for _, name := range steps {
		if entry, ok := rawSteps[name].(map[string]any); ok {
			step = name
			status, _ = entry["status"].(int)
			break
		}
	}
	return &transport.ProbeStepError{Step: step, Status: status, Err: err}
}

// uncertainErr marks a failure after the request was handed to the transport:
// the server may or may not have processed it, so it is never ErrNotSent.
func uncertainErr(err error) error {
	if errors.Is(err, transport.ErrUncertain) {
		return err
	}
	return fmt.Errorf("%w: %w", transport.ErrUncertain, err)
}

// doProbe sends one probe request. The caller accounts the initial request
// through opts.BeforeSend; every redirect the client follows is a further
// network request, so doProbe accounts it here before it is sent. Once a
// redirect has been followed the initial request has left the process, so any
// failure, including a BeforeSend failure for the redirect, is ErrUncertain and
// never ErrNotSent or a dial error.
func (a *Adapter) doProbe(httpReq *http.Request, opts transport.ProbeOptions, preq transport.ProbeRequest) (*http.Response, error) {
	client := *a.client
	policy := a.client.CheckRedirect
	redirected := false
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if policy != nil {
			if err := policy(req, via); err != nil {
				return err
			}
		}
		redirected = true
		if opts.BeforeSend != nil {
			hop := preq
			hop.Redirect = len(via)
			if err := opts.BeforeSend(&hop); err != nil {
				return err
			}
		}
		return nil
	}

	httpResp, err := client.Do(httpReq)
	if err != nil && redirected {
		return nil, fmt.Errorf("%w: redirected probe request: %v", transport.ErrUncertain, err)
	}
	return httpResp, err
}

func recordRawStep(rawSteps map[string]any, step, path string, statusCode int, headers http.Header, body []byte) {
	entry := map[string]any{
		"path": path,
	}
	if statusCode != 0 {
		entry["status"] = statusCode
		if headers != nil {
			entry["headers"] = headers
		}
		if body != nil {
			entry["body"] = string(body)
		}
	}
	rawSteps[step] = entry
}

func recordRawStepWithRequest(rawSteps map[string]any, step, path string, statusCode int, headers http.Header, body []byte, requestBody []byte) {
	entry := map[string]any{
		"path": path,
	}
	if requestBody != nil {
		entry["request"] = string(requestBody)
	}
	if statusCode != 0 {
		entry["status"] = statusCode
		if headers != nil {
			entry["headers"] = headers
		}
		if body != nil {
			entry["body"] = string(body)
		}
	}
	rawSteps[step] = entry
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
		if msg.Role != "system" && msg.Role != "user" && msg.Role != "assistant" && msg.Role != "tool" {
			return result, fmt.Errorf("%w: invalid message role: %s", transport.ErrNotSent, msg.Role)
		}
	}

	var resolvedSecrets []string
	headers := make(http.Header)

	if a.profile.Auth != nil {
		authVal, err := a.resolver.Resolve(*a.profile.Auth)
		if err != nil {
			return result, fmt.Errorf("%w: %w", transport.ErrNotSent, err)
		}
		resolvedSecrets = append(resolvedSecrets, authVal)
		headers.Set("Authorization", "Bearer "+authVal)
	}

	for headerName, secretRef := range a.profile.ExtraHeaders {
		headerVal, err := a.resolver.Resolve(secretRef)
		if err != nil {
			return result, fmt.Errorf("%w: %w", transport.ErrNotSent, err)
		}
		resolvedSecrets = append(resolvedSecrets, headerVal)
		headers.Set(headerName, headerVal)
	}

	reqBody := buildRequestBody(req, &a.profile)
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return result, fmt.Errorf("%w: %w", transport.ErrNotSent, err)
	}

	baseURL, err := url.Parse(a.profile.BaseURL)
	if err != nil {
		return result, fmt.Errorf("%w: %w", transport.ErrNotSent, err)
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
		return result, fmt.Errorf("%w: %w", transport.ErrNotSent, err)
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

	if req.BeforeSend != nil {
		if err := req.BeforeSend(&req); err != nil {
			result.TransportStatus = "not_sent"
			return result, fmt.Errorf("%w: %w", transport.ErrNotSent, err)
		}
	}

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

	result.Status = mapFinishReason(finishReason)

	// Extract tool_calls from the response if present
	if len(respData.Choices[0].Message.ToolCalls) > 0 {
		result.ToolCalls = make([]transport.ToolCall, len(respData.Choices[0].Message.ToolCalls))
		for i, toolCall := range respData.Choices[0].Message.ToolCalls {
			scrubbedArgs, _ := transport.Scrub(toolCall.Function.Arguments, resolvedSecrets)
			result.ToolCalls[i] = transport.ToolCall{
				ID:   toolCall.ID,
				Type: toolCall.Type,
				Function: transport.ToolCallFunction{
					Name:      toolCall.Function.Name,
					Arguments: scrubbedArgs,
				},
			}
		}
	}

	if respData.Usage != nil {
		result.Usage = &transport.Usage{}
		if respData.Usage.PromptTokens != nil {
			result.Usage.Prompt = *respData.Usage.PromptTokens
		}
		if respData.Usage.CompletionTokens != nil {
			result.Usage.Completion = *respData.Usage.CompletionTokens
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
	Role      string             `json:"role"`
	Content   string             `json:"content"`
	ToolCalls []ResponseToolCall `json:"tool_calls"`
}

type ResponseToolCall struct {
	ID       string                   `json:"id"`
	Type     string                   `json:"type"`
	Function ResponseToolCallFunction `json:"function"`
}

type ResponseToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type UsageData struct {
	PromptTokens     *int `json:"prompt_tokens"`
	CompletionTokens *int `json:"completion_tokens"`
}

func buildRequestBody(req transport.CompleteRequest, profile *config.EndpointProfile) map[string]any {
	messages := make([]map[string]any, len(req.Messages))
	for i, msg := range req.Messages {
		msgMap := map[string]any{
			"role": msg.Role,
		}

		// Content is included for user, assistant, and tool messages
		if msg.Content != "" {
			msgMap["content"] = msg.Content
		}

		// Add tool_calls for assistant messages that have them
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			toolCalls := make([]map[string]any, len(msg.ToolCalls))
			for j, call := range msg.ToolCalls {
				toolCalls[j] = map[string]any{
					"id":   call.ID,
					"type": call.Type,
					"function": map[string]any{
						"name":      call.Function.Name,
						"arguments": call.Function.Arguments,
					},
				}
			}
			msgMap["tool_calls"] = toolCalls
		}

		// Add tool_call_id for tool messages
		if msg.Role == "tool" && msg.ToolCallID != "" {
			msgMap["tool_call_id"] = msg.ToolCallID
		}

		messages[i] = msgMap
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

	// Add tools to the request if present
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, tool := range req.Tools {
			tools[i] = map[string]any{
				"type": tool.Type,
				"function": map[string]any{
					"name":        tool.Function.Name,
					"description": tool.Function.Description,
					"parameters":  tool.Function.Parameters,
				},
			}
		}
		body["tools"] = tools
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

// mapFinishReason maps a native finish_reason to a completion status. It is
// shared by Complete and Probe so the two cannot drift.
func mapFinishReason(reason string) transport.CompletionStatus {
	switch reason {
	case "stop":
		return transport.StatusComplete
	case "length":
		return transport.StatusTruncated
	case "content_filter":
		return transport.StatusRefused
	case "tool_calls":
		return transport.StatusToolCalls
	default:
		return transport.StatusError
	}
}

func (a *Adapter) probeModels(ctx context.Context, baseURL *url.URL, record *transport.CapabilityRecord, secrets *[]string, rawSteps map[string]any, opts transport.ProbeOptions) error {
	modelsURL := baseURL.JoinPath("/models")

	reqCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, "GET", modelsURL.String(), nil)
	if err != nil {
		return err
	}

	if err := a.addProbeAuth(httpReq, secrets); err != nil {
		return err
	}

	if opts.BeforeSend != nil {
		if err := opts.BeforeSend(&transport.ProbeRequest{
			Model:   record.Model,
			Step:    "models",
			Timeout: opts.Timeout,
		}); err != nil {
			return fmt.Errorf("%w: %w", transport.ErrNotSent, err)
		}
	}

	httpResp, err := a.doProbe(httpReq, opts, transport.ProbeRequest{Model: record.Model, Step: "models", Timeout: opts.Timeout})
	if err != nil {
		if isConnectionRefusedOrDNSErr(err) {
			recordRawStep(rawSteps, "models", "/models", 0, nil, nil)
			return nil
		}
		return uncertainErr(err)
	}

	body, readErr := io.ReadAll(io.LimitReader(httpResp.Body, 1*1024*1024))
	closeErr := httpResp.Body.Close()
	if readErr != nil {
		return uncertainErr(readErr)
	}
	if closeErr != nil {
		return uncertainErr(closeErr)
	}

	recordRawStep(rawSteps, "models", "/models", httpResp.StatusCode, httpResp.Header, body)

	if httpResp.StatusCode == 404 {
		record.ModelsEndpoint = transport.TriNo
		return nil
	}

	if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
		var modelResp map[string]any
		if err := json.Unmarshal(body, &modelResp); err != nil {
			return nil
		}

		models, ok := modelResp["data"].([]any)
		if !ok {
			return nil
		}

		record.ModelsEndpoint = transport.TriYes

		modelFound := false
		for _, m := range models {
			if modelMap, ok := m.(map[string]any); ok {
				if id, ok := modelMap["id"]; ok && id == record.Model {
					modelFound = true
					break
				}
			}
		}
		if modelFound {
			record.ModelListed = transport.TriYes
		} else {
			record.ModelListed = transport.TriNo
		}
	}

	return nil
}

const (
	// probeOutputLimit is the output limit the probe sends; some gateways
	// refuse a limit of 1.
	probeOutputLimit = 16
	// probeRetryOutputLimit is the limit sent once when probeOutputLimit is
	// reported as too small.
	probeRetryOutputLimit = 256
)

// outputLimitTooSmallPhrase is matched case-insensitively against a 400 body.
const outputLimitTooSmallPhrase = "output limit was reached"

// isOutputLimitTooSmall reports whether a response says the request's output
// limit was too small (as opposed to the limit field being unsupported).
func isOutputLimitTooSmall(status int, body []byte) bool {
	return status == http.StatusBadRequest &&
		strings.Contains(strings.ToLower(string(body)), outputLimitTooSmallPhrase)
}

func (a *Adapter) probeMaxTokens(ctx context.Context, baseURL *url.URL, record *transport.CapabilityRecord, model string, secrets *[]string, rawSteps map[string]any, opts transport.ProbeOptions) (*ResponseBody, int, string, error) {
	endpoint := baseURL.JoinPath("/chat/completions")

	var latency int
	var serverVersion string

	// send issues one request for field at limit and records it as stepName.
	send := func(field, stepName string, limit int) (*http.Response, []byte, error) {
		reqCtx, cancel := context.WithTimeout(ctx, opts.Timeout)

		requestBody := map[string]any{
			"model":    model,
			"messages": []MessageInput{{Role: "user", Content: "test"}},
			"stream":   false,
			field:      limit,
		}

		bodyBytes, _ := json.Marshal(requestBody)

		httpReq, err := http.NewRequestWithContext(reqCtx, "POST", endpoint.String(), bytes.NewReader(bodyBytes))
		if err != nil {
			cancel()
			return nil, nil, &transport.ProbeStepError{Step: stepName, Err: err}
		}

		httpReq.Header.Set("Content-Type", "application/json")
		if err := a.addProbeAuth(httpReq, secrets); err != nil {
			cancel()
			return nil, nil, &transport.ProbeStepError{Step: stepName, Err: err}
		}

		if opts.BeforeSend != nil {
			if err := opts.BeforeSend(&transport.ProbeRequest{
				Model:   model,
				Step:    stepName,
				Timeout: opts.Timeout,
			}); err != nil {
				cancel()
				return nil, nil, &transport.ProbeStepError{Step: stepName, Err: fmt.Errorf("%w: %w", transport.ErrNotSent, err)}
			}
		}

		startTime := time.Now()
		httpResp, err := a.doProbe(httpReq, opts, transport.ProbeRequest{Model: model, Step: stepName, Timeout: opts.Timeout})
		if err != nil {
			cancel()
			if isConnectionRefusedOrDNSErr(err) {
				recordRawStepWithRequest(rawSteps, stepName, "/chat/completions", 0, nil, nil, bodyBytes)
				return nil, nil, &transport.ProbeStepError{Step: stepName, Err: transport.ErrUnreachable}
			}
			return nil, nil, &transport.ProbeStepError{Step: stepName, Err: uncertainErr(err)}
		}

		body, readErr := io.ReadAll(io.LimitReader(httpResp.Body, 1*1024*1024))
		closeErr := httpResp.Body.Close()
		cancel()
		if readErr != nil {
			return nil, nil, &transport.ProbeStepError{Step: stepName, Err: uncertainErr(readErr)}
		}
		if closeErr != nil {
			return nil, nil, &transport.ProbeStepError{Step: stepName, Err: uncertainErr(closeErr)}
		}
		latency = int(time.Since(startTime).Milliseconds())

		recordRawStepWithRequest(rawSteps, stepName, "/chat/completions", httpResp.StatusCode, httpResp.Header, body, bodyBytes)

		if serverVersion == "" && httpResp.Header.Get("Server") != "" {
			serverVersion = httpResp.Header.Get("Server")
		}
		return httpResp, body, nil
	}

	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		step := field
		httpResp, body, err := send(field, step, probeOutputLimit)
		if err != nil {
			return nil, latency, serverVersion, err
		}

		if isOutputLimitTooSmall(httpResp.StatusCode, body) {
			step = field + "_retry"
			httpResp, body, err = send(field, step, probeRetryOutputLimit)
			if err != nil {
				return nil, latency, serverVersion, err
			}
		}

		if httpResp.StatusCode == 401 || httpResp.StatusCode == 403 {
			record.AuthMode = "unknown"
			return nil, latency, serverVersion, &transport.ProbeStepError{Step: step, Status: httpResp.StatusCode, Err: transport.ErrUnauthorized}
		}

		if httpResp.StatusCode == 404 {
			return nil, latency, serverVersion, &transport.ProbeStepError{Step: step, Status: httpResp.StatusCode, Err: transport.ErrModelUnavailable}
		}

		if httpResp.StatusCode == 400 {
			if field == "max_tokens" {
				continue
			}
			return nil, latency, serverVersion, nil
		}

		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			// Only a 400 triggers the max_completion_tokens follow-up.
			return nil, latency, serverVersion, nil
		}

		record.MaxTokensField = field

		var respData ResponseBody
		if err := json.Unmarshal(body, &respData); err == nil {
			return &respData, latency, serverVersion, nil
		}
		return nil, latency, serverVersion, nil
	}

	return nil, latency, serverVersion, nil
}

func (a *Adapter) probeSystemRole(ctx context.Context, baseURL *url.URL, record *transport.CapabilityRecord, model string, secrets *[]string, rawSteps map[string]any, opts transport.ProbeOptions, finishReasons map[string]transport.CompletionStatus) error {
	endpoint := baseURL.JoinPath("/chat/completions")

	reqCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
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
	if err := a.addProbeAuth(httpReq, secrets); err != nil {
		return err
	}

	if opts.BeforeSend != nil {
		if err := opts.BeforeSend(&transport.ProbeRequest{
			Model:   model,
			Step:    "system_role",
			Timeout: opts.Timeout,
		}); err != nil {
			return fmt.Errorf("%w: %w", transport.ErrNotSent, err)
		}
	}

	httpResp, err := a.doProbe(httpReq, opts, transport.ProbeRequest{Model: model, Step: "system_role", Timeout: opts.Timeout})
	if err != nil {
		record.SystemRoleHonored = transport.TriUnknown
		return uncertainErr(err)
	}

	body, readErr := io.ReadAll(io.LimitReader(httpResp.Body, 1*1024*1024))
	closeErr := httpResp.Body.Close()
	if readErr != nil {
		return uncertainErr(readErr)
	}
	if closeErr != nil {
		return uncertainErr(closeErr)
	}

	recordRawStep(rawSteps, "system_role", "/chat/completions", httpResp.StatusCode, httpResp.Header, body)

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
					finishReasons[finishReason] = mapFinishReason(finishReason)
				}
			}
		}
	}

	return nil
}

// contextLengthPhrases are the lower-case phrases that mark a 400 or 413 body as an explicit
// context-length or token-limit refusal. overLengthClassificationRule is built from it.
var contextLengthPhrases = []string{
	"context_length_exceeded",
	"maximum context length",
	"token limit exceeded",
	"prompt is too long",
	"input is too long",
	"context window",
	"context length",
	"too many tokens",
	"exceeds the maximum number of tokens",
	"maximum input tokens",
}

var overLengthClassificationRule = "status 400 or 413 with error code or type context_length_exceeded, or body containing one of: " +
	strings.Join(contextLengthPhrases, ", ") +
	" (case-insensitive) → rejected:context; status 401/403/404/429 or 5xx → rejected:other; other 400/413 → rejected:other; 2xx → accepted_whole or markers_missing"

const overLengthDetailMax = 200

// errorBodyFields holds the JSON fields of an error body that classification and the detail read.
type errorBodyFields struct {
	Code    any `json:"code"`
	Type    any `json:"type"`
	Message any `json:"message"`
	Error   *struct {
		Code    any `json:"code"`
		Type    any `json:"type"`
		Message any `json:"message"`
	} `json:"error"`
}

func parseErrorBody(body []byte) errorBodyFields {
	var f errorBodyFields
	// A body that is not a JSON object (or whose error field is a string) leaves f partly or wholly empty.
	if err := json.Unmarshal(body, &f); err != nil {
		var loose struct {
			Code    any `json:"code"`
			Type    any `json:"type"`
			Message any `json:"message"`
		}
		if json.Unmarshal(body, &loose) == nil {
			f.Code, f.Type, f.Message = loose.Code, loose.Type, loose.Message
		}
	}
	return f
}

func isContextLengthCode(v any) bool {
	s, ok := v.(string)
	return ok && strings.EqualFold(strings.TrimSpace(s), "context_length_exceeded")
}

// classifyRejectionError classifies an HTTP error response and returns both the classification and the rule.
// Context-length errors are status 400/413 with an explicit context-length error code or phrase.
// Returns (classification, rule).
func classifyRejectionError(statusCode int, body []byte) (string, string) {
	if statusCode == 401 || statusCode == 403 {
		return "rejected:other", "authentication error"
	}
	if statusCode == 404 || statusCode == 429 {
		return "rejected:other", "not found or rate limited"
	}
	if statusCode == 400 || statusCode == 413 {
		f := parseErrorBody(body)
		if isContextLengthCode(f.Code) || isContextLengthCode(f.Type) ||
			(f.Error != nil && (isContextLengthCode(f.Error.Code) || isContextLengthCode(f.Error.Type))) {
			return "rejected:context", "explicit context-length or token-limit signal"
		}
		bodyStr := strings.ToLower(string(body))
		for _, phrase := range contextLengthPhrases {
			if strings.Contains(bodyStr, phrase) {
				return "rejected:context", "explicit context-length or token-limit signal"
			}
		}
		return "rejected:other", "400 or 413 without explicit context-length signal"
	}
	if statusCode >= 500 && statusCode < 600 {
		return "rejected:other", "server error"
	}
	return "rejected:other", "unknown error"
}

// rejectionDetail extracts the server's message from an error body: error.message, then message,
// then the body. The result is not truncated: the caller scrubs it first, then cuts it.
func rejectionDetail(body []byte) string {
	f := parseErrorBody(body)
	msg := ""
	if f.Error != nil {
		if m, ok := f.Error.Message.(string); ok {
			msg = m
		}
	}
	if msg == "" {
		if m, ok := f.Message.(string); ok {
			msg = m
		}
	}
	if msg == "" {
		msg = string(body)
	}
	return strings.TrimSpace(msg)
}

func truncateUTF8(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func (a *Adapter) probeToolCalls(ctx context.Context, baseURL *url.URL, record *transport.CapabilityRecord, model string, secrets *[]string, rawSteps map[string]any, opts transport.ProbeOptions, finishReasons map[string]transport.CompletionStatus) error {
	endpoint := baseURL.JoinPath("/chat/completions")

	reqCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	// Use a simple harmless tool for the probe.
	// The request forces the model to call the tool by setting
	// tool_choice to require a call, ensuring we get definitive yes/no results
	// rather than a model that skips optional tool calls.
	messages := []MessageInput{
		{Role: "user", Content: "Call the probe tool with the message PROBE."},
	}

	maxTokensField := record.MaxTokensField
	if maxTokensField == "" {
		maxTokensField = "max_tokens"
	}

	// Define one harmless tool for probing
	tools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "probe_tool",
				"description": "A harmless probe tool for testing tool_calls support",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"message": map[string]any{
							"type":        "string",
							"description": "A message to echo",
						},
					},
				},
			},
		},
	}

	requestBody := map[string]any{
		"model":        model,
		"messages":     messages,
		"stream":       false,
		"tools":        tools,
		"tool_choice":  map[string]any{"type": "function", "function": map[string]any{"name": "probe_tool"}},
		maxTokensField: 50,
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return &transport.ProbeStepError{Step: "tool_calls", Err: uncertainErr(err)}
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, "POST", endpoint.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return &transport.ProbeStepError{Step: "tool_calls", Err: uncertainErr(err)}
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if err := a.addProbeAuth(httpReq, secrets); err != nil {
		return err
	}

	if opts.BeforeSend != nil {
		if err := opts.BeforeSend(&transport.ProbeRequest{
			Model:   model,
			Step:    "tool_calls",
			Timeout: opts.Timeout,
		}); err != nil {
			return fmt.Errorf("%w: %w", transport.ErrNotSent, err)
		}
	}

	httpResp, err := a.doProbe(httpReq, opts, transport.ProbeRequest{Model: model, Step: "tool_calls", Timeout: opts.Timeout})
	if err != nil {
		record.ToolCalls = transport.TriUnknown
		return uncertainErr(err)
	}

	body, readErr := io.ReadAll(io.LimitReader(httpResp.Body, 1*1024*1024))
	closeErr := httpResp.Body.Close()
	if readErr != nil {
		return uncertainErr(readErr)
	}
	if closeErr != nil {
		return uncertainErr(closeErr)
	}

	recordRawStep(rawSteps, "tool_calls", "/chat/completions", httpResp.StatusCode, httpResp.Header, body)

	if httpResp.StatusCode == 401 || httpResp.StatusCode == 403 {
		record.ToolCalls = transport.TriUnknown
		return nil
	}

	if httpResp.StatusCode == 404 {
		record.ToolCalls = transport.TriUnknown
		return nil
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		record.ToolCalls = transport.TriUnknown
		return nil
	}

	var respData ResponseBody
	if err := json.Unmarshal(body, &respData); err != nil {
		record.ToolCalls = transport.TriUnknown
		return nil
	}

	if len(respData.Choices) == 0 {
		record.ToolCalls = transport.TriUnknown
		return nil
	}

	choice := respData.Choices[0]

	// Record the finish reason for tool_calls if present
	if choice.FinishReason != "" {
		finishReasons[choice.FinishReason] = mapFinishReason(choice.FinishReason)
	}

	// Since tool_choice forced a call, if we got tool_calls with tool_calls finish reason, it's yes.
	// Any other response (content, refusal, etc.) with finish_reason != tool_calls is no.
	// A 400 rejecting tools entirely would have been caught as a 4xx error above and returns unknown.
	if len(choice.Message.ToolCalls) > 0 && choice.FinishReason == "tool_calls" {
		record.ToolCalls = transport.TriYes
	} else {
		record.ToolCalls = transport.TriNo
	}

	return nil
}

func (a *Adapter) probeOverLength(ctx context.Context, baseURL *url.URL, record *transport.CapabilityRecord, model string, secrets *[]string, rawSteps map[string]any, opts transport.ProbeOptions) error {
	endpoint := baseURL.JoinPath("/chat/completions")

	reqCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	// Always set the classification rule for consistency
	record.OverLengthClassifier = overLengthClassificationRule

	// Check if calibration is available
	if !record.Calibration.Available {
		record.OverLength = "not_run:no_calibration"
		return nil
	}

	if opts.AssumedContextLen <= 0 {
		record.OverLength = "not_run:no_context"
		return nil
	}

	// Generate hex markers (128 bits = 16 bytes = 32 hex chars)
	headMarkerBytes := make([]byte, 16)
	tailMarkerBytes := make([]byte, 16)
	if _, err := rand.Read(headMarkerBytes); err != nil {
		record.OverLength = "errored"
		return nil
	}
	if _, err := rand.Read(tailMarkerBytes); err != nil {
		record.OverLength = "errored"
		return nil
	}
	headMarker := hex.EncodeToString(headMarkerBytes)
	tailMarker := hex.EncodeToString(tailMarkerBytes)

	// Calculate filler size: 1.5 * assumed_context * bytes_per_token
	fillerSizeBytes := int(1.5 * float64(opts.AssumedContextLen) * record.Calibration.BytesPerToken)
	if fillerSizeBytes < 0 {
		fillerSizeBytes = 0
	}

	// Construct prompt with repeated calibration text
	fillerRepeats := (fillerSizeBytes + len([]byte(calibrationText)) - 1) / len([]byte(calibrationText))
	filler := strings.Repeat(calibrationText, fillerRepeats)
	fillerBytes := []byte(filler)
	if len(fillerBytes) > fillerSizeBytes {
		fillerBytes = fillerBytes[:fillerSizeBytes]
	}

	promptContent := headMarker + "\n" + string(fillerBytes) + "\n" + tailMarker

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
		return nil
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if err := a.addProbeAuth(httpReq, secrets); err != nil {
		record.OverLength = "errored"
		return nil
	}

	if opts.BeforeSend != nil {
		if err := opts.BeforeSend(&transport.ProbeRequest{
			Model:   model,
			Step:    "over_length",
			Timeout: opts.Timeout,
		}); err != nil {
			return fmt.Errorf("%w: %w", transport.ErrNotSent, err)
		}
	}

	httpResp, err := a.doProbe(httpReq, opts, transport.ProbeRequest{Model: model, Step: "over_length", Timeout: opts.Timeout})
	if err != nil {
		record.OverLength = "errored"
		return uncertainErr(err)
	}

	body, readErr := io.ReadAll(io.LimitReader(httpResp.Body, 10*1024*1024))
	closeErr := httpResp.Body.Close()
	if readErr != nil {
		record.OverLength = "errored"
		return uncertainErr(readErr)
	}
	if closeErr != nil {
		record.OverLength = "errored"
		return uncertainErr(closeErr)
	}

	recordRawStep(rawSteps, "over_length", "/chat/completions", httpResp.StatusCode, httpResp.Header, body)

	// Always set the classification rule
	record.OverLengthClassifier = overLengthClassificationRule

	// Classify rejection errors (4xx/5xx)
	if httpResp.StatusCode != 0 && (httpResp.StatusCode < 200 || httpResp.StatusCode >= 300) {
		outcome, _ := classifyRejectionError(httpResp.StatusCode, body)
		record.OverLength = outcome
		// Scrub the full message first, then cut, so a secret split by the cut is not left behind.
		scrubbed, _ := transport.Scrub(rejectionDetail(body), *secrets)
		record.OverLengthDetail = truncateUTF8(scrubbed, overLengthDetailMax)
		return nil
	}

	var respData ResponseBody
	if err := json.Unmarshal(body, &respData); err != nil || len(respData.Choices) == 0 {
		record.OverLength = "errored"
		return nil
	}

	content := respData.Choices[0].Message.Content
	finishReason := respData.Choices[0].FinishReason
	if finishReason != "" {
		record.OverLengthFinishReason = finishReason
	}

	// Check for markers (hex values must appear without requiring surrounding delimiters)
	hasHeadMarker := strings.Contains(content, headMarker)
	hasTailMarker := strings.Contains(content, tailMarker)

	if hasHeadMarker && hasTailMarker {
		record.OverLength = "accepted_whole"
	} else {
		record.OverLength = "markers_missing"
	}

	return nil
}

// calibrationRequest sends one calibration request at probeOutputLimit and,
// when the gateway says the limit was too small, once more at
// probeRetryOutputLimit. The retry's response replaces the first for the caller.
func (a *Adapter) calibrationRequest(ctx context.Context, endpoint *url.URL, model, content, stepName, maxTokensField string, opts transport.ProbeOptions, secrets *[]string, rawSteps map[string]any) (int, http.Header, []byte, error) {
	status, headers, body, reqBody, err := a.makeCalibrationRequest(ctx, endpoint, model, content, stepName, maxTokensField, probeOutputLimit, opts, secrets)
	recordRawStepWithRequest(rawSteps, stepName, "/chat/completions", status, headers, body, reqBody)
	if err == nil && isOutputLimitTooSmall(status, body) {
		stepName += "_retry"
		status, headers, body, reqBody, err = a.makeCalibrationRequest(ctx, endpoint, model, content, stepName, maxTokensField, probeRetryOutputLimit, opts, secrets)
		recordRawStepWithRequest(rawSteps, stepName, "/chat/completions", status, headers, body, reqBody)
	}
	if err != nil {
		err = &transport.ProbeStepError{Step: stepName, Status: status, Err: err}
	}
	return status, headers, body, err
}

func (a *Adapter) probeCalibration(ctx context.Context, baseURL *url.URL, record *transport.CapabilityRecord, model string, secrets *[]string, rawSteps map[string]any, opts transport.ProbeOptions) error {
	endpoint := baseURL.JoinPath("/chat/completions")

	maxTokensField := record.MaxTokensField
	if maxTokensField == "" {
		maxTokensField = "max_tokens"
	}

	baselineStatusCode, _, baselineBody, baselineErr := a.calibrationRequest(ctx, endpoint, model, "", "calibration_baseline", maxTokensField, opts, secrets, rawSteps)

	// A pre-send failure or sent-but-uncertain failure stops the probe before the next request.
	if baselineErr != nil && (errors.Is(baselineErr, transport.ErrNotSent) || errors.Is(baselineErr, transport.ErrUncertain)) {
		record.Calibration.Available = false
		return baselineErr
	}

	// Some endpoints refuse empty message content. Only an HTTP response with a
	// non-2xx status triggers the one-byte fallback baseline.
	baselineBytes := 0
	if baselineErr == nil && baselineStatusCode != 0 && (baselineStatusCode < 200 || baselineStatusCode >= 300) {
		fbStatus, _, fbBody, fbErr := a.calibrationRequest(ctx, endpoint, model, calibrationBaselineNonEmpty, "calibration_baseline_nonempty", maxTokensField, opts, secrets, rawSteps)
		if fbErr != nil && (errors.Is(fbErr, transport.ErrNotSent) || errors.Is(fbErr, transport.ErrUncertain)) {
			record.Calibration.Available = false
			return fbErr
		}
		baselineStatusCode, baselineBody, baselineErr = fbStatus, fbBody, fbErr
		baselineBytes = len(calibrationBaselineNonEmpty)
	}

	calibrationStatusCode, _, calibrationBody, calibrationErr := a.calibrationRequest(ctx, endpoint, model, calibrationText, "calibration", maxTokensField, opts, secrets, rawSteps)

	// ErrNotSent and ErrUncertain must propagate; other errors just mark calibration unavailable
	if baselineErr != nil {
		record.Calibration.Available = false
		return nil
	}
	if calibrationErr != nil {
		if errors.Is(calibrationErr, transport.ErrNotSent) || errors.Is(calibrationErr, transport.ErrUncertain) {
			return calibrationErr
		}
		record.Calibration.Available = false
		return nil
	}
	if baselineStatusCode == 0 || calibrationStatusCode == 0 {
		record.Calibration.Available = false
		return nil
	}

	if baselineStatusCode < 200 || baselineStatusCode >= 300 {
		record.Calibration.Available = false
		return nil
	}

	if calibrationStatusCode < 200 || calibrationStatusCode >= 300 {
		record.Calibration.Available = false
		return nil
	}

	var baselineData ResponseBody
	if err := json.Unmarshal(baselineBody, &baselineData); err != nil || baselineData.Usage == nil || baselineData.Usage.PromptTokens == nil {
		record.Calibration.Available = false
		return nil
	}

	var calibrationData ResponseBody
	if err := json.Unmarshal(calibrationBody, &calibrationData); err != nil || calibrationData.Usage == nil || calibrationData.Usage.PromptTokens == nil {
		record.Calibration.Available = false
		return nil
	}

	baselineTokens := *baselineData.Usage.PromptTokens
	calibrationTokens := *calibrationData.Usage.PromptTokens

	if calibrationTokens <= baselineTokens {
		record.Calibration.Available = false
		return nil
	}

	record.Calibration.Available = true
	record.Calibration.BaselinePromptTokens = baselineTokens
	record.Calibration.ObservedPromptTokens = calibrationTokens
	record.Calibration.BaselineBytes = baselineBytes
	record.Calibration.BytesPerToken = float64(len([]byte(calibrationText))-baselineBytes) / float64(calibrationTokens-baselineTokens)

	return nil
}

func (a *Adapter) makeCalibrationRequest(ctx context.Context, endpoint *url.URL, model string, messageContent string, stepName string, maxTokensField string, limit int, opts transport.ProbeOptions, secrets *[]string) (int, http.Header, []byte, []byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	messages := []MessageInput{
		{Role: "user", Content: messageContent},
	}

	requestBody := map[string]any{
		"model":        model,
		"messages":     messages,
		"stream":       false,
		maxTokensField: limit,
	}

	bodyBytes, _ := json.Marshal(requestBody)

	httpReq, err := http.NewRequestWithContext(reqCtx, "POST", endpoint.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, nil, nil, nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if err := a.addProbeAuth(httpReq, secrets); err != nil {
		return 0, nil, nil, nil, err
	}

	if opts.BeforeSend != nil {
		if err := opts.BeforeSend(&transport.ProbeRequest{
			Model:   model,
			Step:    stepName,
			Timeout: opts.Timeout,
		}); err != nil {
			return 0, nil, nil, nil, fmt.Errorf("%w: %w", transport.ErrNotSent, err)
		}
	}

	httpResp, err := a.doProbe(httpReq, opts, transport.ProbeRequest{Model: model, Step: stepName, Timeout: opts.Timeout})
	if err != nil {
		return 0, nil, nil, nil, uncertainErr(err)
	}

	respBody, err := io.ReadAll(io.LimitReader(httpResp.Body, defaultMaxBodySize))
	closeErr := httpResp.Body.Close()
	if err != nil {
		return httpResp.StatusCode, httpResp.Header, nil, bodyBytes, uncertainErr(err)
	}
	if closeErr != nil {
		return httpResp.StatusCode, httpResp.Header, nil, bodyBytes, uncertainErr(closeErr)
	}

	return httpResp.StatusCode, httpResp.Header, respBody, bodyBytes, nil
}

// addProbeAuth sets auth headers and appends every secret value resolved for
// this request to secrets, so Scrub sees what was actually sent.
func (a *Adapter) addProbeAuth(req *http.Request, secrets *[]string) error {
	vals, err := a.resolveAuthHeaders(req)
	*secrets = append(*secrets, vals...)
	return err
}

func (a *Adapter) resolveAuthHeaders(req *http.Request) ([]string, error) {
	var vals []string
	if a.profile.Auth != nil {
		authVal, err := a.resolver.Resolve(*a.profile.Auth)
		if err != nil {
			return vals, err
		}
		vals = append(vals, authVal)
		req.Header.Set("Authorization", "Bearer "+authVal)
	}

	for headerName, secretRef := range a.profile.ExtraHeaders {
		headerVal, err := a.resolver.Resolve(secretRef)
		if err != nil {
			return vals, err
		}
		vals = append(vals, headerVal)
		req.Header.Set(headerName, headerVal)
	}
	return vals, nil
}
