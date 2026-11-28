package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
)

const (
	modelsCheckTimeout   = 10 * time.Second
	modelsCheckBodyLimit = 1 << 20
)

// CheckModels sends one GET on the models route with the endpoint's
// credential, a 10 s timeout and at most 1 MiB of body read. It sends no
// completion request and records no evidence. The class, wait and no-retry
// mark are those a completion with the same response would carry.
func (a *Adapter) CheckModels(ctx context.Context) (transport.ModelsCheck, error) {
	var res transport.ModelsCheck
	baseURL, err := url.Parse(a.profile.BaseURL)
	if err != nil {
		return res, fmt.Errorf("%w: %w", transport.ErrNotSent, err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, modelsCheckTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodGet, baseURL.JoinPath("/models").String(), nil)
	if err != nil {
		return res, fmt.Errorf("%w: %w", transport.ErrNotSent, err)
	}
	if _, err := a.resolveAuthHeaders(httpReq); err != nil {
		err = fmt.Errorf("%w: %w", transport.ErrNotSent, err)
		res.FailureClass = transport.Classify(transport.ClassifyInput{Err: err})
		return res, err
	}

	// A health check is one GET: a redirect is reported, never followed.
	client := *a.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(httpReq)
	if err != nil {
		in := transport.ClassifyInput{Err: err, EngineCancelled: ctx.Err() != nil}
		if isConnectionRefusedOrDNSErr(err) {
			in.TransportStatus = transport.TransportStatusUnreachable
			err = fmt.Errorf("%w: %w", transport.ErrUnreachable, err)
		} else {
			err = uncertainErr(err)
		}
		res.FailureClass = transport.Classify(in)
		return res, err
	}

	// Read no more than the bound; a body that fills it is treated as cut.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, modelsCheckBodyLimit))
	over := int64(len(body)) >= modelsCheckBodyLimit
	readErr = errors.Join(readErr, resp.Body.Close())

	in := transport.ClassifyInput{
		Received:              true,
		HTTPStatus:            resp.StatusCode,
		BadGatewayServerFault: a.profile.BadGateway == config.BadGatewayServerFault,
	}
	if readErr == nil && !over {
		in.QuotaMarker = hasQuotaMarker(resp.StatusCode, body)
	}
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	var models []string
	var listed bool
	if readErr != nil {
		in.BodyRead = transport.BodyReset
		if isDeadlineExceededErr(readErr) {
			in.BodyRead = transport.BodyTimeout
		}
	} else if ok {
		if over {
			in.BodyRead = transport.BodyOverLimit
		} else if models, listed = parseModelList(body); !modelsUsable(body) {
			in.BodyRead = transport.BodyUnparsable
		}
	}

	res.Status = resp.StatusCode
	res.OK = ok
	res.Usable = res.Status != 404 && res.Status != 405 && res.Status != 501
	res.FailureClass = transport.Classify(in)
	if res.FailureClass != transport.FailureClassNone {
		res.ServerWait = readServerWait(resp.StatusCode, resp.Header, time.Now())
		res.NoRetry = readNoRetry(resp.Header)
	}
	if ok && listed {
		res.Listed = true
		res.Models = models
	}
	return res, nil
}

// parseModelList reads the ids of a models listing. listed is false when the
// body is not a list with a data array or indicates a further page.
func parseModelList(body []byte) (ids []string, listed bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return nil, false
	}
	var data []map[string]any
	if raw, ok := m["data"]; !ok || string(raw) == "null" || json.Unmarshal(raw, &data) != nil {
		return nil, false
	}
	if raw, ok := m["has_more"]; ok {
		var more bool
		if json.Unmarshal(raw, &more) != nil || more {
			return nil, false
		}
	}
	for _, key := range []string{"next", "next_page", "next_page_token", "next_cursor", "next_url"} {
		if raw, ok := m[key]; ok {
			var v any
			if json.Unmarshal(raw, &v) != nil || (v != nil && v != "" && v != false) {
				return nil, false
			}
		}
	}
	ids = []string{}
	for _, e := range data {
		if id, ok := e["id"].(string); ok {
			ids = append(ids, id)
		}
	}
	return ids, true
}
