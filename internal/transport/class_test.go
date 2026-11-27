package transport

import (
	"errors"
	"fmt"
	"testing"
)

func TestClassifyStatusTable(t *testing.T) {
	want := map[int]FailureClass{
		429: FailureClassThrottled, 503: FailureClassThrottled, 529: FailureClassThrottled,
		500: FailureClassServerFault, 506: FailureClassServerFault, 507: FailureClassServerFault,
		511: FailureClassServerFault, 599: FailureClassServerFault,
		502: FailureClassUncertain, 504: FailureClassUncertain, 408: FailureClassUncertain,
		401: FailureClassRejectedConfig, 403: FailureClassRejectedConfig, 404: FailureClassRejectedConfig,
		501: FailureClassRejectedConfig, 505: FailureClassRejectedConfig,
		300: FailureClassRejectedConfig, 301: FailureClassRejectedConfig, 302: FailureClassRejectedConfig,
		307: FailureClassRejectedConfig, 308: FailureClassRejectedConfig, 399: FailureClassRejectedConfig,
		402: FailureClassQuota,
		400: FailureClassRejectedRequest, 405: FailureClassRejectedRequest, 413: FailureClassRejectedRequest,
		422: FailureClassRejectedRequest, 418: FailureClassRejectedRequest, 451: FailureClassRejectedRequest,
		499: FailureClassRejectedRequest,
	}
	for code := 300; code <= 599; code++ {
		got := Classify(ClassifyInput{Received: true, HTTPStatus: code})
		exp, ok := want[code]
		if !ok {
			switch {
			case code >= 500:
				exp = FailureClassServerFault
			case code >= 400:
				exp = FailureClassRejectedRequest
			default:
				exp = FailureClassRejectedConfig
			}
		}
		if got != exp {
			t.Errorf("status %d: got %q want %q", code, got, exp)
		}
	}
}

func TestClassifyBadGateway(t *testing.T) {
	if got := Classify(ClassifyInput{Received: true, HTTPStatus: 502}); got != FailureClassUncertain {
		t.Errorf("default 502: %q", got)
	}
	if got := Classify(ClassifyInput{Received: true, HTTPStatus: 502, BadGatewayServerFault: true}); got != FailureClassServerFault {
		t.Errorf("502 server_fault: %q", got)
	}
	for _, c := range []int{504, 408} {
		if got := Classify(ClassifyInput{Received: true, HTTPStatus: c, BadGatewayServerFault: true}); got != FailureClassUncertain {
			t.Errorf("%d with bad_gateway: %q", c, got)
		}
	}
}

func TestClassifyPrecedence(t *testing.T) {
	other := errors.New("boom")
	notSent := fmt.Errorf("wrap: %w", ErrNotSent)
	tests := []struct {
		name string
		in   ClassifyInput
		want FailureClass
	}{
		{"rule1 cancelled no response", ClassifyInput{EngineCancelled: true, TransportStatus: TransportStatusCancelled}, FailureClassNone},
		{"rule1 beats unreachable", ClassifyInput{EngineCancelled: true, TransportStatus: TransportStatusUnreachable}, FailureClassNone},
		{"rule1 beats not sent", ClassifyInput{EngineCancelled: true, Err: notSent}, FailureClassNone},
		{"rule2 unreachable", ClassifyInput{TransportStatus: TransportStatusUnreachable}, FailureClassUnreachable},
		{"rule2 unreachable with error", ClassifyInput{TransportStatus: TransportStatusUnreachable, Err: other}, FailureClassUnreachable},
		{"rule2 unreachable beats not sent", ClassifyInput{TransportStatus: TransportStatusUnreachable, Err: notSent}, FailureClassUnreachable},
		{"rule2 not sent", ClassifyInput{Err: notSent}, FailureClassLocal},
		{"rule3 reset", ClassifyInput{TransportStatus: TransportStatusReset}, FailureClassUncertain},
		{"rule3 timeout", ClassifyInput{TransportStatus: TransportStatusTimeout}, FailureClassUncertain},
		{"rule3 other error", ClassifyInput{Err: other}, FailureClassUncertain},
		{"rule3 cancel not by engine", ClassifyInput{TransportStatus: TransportStatusCancelled}, FailureClassUncertain},
		{"rule3 after redirect", ClassifyInput{RedirectFollowed: true, TransportStatus: TransportStatusUnreachable}, FailureClassUncertain},
		{"rule3 after redirect not sent", ClassifyInput{RedirectFollowed: true, Err: notSent}, FailureClassUncertain},
		{"rule4 429 quota", ClassifyInput{Received: true, HTTPStatus: 429, QuotaMarker: true}, FailureClassQuota},
		{"rule4 500 quota", ClassifyInput{Received: true, HTTPStatus: 500, QuotaMarker: true}, FailureClassQuota},
		{"rule4 400 quota", ClassifyInput{Received: true, HTTPStatus: 400, QuotaMarker: true}, FailureClassQuota},
		{"rule4 2xx quota", ClassifyInput{Received: true, HTTPStatus: 200, QuotaMarker: true}, FailureClassQuota},
		{"rule4 cancelled but received", ClassifyInput{Received: true, HTTPStatus: 429, QuotaMarker: true, EngineCancelled: true}, FailureClassQuota},
		{"rule5 cancelled but received", ClassifyInput{Received: true, HTTPStatus: 429, EngineCancelled: true}, FailureClassThrottled},
		{"rule5 unreadable body keeps status", ClassifyInput{Received: true, HTTPStatus: 503, TransportStatus: TransportStatusReset, Err: other}, FailureClassThrottled},
		{"rule5 over limit keeps status", ClassifyInput{Received: true, HTTPStatus: 404, TransportStatus: TransportStatusBodyTooLarge}, FailureClassRejectedConfig},
		{"rule5 non-2xx ignores body read", ClassifyInput{Received: true, HTTPStatus: 400, BodyRead: BodyUnparsable}, FailureClassRejectedRequest},
		{"rule6 parsed", ClassifyInput{Received: true, HTTPStatus: 200}, FailureClassNone},
		{"rule6 reset", ClassifyInput{Received: true, HTTPStatus: 200, BodyRead: BodyReset}, FailureClassUncertain},
		{"rule6 timeout", ClassifyInput{Received: true, HTTPStatus: 200, BodyRead: BodyTimeout}, FailureClassUncertain},
		{"rule6 over limit", ClassifyInput{Received: true, HTTPStatus: 200, BodyRead: BodyOverLimit}, FailureClassUncertain},
		{"rule6 unparsable", ClassifyInput{Received: true, HTTPStatus: 200, BodyRead: BodyUnparsable}, FailureClassBadResponse},
		{"rule6 cancelled but received", ClassifyInput{Received: true, HTTPStatus: 200, BodyRead: BodyUnparsable, EngineCancelled: true}, FailureClassBadResponse},
	}
	for _, tc := range tests {
		if got := Classify(tc.in); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}
