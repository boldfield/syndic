package mcp

// This file derives a tool's definition, its bytes and its pin from what a
// server reported for the tool in a listing, or the reason the tool cannot be
// offered (v9 delta, section 2, "Definitions" and "Pins").
//
// It fetches nothing: a schema reference is only inspected, never resolved.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/boldfield/syndic/internal/mcp/jcs"
)

// DefaultMaxDefinitionBytes is mcp.max_definition_bytes when the roster does
// not set it.
const DefaultMaxDefinitionBytes = 16384

// Reason is why a tool is not offered. The operator is shown it.
type Reason string

// Reasons a tool is not offered.
const (
	// ReasonMalformed: the tool is not a single JSON object, a member is not
	// of its required type (name and description must be strings), or it is
	// nested too deeply to canonicalize.
	ReasonMalformed Reason = "malformed"
	// ReasonDuplicateMember: some object in the tool has a member name twice.
	ReasonDuplicateMember Reason = "duplicate_member"
	// ReasonMissingField: name or inputSchema is absent or null.
	ReasonMissingField Reason = "missing_field"
	// ReasonSchemaNotObject: inputSchema is not a JSON object.
	ReasonSchemaNotObject Reason = "schema_not_object"
	// ReasonTooLarge: the definition is over mcp.max_definition_bytes.
	ReasonTooLarge Reason = "too_large"
	// ReasonDialect: some schema in inputSchema declares a $schema other than
	// JSON Schema 2020-12.
	ReasonDialect Reason = "dialect"
	// ReasonExternalRef: a $ref, $dynamicRef or $recursiveRef does not begin
	// with "#", so it points outside the schema.
	ReasonExternalRef Reason = "external_ref"
	// ReasonMCPHeader: an x-mcp-header member appears in inputSchema.
	ReasonMCPHeader Reason = "x_mcp_header"
)

// NotOfferedError reports that a tool is not offered and why.
type NotOfferedError struct {
	Reason Reason
	Detail string
}

func (e *NotOfferedError) Error() string {
	return fmt.Sprintf("mcp: tool not offered (%s): %s", e.Reason, e.Detail)
}

func notOffered(r Reason, format string, args ...any) error {
	return &NotOfferedError{Reason: r, Detail: fmt.Sprintf(format, args...)}
}

// Definition is a tool's definition as a model is shown it and a reviewer
// approves it.
type Definition struct {
	// Name is the tool's name as the server reported it.
	Name string
	// Bytes is the canonical JSON of {"name", "description", "inputSchema"}.
	Bytes []byte
	// Pin is the SHA-256 of Bytes, lower-case hex.
	Pin string
	// Annotations is the canonical JSON of the server's annotations member,
	// or nil when it reported none. It is never part of Bytes.
	Annotations json.RawMessage
}

// dialect2020 is the meta-schema URI of JSON Schema 2020-12.
const dialect2020 = "https://json-schema.org/draft/2020-12/schema"

// BuildDefinition derives the definition of one tool from the tool object a
// server reported in a listing. maxBytes is mcp.max_definition_bytes; zero or
// less means DefaultMaxDefinitionBytes. A tool that cannot be offered gives a
// *NotOfferedError.
//
// An absent description is the empty string; a null or non-string description
// is malformed, not absent. Members other than name, description and
// inputSchema are left out of the definition; annotations is returned beside
// it.
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
	if err := json.Unmarshal(canon, &members); err != nil || members == nil {
		return nil, notOffered(ReasonMalformed, "the tool is not a JSON object")
	}

	rawName, ok := members["name"]
	if !ok || isNull(rawName) {
		return nil, notOffered(ReasonMissingField, "name is missing")
	}
	var name string
	if rawName[0] != '"' || json.Unmarshal(rawName, &name) != nil {
		return nil, notOffered(ReasonMalformed, "name is not a string")
	}
	rawDesc, ok := members["description"]
	if !ok {
		rawDesc = json.RawMessage(`""`)
	} else if rawDesc[0] != '"' {
		return nil, notOffered(ReasonMalformed, "description is not a string")
	}
	rawSchema, ok := members["inputSchema"]
	if !ok || isNull(rawSchema) {
		return nil, notOffered(ReasonMissingField, "inputSchema is missing")
	}
	if rawSchema[0] != '{' {
		return nil, notOffered(ReasonSchemaNotObject, "inputSchema is not an object")
	}

	// Members are already canonical and written in canonical order, so this
	// is the definition's canonical form.
	var def bytes.Buffer
	def.WriteString(`{"description":`)
	def.Write(rawDesc)
	def.WriteString(`,"inputSchema":`)
	def.Write(rawSchema)
	def.WriteString(`,"name":`)
	def.Write(rawName)
	def.WriteString(`}`)
	if def.Len() > maxBytes {
		return nil, notOffered(ReasonTooLarge, "definition is %d bytes, over the bound of %d", def.Len(), maxBytes)
	}

	var schema any
	dec := json.NewDecoder(bytes.NewReader(rawSchema))
	dec.UseNumber()
	if err := dec.Decode(&schema); err != nil {
		return nil, notOffered(ReasonMalformed, "inputSchema: %v", err)
	}
	if path, found := findMember(schema, "x-mcp-header", "inputSchema"); found {
		return nil, notOffered(ReasonMCPHeader, "x-mcp-header at %s", path)
	}
	if err := checkSchema(schema, "inputSchema"); err != nil {
		return nil, err
	}

	sum := sha256.Sum256(def.Bytes())
	d := &Definition{Name: name, Bytes: def.Bytes(), Pin: hex.EncodeToString(sum[:])}
	if ann, ok := members["annotations"]; ok {
		d.Annotations = ann
	}
	return d, nil
}

func isNull(raw json.RawMessage) bool {
	return string(raw) == "null"
}

// sortedKeys returns the member names of m in byte order, so that every walk
// visits members in the same order and reports the same reason.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// findMember reports the path of the first member called name in v, at any
// depth and in any position, schema or data.
func findMember(v any, name, path string) (string, bool) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(t) {
			p := path + "/" + k
			if k == name {
				return p, true
			}
			if found, ok := findMember(t[k], name, p); ok {
				return found, true
			}
		}
	case []any:
		for i, item := range t {
			if found, ok := findMember(item, name, fmt.Sprintf("%s/%d", path, i)); ok {
				return found, true
			}
		}
	}
	return "", false
}

// Keywords of JSON Schema 2020-12, and the earlier drafts' spellings, by the
// shape of their value. Any other member of a schema object, an unknown
// extension or annotation keyword among them, holds data and is not walked as
// a schema.
var (
	schemaKeywords = map[string]bool{
		"additionalItems": true, "additionalProperties": true, "contains": true,
		"contentSchema": true, "else": true, "if": true, "items": true,
		"not": true, "propertyNames": true, "then": true,
		"unevaluatedItems": true, "unevaluatedProperties": true,
	}
	schemaArrayKeywords = map[string]bool{
		"allOf": true, "anyOf": true, "oneOf": true, "prefixItems": true,
	}
	schemaMapKeywords = map[string]bool{
		"$defs": true, "definitions": true, "dependentSchemas": true,
		"patternProperties": true, "properties": true,
	}
	refKeywords = map[string]bool{
		"$ref": true, "$dynamicRef": true, "$recursiveRef": true,
	}
)

// checkSchema checks a value in schema position: every $schema it or any
// subschema declares is 2020-12, and every reference begins with "#".
// Member names under properties, $defs and the like are instance or
// definition names, not keywords, and only their values are walked.
func checkSchema(v any, path string) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil // a boolean schema, or a value no validator would accept
	}
	for _, k := range sortedKeys(obj) {
		val := obj[k]
		p := path + "/" + k
		switch {
		case k == "$schema":
			s, ok := val.(string)
			if !ok || strings.TrimSuffix(s, "#") != dialect2020 {
				return notOffered(ReasonDialect, "%s declares %s, not JSON Schema 2020-12", p, describe(val))
			}
		case refKeywords[k]:
			s, ok := val.(string)
			if !ok {
				return notOffered(ReasonExternalRef, "%s is %s, not a reference within the schema", p, describe(val))
			}
			if !strings.HasPrefix(s, "#") {
				return notOffered(ReasonExternalRef, "%s %q points outside the schema", p, s)
			}
		case schemaKeywords[k]:
			// items and additionalItems took an array of schemas before
			// 2020-12; walk either form.
			if arr, ok := val.([]any); ok {
				if err := checkSchemaArray(arr, p); err != nil {
					return err
				}
			} else if err := checkSchema(val, p); err != nil {
				return err
			}
		case schemaArrayKeywords[k]:
			if arr, ok := val.([]any); ok {
				if err := checkSchemaArray(arr, p); err != nil {
					return err
				}
			}
		case schemaMapKeywords[k]:
			if err := checkSchemaMap(val, p); err != nil {
				return err
			}
		case k == "dependencies":
			// Before 2020-12, each value is a schema or an array of names.
			if err := checkSchemaMap(val, p); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkSchemaArray(arr []any, path string) error {
	for i, item := range arr {
		if err := checkSchema(item, fmt.Sprintf("%s/%d", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func checkSchemaMap(v any, path string) error {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	for _, k := range sortedKeys(m) {
		if err := checkSchema(m[k], path+"/"+k); err != nil {
			return err
		}
	}
	return nil
}

// describe renders a keyword's value for an operator-facing detail.
func describe(v any) string {
	switch t := v.(type) {
	case string:
		return fmt.Sprintf("%q", t)
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case json.Number:
		return "a number"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	}
	return "a value"
}
