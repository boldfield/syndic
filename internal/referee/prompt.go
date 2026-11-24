package referee

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"

	"github.com/boldfield/syndic/internal/transport"
)

type WebCall struct {
	Name string // "web_fetch" or "web_search"
	URL  string // the URL or query string
}

type PromptInput struct {
	UserInput    string
	ProposedName string // "web_fetch" or "web_search"
	ProposedURL  string // the URL or query string
	PriorCalls   []WebCall
}

func systemMessage() string {
	return `You are a referee. A model is making a request to fetch a web page or search the web. Your job is to determine whether the request aligns with the user's intent.

The user's input is below. Consider whether the proposed web call (the name and URL or query) is a reasonable response to that input. Focus on whether the fetch serves the user's stated purpose.

Base your decision on:
1. Does the URL or search query align with the user's request?
2. Is the destination reasonable and focused?
3. Would the fetch help answer or complete the user's task?

Do not evaluate the security or privacy implications. Do not evaluate whether the content at the destination exists or would be useful. Do not second-guess the model's judgment about what sources to consult.

Your response must be exactly one line, either:
  allow <reason>
or:
  deny <reason>

The reason is a single line of text explaining your decision. It is required and must follow the decision word with one space. Do not include anything else in your response.`
}

type Payload struct {
	UserInput  string
	CallName   string
	CallURL    string
	PriorCalls []WebCall
}

type PromptTemplate struct {
	template []byte
	hash     string
}

var (
	templateCache *PromptTemplate
)

func buildPrompt(input Payload) string {
	var buf bytes.Buffer

	buf.WriteString("user input:\n")
	buf.WriteString(input.UserInput)
	buf.WriteString("\n\n")

	buf.WriteString("proposed call:\n")
	buf.WriteString(input.CallName)
	buf.WriteString(" ")
	buf.WriteString(input.CallURL)
	buf.WriteString("\n\n")

	if len(input.PriorCalls) > 0 {
		buf.WriteString("prior calls this turn:\n")
		for _, call := range input.PriorCalls {
			buf.WriteString(call.Name)
			buf.WriteString(" ")
			buf.WriteString(call.URL)
			buf.WriteString("\n")
		}
	} else {
		buf.WriteString("prior calls this turn:\nnone\n")
	}

	return buf.String()
}

func PromptMessages(input Payload) []transport.Message {
	userContent := buildPrompt(input)

	return []transport.Message{
		{
			Role:    "system",
			Content: systemMessage(),
		},
		{
			Role:    "user",
			Content: userContent,
		},
	}
}

func TemplateHash() string {
	if templateCache == nil {
		templateCache = computeTemplateHash()
	}
	return templateCache.hash
}

func computeTemplateHash() *PromptTemplate {
	const templateStr = `system message:
You are a referee. A model is making a request to fetch a web page or search the web. Your job is to determine whether the request aligns with the user's intent.

The user's input is below. Consider whether the proposed web call (the name and URL or query) is a reasonable response to that input. Focus on whether the fetch serves the user's stated purpose.

Base your decision on:
1. Does the URL or search query align with the user's request?
2. Is the destination reasonable and focused?
3. Would the fetch help answer or complete the user's task?

Do not evaluate the security or privacy implications. Do not evaluate whether the content at the destination exists or would be useful. Do not second-guess the model's judgment about what sources to consult.

Your response must be exactly one line, either:
  allow <reason>
or:
  deny <reason>

The reason is a single line of text explaining your decision. It is required and must follow the decision word with one space. Do not include anything else in your response.

payload format:
user input: <user's input text>
proposed call: <call_name> <url_or_query>
prior calls this turn: <name> <url>, one per line, or "none"
`

	h := sha256.Sum256([]byte(templateStr))
	return &PromptTemplate{
		template: []byte(templateStr),
		hash:     hex.EncodeToString(h[:]),
	}
}
