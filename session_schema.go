package a13n

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/converge-ai-labs/a13n-sdk-go/generated/sessionstream"
	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Compile only the pinned, bundled graph. All frame references are local; no
// schema discovery, Service request or remote loader is part of observation.
var sessionSchemas = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	var document map[string]any
	if err := json.Unmarshal([]byte(sessionstream.JSONSchema), &document); err != nil {
		return nil, err
	}
	const location = "https://schemas.a13n.invalid/session-stream.json"
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.UseRegexpEngine(func(pattern string) (jsonschema.Regexp, error) {
		expression, err := regexp2.Compile(pattern, regexp2.ECMAScript)
		if err != nil {
			return nil, err
		}
		return sessionRegexp{expression}, nil
	})
	compiler.UseLoader(sessionSchemaLoader{})
	if err := compiler.AddResource(location, document); err != nil {
		return nil, err
	}
	compiled := make(map[string]*jsonschema.Schema)
	for name, value := range document["frames"].(map[string]any) {
		ref := value.(map[string]any)["$ref"].(string)
		schema, err := compiler.Compile(location + ref)
		if err != nil {
			return nil, err
		}
		compiled[name] = schema
	}
	return compiled, nil
})

func validateSessionFrame(name string, data []byte) error {
	schemas, err := sessionSchemas()
	if err != nil {
		return fmt.Errorf("%w: invalid bundled Session schema", ErrProtocol)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if !json.Valid(data) || decoder.Decode(&value) != nil || schemas[name].Validate(value) != nil {
		// Validation errors can contain arbitrary native values and identifiers.
		return fmt.Errorf("%w: malformed Session frame", ErrProtocol)
	}
	return nil
}

// Keep the schema compiler's loader disabled even if a future export adds an
// external reference: protocol inputs must remain independently bundled.
type sessionSchemaLoader struct{}

func (sessionSchemaLoader) Load(_ string) (any, error) {
	return nil, fmt.Errorf("unbundled Session schema")
}

// Pydantic's decimal pattern contains lookahead, outside Go regexp syntax.
type sessionRegexp struct{ *regexp2.Regexp }

func (r sessionRegexp) MatchString(value string) bool {
	matched, err := r.Regexp.MatchString(value)
	return err == nil && matched
}
