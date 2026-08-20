package flaghelpers

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/concourse/concourse/vars"
)

type JSONVariablePairFlag vars.KVPair

func (pair *JSONVariablePairFlag) UnmarshalFlag(value string) error {
	k, v, ok := parseKeyValuePair(value)
	if !ok {
		return fmt.Errorf("invalid JSON variable pair %q (must be name=JSON)", value)
	}

	ref, err := vars.ParseReference(k)
	if err != nil {
		return err
	}

	decoder := json.NewDecoder(strings.NewReader(v))
	var scalar any
	if err := decoder.Decode(&scalar); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid JSON scalar %q", v)
		}
		return err
	}

	switch scalar.(type) {
	case nil, map[string]any, []any:
		return fmt.Errorf("JSON variable %q must be a non-null scalar", k)
	}

	pair.Ref = ref
	pair.Value = scalar
	return nil
}
