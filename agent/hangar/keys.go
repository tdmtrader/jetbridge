package hangar

import (
	"fmt"
	"strings"
)

const digestPrefix = "sha256:"

func (kind Kind) Validate() error {
	switch kind {
	case KindSnapshot, KindCheckpoint:
		return nil
	default:
		return fmt.Errorf("hangar: invalid object kind %q", kind)
	}
}

func (digest Digest) Validate() error {
	raw := string(digest)
	if len(raw) != len(digestPrefix)+64 || !strings.HasPrefix(raw, digestPrefix) {
		return fmt.Errorf("hangar: digest must be sha256 followed by 64 lowercase hexadecimal characters")
	}
	for _, character := range raw[len(digestPrefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return fmt.Errorf("hangar: digest must be sha256 followed by 64 lowercase hexadecimal characters")
		}
	}
	return nil
}

func Key(kind Kind, digest Digest) (string, error) {
	if err := kind.Validate(); err != nil {
		return "", err
	}
	if err := digest.Validate(); err != nil {
		return "", err
	}
	return "hangar/v1/" + string(kind) + "/sha256/" +
		strings.TrimPrefix(string(digest), digestPrefix) + ".tar.zst", nil
}

func NewObjectRef(kind Kind, digest Digest, generation int64) (ObjectRef, error) {
	key, err := Key(kind, digest)
	if err != nil {
		return ObjectRef{}, err
	}
	if generation <= 0 {
		return ObjectRef{}, fmt.Errorf("hangar: object generation must be positive")
	}
	return ObjectRef{
		Kind:       kind,
		Digest:     digest,
		Key:        key,
		Generation: generation,
	}, nil
}

func (ref ObjectRef) Validate() error {
	key, err := Key(ref.Kind, ref.Digest)
	if err != nil {
		return err
	}
	if ref.Generation <= 0 {
		return fmt.Errorf("hangar: object generation must be positive")
	}
	if ref.Key != key {
		return fmt.Errorf("hangar: object key does not match kind and digest")
	}
	return nil
}
