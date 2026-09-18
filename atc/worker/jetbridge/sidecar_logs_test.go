package jetbridge

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestSidecarLogFormatting(t *testing.T) {
	payload := "first\n\n" + strings.Repeat("x", 70*1024) + "\ntail"
	want := "[helper] " + strings.ReplaceAll(payload, "\n", "\n[helper] ")
	for _, chunk := range []int{1, 3, 32 * 1024, len(payload)} {
		t.Run(fmt.Sprint(chunk), func(t *testing.T) {
			var out bytes.Buffer
			writer := &prefixedLogWriter{writer: &out, prefix: "[helper] ", lineStart: true}
			for start := 0; start < len(payload); start += chunk {
				end := min(start+chunk, len(payload))
				n, err := writer.Write([]byte(payload[start:end]))
				if err != nil || n != end-start {
					t.Fatalf("write: %d, %v", n, err)
				}
				// No flush/EOF required: partial lines must already be visible.
				prefix := payload[:end]
				expected := "[helper] " + strings.ReplaceAll(prefix, "\n", "\n[helper] ")
				if strings.HasSuffix(prefix, "\n") {
					expected = strings.TrimSuffix(expected, "[helper] ")
				}
				if out.String() != expected {
					t.Fatalf("incorrect prefix at input byte %d", end)
				}
			}
			if out.String() != want {
				t.Fatal("line formatting changed")
			}
		})
	}
	t.Run("shared stdout", func(t *testing.T) {
		var out bytes.Buffer
		writer := &serializedLogWriter{writer: &out}
		var wg sync.WaitGroup
		for _, text := range []string{"main\n", "[one] first\n", "[two] second\n"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 100 {
					if _, err := writer.Write([]byte(text)); err != nil {
						t.Error(err)
					}
				}
			}()
		}
		wg.Wait()
		for _, text := range []string{"main\n", "[one] first\n", "[two] second\n"} {
			if strings.Count(out.String(), text) != 100 {
				t.Fatalf("lost shared output %q", text)
			}
		}
	})
}
