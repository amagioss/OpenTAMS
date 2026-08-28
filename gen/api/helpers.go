package api

import "encoding/json"

// MarshalJSON / UnmarshalJSON for tag body union types — oapi-codegen does not generate
// these for oneOf request bodies, leaving the union field nil after JSON decode.

func (b *PutSourceTagJSONBody) UnmarshalJSON(data []byte) error {
	b.union = data
	return nil
}

func (b PutSourceTagJSONBody) MarshalJSON() ([]byte, error) {
	return b.union, nil
}

func (b PutSourceTagJSONBody) RawValue() json.RawMessage {
	return b.union
}

func (b *PutFlowTagJSONBody) UnmarshalJSON(data []byte) error {
	b.union = data
	return nil
}

func (b PutFlowTagJSONBody) MarshalJSON() ([]byte, error) {
	return b.union, nil
}

func (b PutFlowTagJSONBody) RawValue() json.RawMessage {
	return b.union
}

// NewTagsAdditionalProperties wraps a raw JSON value into a Tags_AdditionalProperties union.
func NewTagsAdditionalProperties(raw json.RawMessage) Tags_AdditionalProperties {
	return Tags_AdditionalProperties{union: raw}
}

// Constructors for tag response union types — union field is unexported so
// these must live in the same package.

func NewGetSourceTag200JSONResponse(raw json.RawMessage) GetSourceTag200JSONResponse {
	return GetSourceTag200JSONResponse{union: raw}
}

func NewGetFlowTag200JSONResponse(raw json.RawMessage) GetFlowTag200JSONResponse {
	return GetFlowTag200JSONResponse{union: raw}
}
