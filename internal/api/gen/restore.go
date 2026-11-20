package gen

import (
	"bytes"
	"encoding/json"
	"errors"
)

// UnmarshalJSON preserves the distinction between an omitted path (restore
// the whole snapshot) and an explicit null (invalid under the contract).
func (r *RestoreRequest) UnmarshalJSON(data []byte) error {
	type fields RestoreRequest
	var decoded fields
	wire := struct {
		*fields
		Path json.RawMessage `json:"path"`
	}{fields: &decoded}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Path != nil {
		if bytes.Equal(wire.Path, []byte("null")) {
			return errors.New("restore path must be a string when supplied")
		}
		if err := json.Unmarshal(wire.Path, &decoded.Path); err != nil {
			return err
		}
	}
	*r = RestoreRequest(decoded)
	return nil
}
