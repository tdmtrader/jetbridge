package contracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// Canonical JSON, in two named layers. Confusing them is the failure this naming
// exists to prevent: canonicalJSONPayload is the payload, and only the framed
// canonicalJSONSerialization is a descriptor.
//
// Go's own encoder produces none of this. encoding/json HTML-escapes '<', '>' and
// '&', escapes U+2028 and U+2029, and re-renders every number from float64, so
// json.Marshal cannot be used and json.Compact is likewise insufficient — the
// documents this hashes are human-authored and record.json is model-authored, so
// key order and number spelling are unstable across re-runs, which is the exact
// instability a content digest exists to defeat.
const canonicalJSONAlgorithm = "snapshot-canonical-json/1"

// canonicalJSONPayload is canonical-value(v): recursive key sort by unsigned byte
// comparison, array order preserved, number literals preserved byte for byte, no
// insignificant whitespace anywhere.
//
// Every rejection below exists for one reason: it is a way for two distinct
// inputs to reach one canonical form, and a content digest must never allow that.
func canonicalJSONPayload(document []byte) ([]byte, error) {
	value, err := canonicalJSONValueTree(document)
	if err != nil {
		return nil, err
	}
	var payload bytes.Buffer
	if err := writeCanonicalValue(&payload, value); err != nil {
		return nil, err
	}
	return payload.Bytes(), nil
}

// canonicalJSONValueTree decodes one document into the ordered, literal-preserving
// tree the canonical writer emits from. It is exposed separately so a caller that
// needs to inspect the document's structure — the schema loader checking that no
// key smuggles in an epistemic status — walks the same tree the digest is computed
// over, rather than a second, differently-decoded copy.
func canonicalJSONValueTree(document []byte) (any, error) {
	if !utf8.Valid(document) {
		return nil, fmt.Errorf("snapshot contracts: canonical JSON input is not valid UTF-8; replacing a bad byte with U+FFFD would map two distinct inputs to one canonical form")
	}
	if err := rejectUnpairedSurrogateEscapes(document); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	value, err := decodeCanonicalValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("snapshot contracts: canonical JSON input must contain exactly one JSON value")
		}
		return nil, fmt.Errorf("snapshot contracts: canonical JSON input must contain exactly one JSON value: %w", err)
	}
	return value, nil
}

// canonicalJSONSerialization is canonical-serialization(v):
//
//	"snapshot-canonical-json/1" 0x0A <decimal byte length> 0x0A <payload>
//
// The framing makes the encoding prefix-free, so no concatenation of canonical
// values can be confused with a different one, and it versions the algorithm in
// the bytes themselves. A descriptor string for revision >= 2 is exactly this.
func canonicalJSONSerialization(document []byte) ([]byte, error) {
	payload, err := canonicalJSONPayload(document)
	if err != nil {
		return nil, err
	}
	framed := make([]byte, 0, len(canonicalJSONAlgorithm)+len(payload)+16)
	framed = append(framed, canonicalJSONAlgorithm...)
	framed = append(framed, '\n')
	framed = append(framed, strconv.Itoa(len(payload))...)
	framed = append(framed, '\n')
	framed = append(framed, payload...)
	return framed, nil
}

// canonicalMember is one object member, kept as a slice rather than a map so a
// duplicate key is detectable at all: a map would silently apply last-wins, which
// is a bug surface and not a rule.
type canonicalMember struct {
	key   string
	value any
}

type canonicalObject []canonicalMember

type canonicalArray []any

func decodeCanonicalValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("snapshot contracts: canonical JSON input must contain exactly one JSON value")
		}
		return nil, fmt.Errorf("snapshot contracts: decode canonical JSON: %w", err)
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return canonicalScalar(token)
	}
	switch delimiter {
	case '{':
		object := canonicalObject{}
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, fmt.Errorf("snapshot contracts: decode canonical JSON object key: %w", err)
			}
			key, isString := keyToken.(string)
			if !isString {
				return nil, fmt.Errorf("snapshot contracts: canonical JSON object key is not a string")
			}
			if _, found := seen[key]; found {
				return nil, fmt.Errorf("snapshot contracts: canonical JSON object has duplicate key %q; last-wins is a bug surface, not a rule", key)
			}
			seen[key] = struct{}{}
			value, err := decodeCanonicalValue(decoder)
			if err != nil {
				return nil, err
			}
			object = append(object, canonicalMember{key: key, value: value})
		}
		if _, err := decoder.Token(); err != nil {
			return nil, fmt.Errorf("snapshot contracts: decode canonical JSON object end: %w", err)
		}
		return object, nil
	case '[':
		array := canonicalArray{}
		for decoder.More() {
			element, err := decodeCanonicalValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, element)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, fmt.Errorf("snapshot contracts: decode canonical JSON array end: %w", err)
		}
		return array, nil
	default:
		return nil, fmt.Errorf("snapshot contracts: unexpected canonical JSON delimiter %q", delimiter)
	}
}

func canonicalScalar(token json.Token) (any, error) {
	switch typed := token.(type) {
	case string, json.Number, bool, nil:
		return typed, nil
	default:
		return nil, fmt.Errorf("snapshot contracts: unexpected canonical JSON token %T", token)
	}
}

func writeCanonicalValue(buffer *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case canonicalObject:
		// Unsigned byte comparison of the key's UTF-8 encoding, which is exactly
		// what Go's string comparison does.
		members := append(canonicalObject(nil), typed...)
		sort.SliceStable(members, func(i, j int) bool { return members[i].key < members[j].key })
		buffer.WriteByte('{')
		for index, member := range members {
			if index > 0 {
				buffer.WriteByte(',')
			}
			if err := writeCanonicalString(buffer, member.key); err != nil {
				return err
			}
			buffer.WriteByte(':')
			if err := writeCanonicalValue(buffer, member.value); err != nil {
				return err
			}
		}
		buffer.WriteByte('}')
		return nil
	case canonicalArray:
		// Array order is data and is never sorted.
		buffer.WriteByte('[')
		for index, element := range typed {
			if index > 0 {
				buffer.WriteByte(',')
			}
			if err := writeCanonicalValue(buffer, element); err != nil {
				return err
			}
		}
		buffer.WriteByte(']')
		return nil
	case string:
		return writeCanonicalString(buffer, typed)
	case json.Number:
		// The source literal, preserved byte for byte: 1, 1.0 and 1e0 are three
		// distinct canonical forms. Every normalization of a decimal literal
		// either loses information or needs a float round-trip that is not stable
		// across implementations.
		buffer.WriteString(typed.String())
		return nil
	case bool:
		if typed {
			buffer.WriteString("true")
		} else {
			buffer.WriteString("false")
		}
		return nil
	case nil:
		buffer.WriteString("null")
		return nil
	default:
		return fmt.Errorf("snapshot contracts: cannot serialize canonical JSON value of type %T", value)
	}
}

// writeCanonicalString escapes exactly the dialect's set and nothing else: not
// '/', not '<', '>' or '&', not U+2028 or U+2029.
func writeCanonicalString(buffer *bytes.Buffer, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("snapshot contracts: canonical JSON string is not valid UTF-8")
	}
	buffer.WriteByte('"')
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch character {
		case '"':
			buffer.WriteString(`\"`)
		case '\\':
			buffer.WriteString(`\\`)
		case '\b':
			buffer.WriteString(`\b`)
		case '\t':
			buffer.WriteString(`\t`)
		case '\n':
			buffer.WriteString(`\n`)
		case '\f':
			buffer.WriteString(`\f`)
		case '\r':
			buffer.WriteString(`\r`)
		default:
			if character < 0x20 {
				buffer.WriteString(`\u00`)
				const lowercaseHex = "0123456789abcdef"
				buffer.WriteByte(lowercaseHex[character>>4])
				buffer.WriteByte(lowercaseHex[character&0x0f])
				continue
			}
			buffer.WriteByte(character)
		}
	}
	buffer.WriteByte('"')
	return nil
}

// rejectUnpairedSurrogateEscapes is the second half of the no-replacement rule,
// and it has to run over the RAW bytes because encoding/json silently substitutes
// U+FFFD for an unpaired surrogate escape while decoding. By the time a decoded
// string is in hand, the two distinct inputs have already collapsed into one.
func rejectUnpairedSurrogateEscapes(document []byte) error {
	inString := false
	for index := 0; index < len(document); index++ {
		character := document[index]
		if !inString {
			if character == '"' {
				inString = true
			}
			continue
		}
		switch character {
		case '"':
			inString = false
		case '\\':
			if index+1 >= len(document) {
				return nil
			}
			if document[index+1] != 'u' {
				index++
				continue
			}
			codeUnit, ok := readHexCodeUnit(document, index+2)
			if !ok {
				// Malformed escape: the JSON decoder reports it precisely, so
				// there is nothing useful to add here.
				return nil
			}
			index += 5
			if !utf16.IsSurrogate(rune(codeUnit)) {
				continue
			}
			if codeUnit < 0xDC00 && index+6 < len(document) &&
				document[index+1] == '\\' && document[index+2] == 'u' {
				if low, ok := readHexCodeUnit(document, index+3); ok && low >= 0xDC00 && low <= 0xDFFF {
					index += 6
					continue
				}
			}
			return fmt.Errorf(
				"snapshot contracts: canonical JSON string contains the unpaired surrogate escape \\u%04x; replacing it with U+FFFD would map two distinct inputs to one canonical form",
				codeUnit,
			)
		}
	}
	return nil
}

func readHexCodeUnit(document []byte, start int) (uint32, bool) {
	if start+4 > len(document) {
		return 0, false
	}
	var value uint32
	for offset := 0; offset < 4; offset++ {
		digit := document[start+offset]
		switch {
		case digit >= '0' && digit <= '9':
			value = value<<4 | uint32(digit-'0')
		case digit >= 'a' && digit <= 'f':
			value = value<<4 | uint32(digit-'a'+10)
		case digit >= 'A' && digit <= 'F':
			value = value<<4 | uint32(digit-'A'+10)
		default:
			return 0, false
		}
	}
	return value, true
}
