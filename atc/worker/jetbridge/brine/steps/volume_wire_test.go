package steps

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	frames "github.com/moby/spdystream/spdy"
)

// These are parser inputs, not an executor or a Kubernetes response fixture.
// The live scenarios separately establish that captures come from real exec.
func TestVolumeWirePayload(t *testing.T) {
	encode := func(list ...frames.Frame) []byte {
		t.Helper()
		var out bytes.Buffer
		f, err := frames.NewFramer(&out, bytes.NewReader(nil))
		if err != nil {
			t.Fatal(err)
		}
		for _, frame := range list {
			if err := f.WriteFrame(frame); err != nil {
				t.Fatal(err)
			}
		}
		return out.Bytes()
	}
	declare := func(kind string, id frames.StreamId) *frames.SynStreamFrame {
		return &frames.SynStreamFrame{StreamId: id, Headers: http.Header{"streamType": {kind}}}
	}
	data := func(id frames.StreamId, fin bool, payload string) *frames.DataFrame {
		f := &frames.DataFrame{StreamId: id, Data: []byte(payload)}
		if fin {
			f.Flags = frames.DataFlagFin
		}
		return f
	}
	for _, kind := range []string{"stdin", "stdout"} {
		t.Run(kind, func(t *testing.T) {
			headers := encode(declare(kind, 3), declare("stderr", 5))
			payload := []byte("opaque bytes including tar padding\x00\x00")
			for _, tc := range []struct {
				name    string
				body    []byte
				tail    []byte
				wantErr string
			}{
				{"complete", encode(data(3, true, string(payload))), nil, ""},
				{"split data", encode(data(5, true, "not artifact"), data(3, false, "opaque bytes "), data(3, true, "including tar padding\x00\x00")), nil, ""},
				{"closed control", encode(data(3, true, string(payload))), []byte{0x80, 0x03}, ""},
				{"control header ends at field boundary", encode(data(3, true, string(payload))), []byte{0x80, 0x03, 0x00, 0x07}, ""},
				{"missing FIN", encode(data(3, false, string(payload))), nil, "no FIN"},
				{"missing FIN with closing control", encode(data(3, false, string(payload))), []byte{0x80, 0x03}, "no FIN"},
				{"DATA after FIN", encode(data(3, true, string(payload)), data(3, true, "more")), nil, "DATA after"},
				{"truncated DATA", encode(data(3, true, string(payload)))[:10], nil, "incomplete"},
				{"truncated DATA after FIN", encode(data(3, true, string(payload))), []byte{0x00, 0x00, 0x00}, "incomplete"},
				{"single byte tail", encode(data(3, true, string(payload))), []byte{0x00}, "incomplete"},
				{"other stream only", encode(data(5, true, string(payload))), nil, "no FIN"},
				{"empty", encode(data(3, true, "")), nil, "empty"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					body := append(append([]byte(nil), tc.body...), tc.tail...)
					sent, received := headers, body
					if kind == "stdin" {
						sent = append(append([]byte(nil), headers...), body...)
						received = nil
					}
					actual, err := wirePayload(sent, received, kind)
					if tc.wantErr != "" {
						if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
							t.Fatalf("got %v, want %s", err, tc.wantErr)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(actual, payload) {
						t.Fatalf("got %q, want %q", actual, payload)
					}
				})
			}
		})
	}
	t.Run("missing declaration", func(t *testing.T) {
		if _, err := wirePayload(nil, encode(data(3, true, "data")), "stdout"); err == nil {
			t.Fatal("accepted undeclared stream")
		}
	})
	t.Run("duplicate declaration", func(t *testing.T) {
		if _, err := wirePayload(encode(declare("stdout", 3), declare("stdout", 7)), nil, "stdout"); err == nil {
			t.Fatal("accepted duplicate stream")
		}
	})
	t.Run("byte oracle detects padding", func(t *testing.T) {
		if err := requireSameVolumeBytes([]byte("tar\x00"), []byte("tar")); err == nil {
			t.Fatal("ignored trailing byte")
		}
		if err := requireSameVolumeBytes([]byte("tar"), []byte("tar")); err != nil {
			t.Fatal(err)
		}
	})
}
