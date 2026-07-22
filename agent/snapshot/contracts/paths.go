package contracts

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

func validatePOSIXPath(label, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", label)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s must be valid UTF-8 without NUL", label)
	}
	if strings.Contains(value, `\`) || path.IsAbs(value) || path.Clean(value) != value || value == "." || strings.HasSuffix(value, "/") {
		return fmt.Errorf("%s must be a canonical relative POSIX path", label)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." || driveLikeSegment(segment) {
			return fmt.Errorf("%s must be a canonical relative POSIX path", label)
		}
	}
	return nil
}

func driveLikeSegment(segment string) bool {
	return len(segment) >= 2 && ((segment[0] >= 'a' && segment[0] <= 'z') || (segment[0] >= 'A' && segment[0] <= 'Z')) && segment[1] == ':'
}

func requireRegularPayload(root *os.Root, label, name string) error {
	if err := validatePOSIXPath(label, name); err != nil {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s %q is missing", label, name)
		}
		return fmt.Errorf("inspect %s %q: %w", label, name, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s %q must be a regular file", label, name)
	}
	return nil
}
