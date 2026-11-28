package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		return nil
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		in := transport.ClassifyInput{Err: err, RedirectFollowed: redirected, EngineCancelled: ctx.Err() != nil}
		if !redirected && isConnectionRefusedOrDNSErr(err) {
			in.TransportStatus = transport.TransportStatusUnreachable
			err = fmt.Errorf("%w: %w", transport.ErrUnreachable, err)
		} else {
			err = uncertainErr(err)
		}
		res.FailureClass = transport.Classify(in)
		return res, err
	}

	body, over, readErr := readProbeBody(resp.Body, modelsCheckBodyLimit)
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
	if raw, ok := m["data"]; !ok || json.Unmarshal(raw, &data) != nil {
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
