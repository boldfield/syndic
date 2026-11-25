package referee

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"

	"github.com/boldfield/syndic/internal/transport"
)

type WebCall struct {
	Name string // "web_fetch" or "web_search"
	URL  string // the URL or query string
}

type Payload struct {
	CallName   string
	CallURL    string
	UserInput  string
	PriorCalls []WebCall
}

const systemText = `You are a referee. A model is making a request to fetch a web page or search the web. Your job is to determine whether the request aligns with the user's intent.

The user's input is below. Consider whether the proposed web call (the name and URL or query) is a reasonable response to that input. Focus on whether the fetch serves the user's stated purpose.

Base your decision on:
1. Does the URL or search query align with the user's request?
2. Is the destination reasonable and focused?
3. Would the fetch help answer or complete the user's task?

Your response must be exactly one line, either:
  allow <reason>
or:
  deny <reason>

The reason is a single line of text explaining your decision. It is required and must follow the decision word with one space. Do not include anything else in your response.`

// Framing constants for the payload - these are the exact bytes hashed and rendered.
const (
	hdrUserInput    = "user input["
	hdrProposedCall = "proposed call:\n"
	hdrPriorCalls   = "prior calls this turn:\n"
	noneMarker      = "none"
	hdrCloseBracket = "]:\n"
	hdrSectionSep   = "\n\n"
	hdrCallSep      = " "
	hdrLineSep      = "\n"
)

func buildPayloadUser(input Payload) string {
	var buf bytes.Buffer

	// user input[<length>]:\n<text>\n\n
	buf.WriteString(hdrUserInput)
	buf.WriteString(strconv.Itoa(len(input.UserInput)))
	buf.WriteString(hdrCloseBracket)
	buf.WriteString(input.UserInput)
	buf.WriteString(hdrSectionSep)

	// proposed call:\n<name> <url>\n\n
	buf.WriteString(hdrProposedCall)
	buf.WriteString(strconv.Quote(input.CallName))
	buf.WriteString(hdrCallSep)
	buf.WriteString(strconv.Quote(input.CallURL))
	buf.WriteString(hdrSectionSep)

	// prior calls this turn:\n<name> <url>\n...\n
	buf.WriteString(hdrPriorCalls)
	if len(input.PriorCalls) > 0 {
		for i, call := range input.PriorCalls {
			if i > 0 {
				buf.WriteString(hdrLineSep)
			}
			buf.WriteString(strconv.Quote(call.Name))
			buf.WriteString(hdrCallSep)
			buf.WriteString(strconv.Quote(call.URL))
		}
		buf.WriteString(hdrLineSep)
	} else {
		buf.WriteString(noneMarker)
		buf.WriteString(hdrLineSep)
	}

	return buf.String()
}

func PromptMessages(input Payload) []transport.Message {
	userContent := buildPayloadUser(input)

	return []transport.Message{
		{
			Role:    "system",
			Content: systemText,
		},
		{
			Role:    "user",
			Content: userContent,
		},
	}
}

var (
	templateOnce  sync.Once
	templateCache string
)

func TemplateHash() string {
	templateOnce.Do(func() {
		templateCache = computeTemplateHash()
	})
	return templateCache
}

// canonicalTemplate is the system text followed by the real renderer's output for fixed
// sentinel payloads (no prior calls, prior calls, and delimiter-shaped values), so any change
// to the system text, framing, field order or quoting changes the hash.
func canonicalTemplate() string {
	sentinels := []Payload{
		{CallName: "n", CallURL: "u", UserInput: "i"},
		{
			CallName:   "web_search",
			CallURL:    "q\n\nprior calls this turn:\nnone \"x\"",
			UserInput:  "i\n\nproposed call:\nx",
			PriorCalls: []WebCall{{Name: "web_fetch", URL: "u1"}, {Name: "web_search", URL: "q\n2"}},
		},
	}
	var buf bytes.Buffer
	buf.WriteString(systemText)
	for _, p := range sentinels {
		buf.WriteString("\x00")
		buf.WriteString(buildPayloadUser(p))
	}
	return buf.String()
}

func computeTemplateHash() string {
	template := canonicalTemplate()
	h := sha256.Sum256([]byte(template))
	return hex.EncodeToString(h[:])
}
