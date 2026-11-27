// Package mcp holds syndic's MCP client pieces. This file turns what a server
// reported for one tool into the tool's definition, its bytes and its pin, or
// the reason the tool cannot be offered. It makes no network call: a schema
// reference is only ever inspected, never fetched.
package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/boldfield/syndic/internal/mcp/jcs"
)

// DefaultMaxDefinitionBytes is the default for mcp.max_definition_bytes.
const DefaultMaxDefinitionBytes = 16384

const dialect2020 = "https://json-schema.org/draft/2020-12/schema"

// Reason says why a tool is not offered. The codes are stable: a later task
// shows them to the operator.
type Reason string

const (
	ReasonMalformed       Reason = "malformed"
	ReasonDuplicateMember Reason = "duplicate_member"
	ReasonMissingField    Reason = "missing_field"
	ReasonSchemaNotObject Reason = "schema_not_object"
	ReasonTooLarge        Reason = "too_large"
	ReasonDialect         Reason = "dialect"
	ReasonExternalRef     Reason = "external_ref"
	ReasonXMCPHeader      Reason = "x_mcp_header"
)

// NotOffered is the error returned for a tool that cannot be offered.
type NotOffered struct {
	Reason Reason
	Detail string
}

func (e *NotOffered) Error() string {
	return fmt.Sprintf("tool not offered: %s: %s", e.Reason, e.Detail)
}

// Definition is a tool's definition and its pin.
type Definition struct {
	// Name is the tool's name as reported.
	Name string
	// Bytes is the canonical JSON of {"name","description","inputSchema"}.
	Bytes []byte
	// Pin is the lower-case hex SHA-256 of Bytes.
	Pin string
	// Annotations holds every other member the server reported, as canonical
	// JSON by member name. It is never part of Bytes.
	Annotations map[string]json.RawMessage
}

func notOffered(r Reason, format string, args ...any) error {
	return &NotOffered{Reason: r, Detail: fmt.Sprintf(format, args...)}
}

// BuildDefinition builds the definition of the tool object tool, as a server
// reported it in a listing. A maxBytes of zero or less means
// DefaultMaxDefinitionBytes. A tool that cannot be offered yields a
// *NotOffered error.
func BuildDefinition(tool []byte, maxBytes int) (*Definition, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxDefinitionBytes
	}
	canon, err := jcs.Canonicalize(tool)
	if err != nil {
		if errors.Is(err, jcs.ErrDuplicateMember) {
			return nil, notOffered(ReasonDuplicateMember, "%v", err)
		}
		return nil, notOffered(ReasonMalformed, "%v", err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(canon, &members); err != nil {
		return nil, notOffered(ReasonMalformed, "tool is not a JSON object")
	}

	var name string
	rawName, ok := members["name"]
	if !ok {
		return nil, notOffered(ReasonMissingField, "name")
	}
	if err := json.Unmarshal(rawName, &name); err != nil {
		return nil, notOffered(ReasonMalformed, "name is not a string")
	}
	rawSchema, ok := members["inputSchema"]
	if !ok {
		return nil, notOffered(ReasonMissingField, "inputSchema")
	}
	if len(rawSchema) == 0 || rawSchema[0] != '{' {
		return nil, notOffered(ReasonSchemaNotObject, "inputSchema is not an object")
	}
	rawDesc, ok := members["description"]
	if !ok {
		rawDesc = json.RawMessage(`""`)
	} else if len(rawDesc) == 0 || rawDesc[0] != '"' {
		return nil, notOffered(ReasonMalformed, "description is not a string")
	}

	var b bytes.Buffer
	b.WriteString(`{"name":`)
	b.Write(rawName)
	b.WriteString(`,"description":`)
	b.Write(rawDesc)
	b.WriteString(`,"inputSchema":`)
	b.Write(rawSchema)
	b.WriteString(`}`)
	def, err := jcs.Canonicalize(b.Bytes())
	if err != nil {
		return nil, notOffered(ReasonMalformed, "%v", err)
	}
	if len(def) > maxBytes {
		return nil, notOffered(ReasonTooLarge, "%d bytes over the limit of %d", len(def), maxBytes)
	}

	if err := checkSchema(rawSchema); err != nil {
		return nil, err
	}

	sum := sha256.Sum256(def)
	annotations := map[string]json.RawMessage{}
	for k, v := range members {
		switch k {
		case "name", "description", "inputSchema":
		default:
			annotations[k] = v
		}
	}
	return &Definition{
		Name:        name,
		Bytes:       def,
		Pin:         hex.EncodeToString(sum[:]),
		Annotations: annotations,
	}, nil
}

func checkSchema(raw json.RawMessage) error {
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return notOffered(ReasonMalformed, "inputSchema: %v", err)
	}
	if d, ok := schema["$schema"]; ok {
		s, isStr := d.(string)
		if !isStr || strings.TrimSuffix(s, "#") != dialect2020 {
			return notOffered(ReasonDialect, "$schema %v is not JSON Schema 2020-12", d)
		}
	}
	return walk(schema)
}

// walk visits every object member at any depth. The jcs parse has already
// bounded nesting, so recursion here is bounded too.
func walk(v any) error {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			switch k {
			case "x-mcp-header":
				return notOffered(ReasonXMCPHeader, "x-mcp-header in the schema")
			case "$ref":
				ref, ok := child.(string)
				if !ok || !strings.HasPrefix(ref, "#") {
					return notOffered(ReasonExternalRef, "$ref %v points outside the schema", child)
				}
			}
			if err := walk(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range t {
			if err := walk(child); err != nil {
				return err
			}
		}
	}
	return nil
}
