package steps

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

// NewStubVolume is production's resource-cache placeholder, not a test double.
// Its refusal contract is independent of whether a caller requests compression.
type PlaceholderVolume struct {
	Volume *jetbridge.Volume
}

type PlaceholderAttempt struct {
	Encoding string
	Err      error
}

type PlaceholderIO struct {
	Volume   *jetbridge.Volume
	Attempts []PlaceholderAttempt
}

func PlaceholderVolumeDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, PlaceholderVolume](
			"a resource-cache placeholder volume",
			func(brine.Empty, brine.Params, *brine.Recorder) (PlaceholderVolume, error) {
				return PlaceholderVolume{Volume: jetbridge.NewStubVolume("rc-42", "k8s-worker", "")}, nil
			},
		),
		Transform[PlaceholderVolume, PlaceholderIO](
			"the placeholder is asked to {string} with and without compression",
			func(in PlaceholderVolume, args Args) (PlaceholderIO, error) {
				operation := args.String(0)
				if operation != "read" && operation != "write" {
					return PlaceholderIO{}, fmt.Errorf("unknown placeholder operation %q", operation)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				out := PlaceholderIO{Volume: in.Volume}
				for _, mode := range []struct {
					name string
					enc  compression.Compression
				}{{"raw", nil}, {"gzip", compression.NewGzipCompression()}} {
					var operationErr error
					if operation == "read" {
						stream, err := in.Volume.StreamOut(ctx, ".", mode.enc)
						operationErr = err
						if stream != nil {
							if err := stream.Close(); err != nil {
								return PlaceholderIO{}, fmt.Errorf("close unexpected placeholder stream: %w", err)
							}
						}
					} else {
						// The raw caller deliberately supplies the original Go
						// test's bytes. Refusal must precede archive decoding.
						var reader io.Reader = strings.NewReader("data")
						if mode.enc != nil {
							var err error
							reader, err = tarOfOneFile("probe.txt", "probe")
							if err != nil {
								return PlaceholderIO{}, err
							}
						}
						operationErr = in.Volume.StreamIn(ctx, ".", mode.enc, 0, reader)
					}
					out.Attempts = append(out.Attempts, PlaceholderAttempt{Encoding: mode.name, Err: operationErr})
				}
				return out, nil
			},
		),
		Assert[PlaceholderIO](
			"it reports no executor and refuses {string}",
			func(in PlaceholderIO, args Args) error {
				operation := args.String(0)
				direction := "out"
				switch operation {
				case "read":
				case "write":
					direction = "in"
				default:
					return fmt.Errorf("unknown placeholder operation %q", operation)
				}
				if in.Volume.HasExecutor() {
					return fmt.Errorf("the placeholder reports an executor")
				}
				if len(in.Attempts) != 2 || in.Attempts[0].Encoding != "raw" || in.Attempts[1].Encoding != "gzip" {
					return fmt.Errorf("placeholder refusal must observe both raw and gzip calls")
				}
				for _, attempt := range in.Attempts {
					if attempt.Err == nil {
						return fmt.Errorf("placeholder %s %s succeeded instead of refusing I/O", attempt.Encoding, operation)
					}
					for _, want := range []string{"cannot stream " + direction, "no executor"} {
						if !strings.Contains(attempt.Err.Error(), want) {
							return fmt.Errorf("placeholder %s %s error must contain %q, got %q",
								attempt.Encoding, operation, want, attempt.Err.Error())
						}
					}
				}
				return nil
			},
		),
	}
}
