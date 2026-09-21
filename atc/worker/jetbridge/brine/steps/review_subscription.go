package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/agent/review"
)

type ReviewSubscription struct {
	Report *review.Report
}

// This feature is opt-in and uses prebuilt production binaries. The launcher
// must stage auth in a disposable container's tmpfs, never a repository mount.
func ReviewSubscriptionDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, ReviewSubscription]("the authorized subscription reviews the private first-byte regression", func(_ brine.Empty, _ brine.Params, _ *brine.Recorder) (ReviewSubscription, error) {
			return runReviewSubscription()
		}),
		CheckThat[ReviewSubscription]("the live review locates the regression with verified provenance and no credentials", func(in ReviewSubscription) error {
			r := in.Report
			if r == nil || r.Verdict != "findings" || !r.Assessment.Complete {
				return errors.New("live review did not complete with findings")
			}
			if r.Provenance.ModelRequested != os.Getenv("BRINE_REVIEW_LIVE_MODEL") || r.Provenance.CodexVersion != "codex-cli "+review.CodexVersion() || r.Provenance.ExecutionPolicy != "inspect-only" {
				return errors.New("live review provenance does not match the requested execution")
			}
			if !strings.Contains(strings.Join(r.Assessment.Limitations, " "), "not executed") {
				return errors.New("live review lacks non-execution disclosure")
			}
			for _, f := range r.Assessment.Findings {
				if f.ID != "" && f.Location.Side == "head" && f.Location.Path == "parser.go" && f.Location.StartLine <= 2 && f.Location.EndLine >= 2 {
					return nil
				}
			}
			return errors.New("live review did not locate the seeded first-byte regression")
		}),
	}
}

func runReviewSubscription() (ReviewSubscription, error) {
	var result ReviewSubscription
	root, model := os.Getenv("BRINE_REVIEW_LIVE_ROOT"), os.Getenv("BRINE_REVIEW_LIVE_MODEL")
	if root == "" || model == "" {
		return result, errors.New("live subscription acceptance requires explicit BRINE_REVIEW_LIVE_ROOT and BRINE_REVIEW_LIVE_MODEL")
	}
	// Fixed tmpfs transport path keeps credential material out of Brine resources,
	// recorder events, environment values, command arguments and durable mounts.
	const authPath = "/dev/shm/review-live-auth/auth.json"
	defer os.Remove(authPath)
	bundle, err := review.LoadBundle(filepath.Join(root, "input"))
	if err != nil {
		return result, err
	}
	auth, err := os.ReadFile(authPath)
	if err != nil {
		return result, errors.New("live subscription credential handoff is absent")
	}
	if err := os.Remove(authPath); err != nil {
		return result, err
	}
	var authFields map[string]any
	if json.Unmarshal(auth, &authFields) != nil {
		return result, errors.New("live subscription credentials are not JSON")
	}
	secrets := reviewSubscriptionSecrets(authFields)
	runtime, err := os.MkdirTemp("/dev/shm", "review-live-session-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(runtime)
	output := filepath.Join(root, "output", "report")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(root, "bin", "jb-review-worker"),
		"--input", bundle.Dir, "--output", output, "--runtime-dir", runtime,
		"--codex", filepath.Join(root, "bin", "codex"), "--model", model, "--timeout", "4m", "--auth-stdin")
	cmd.Stdin = bytes.NewReader(auth)
	log, runErr := cmd.CombinedOutput()
	entries, err := os.ReadDir(runtime)
	if err != nil || len(entries) != 0 {
		return result, errors.New("live worker left credential session files behind")
	}
	if containsReviewSubscriptionSecret(log, secrets) {
		return result, errors.New("live worker output contained credentials; output suppressed")
	}
	if runErr != nil {
		return result, fmt.Errorf("live worker failed: %w: %s", runErr, log)
	}
	after, err := review.LoadBundle(bundle.Dir)
	if err != nil || after.Digest != bundle.Digest {
		return result, errors.New("live review changed the captured input")
	}
	for _, name := range []string{"review.json", "review.md"} {
		data, err := os.ReadFile(filepath.Join(output, name))
		if err != nil {
			return result, err
		}
		if containsReviewSubscriptionSecret(data, secrets) {
			os.RemoveAll(output)
			return result, errors.New("live report contained credentials; report removed")
		}
		if name == "review.json" {
			result.Report, err = review.ParseReport(data, bundle)
			if err != nil {
				return result, err
			}
		} else if string(data) != result.Report.Markdown() {
			return result, errors.New("live Markdown differs from the validated report")
		}
	}
	return result, nil
}

func reviewSubscriptionSecrets(fields map[string]any) [][]byte {
	var values [][]byte
	for _, value := range fields {
		switch v := value.(type) {
		case string:
			if len(v) >= 12 {
				values = append(values, []byte(v))
			}
		case map[string]any:
			values = append(values, reviewSubscriptionSecrets(v)...)
		}
	}
	return values
}

func containsReviewSubscriptionSecret(data []byte, secrets [][]byte) bool {
	for _, secret := range secrets {
		if bytes.Contains(data, secret) {
			return true
		}
	}
	return false
}
