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
	UserInput  string
	CallName   string
	CallURL    string
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

func buildPayloadUser(input Payload) string {
	var buf bytes.Buffer

	buf.WriteString("user input[")
	userLen := len(input.UserInput)
	buf.WriteString(strconv.Itoa(userLen))
	buf.WriteString("]:\n")
	buf.WriteString(input.UserInput)
	buf.WriteString("\n\n")

	buf.WriteString("proposed call:\n")
	buf.WriteString(input.CallName)
	buf.WriteString(" ")
	buf.WriteString(input.CallURL)
	buf.WriteString("\n\n")

	buf.WriteString("prior calls this turn:\n")
	if len(input.PriorCalls) > 0 {
		for _, call := range input.PriorCalls {
			buf.WriteString(call.Name)
			buf.WriteString(" ")
			buf.WriteString(call.URL)
			buf.WriteString("\n")
		}
	} else {
		buf.WriteString("none\n")
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

func computeTemplateHash() string {
	var buf bytes.Buffer
	buf.WriteString(systemText)
	buf.WriteString("\n\npayload format:\nuser input[<length>]:\n<user input text>\n\nproposed call:\n<call_name> <url_or_query>\n\nprior calls this turn:\n<name> <url>, one per line, or \"none\"\n")
	templateBytes := buf.Bytes()
	h := sha256.Sum256(templateBytes)
	return hex.EncodeToString(h[:])
}
