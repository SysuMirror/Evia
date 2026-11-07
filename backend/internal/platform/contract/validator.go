// Package contract enforces the generated, offline API schema bundle.
package contract

import (
	"encoding/json"
	"fmt"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Validator struct{ schemas map[string]*jsonschema.Schema }

func New() (*Validator, error) {
	var bundle map[string]any
	if err := json.Unmarshal(wire.Schemas, &bundle); err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	const uri = "https://schemas.evia.invalid/contracts/v1"
	if err := c.AddResource(uri, bundle); err != nil {
		return nil, err
	}
	v := &Validator{schemas: make(map[string]*jsonschema.Schema)}
	for name := range bundle["$defs"].(map[string]any) {
		schema, err := c.Compile(uri + "#/$defs/" + name)
		if err != nil {
			return nil, fmt.Errorf("compile %s: %w", name, err)
		}
		v.schemas[name] = schema
	}
	return v, nil
}

func (v *Validator) Validate(name string, data any) error {
	schema, ok := v.schemas[name]
	if !ok {
		return fmt.Errorf("unknown contract %q", name)
	}
	return schema.Validate(data)
}

func (v *Validator) Encode(name string, value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoded, err := Decode(raw)
	if err != nil {
		return nil, err
	}
	if err := v.Validate(name, decoded); err != nil {
		return nil, err
	}
	return raw, nil
}
