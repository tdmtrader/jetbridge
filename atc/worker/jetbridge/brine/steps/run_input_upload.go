package steps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
	"google.golang.org/api/iterator"
)

func RunInputUploadDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, HangarDaemon]("a managed input upload node", func(_ brine.Empty, _ brine.Params, rec *brine.Recorder) (HangarDaemon, error) {
			return startHangarDaemon(rec, true)
		}),
		brine.DefineMap[HangarDaemon, HangarDaemon]("its input upload encounters {string}", func(in HangarDaemon, p brine.Params, _ *brine.Recorder) (HangarDaemon, error) {
			mode, _ := p.GetString(0)
			return in, exerciseInputUpload(in, mode)
		}),
	}
}

func exerciseInputUpload(in HangarDaemon, mode string) error {
	if mode == "expired stage" {
		if err := in.Output.cmd.Process.Kill(); err != nil {
			return err
		}
		<-in.Output.done
		in.Output.cmd.Args = append(in.Output.cmd.Args, "--output-timeout", "500ms")
		if err := in.Output.restart(in.Ctx, in.HTTP); err != nil {
			return err
		}
	}
	archive, err := durableTarOfOneFile("manifest.json", "committed review bundle")
	if err != nil {
		return err
	}
	call := func(client *http.Client, path string, body []byte) (int, []byte, error) {
		req, err := http.NewRequestWithContext(in.Ctx, http.MethodPost, in.Output.URL+path, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		response, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
		return response.StatusCode, data, err
	}
	if mode == "malformed archive" {
		archive = []byte("not a tar")
	}
	client := in.HTTP
	if mode == "missing peer" {
		transport := in.HTTP.Transport.(*http.Transport).Clone()
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		transport.TLSClientConfig.Certificates = nil
		defer transport.CloseIdleConnections()
		client = &http.Client{Transport: transport, Timeout: time.Second}
	}
	status, data, err := call(client, "/input/v1/stage", archive)
	if err != nil {
		return err
	}
	if mode == "missing peer" || mode == "malformed archive" {
		want := http.StatusBadRequest
		if mode == "missing peer" {
			want = http.StatusForbidden
		}
		if status != want {
			return fmt.Errorf("input stage refusal: got %d, want %d: %s", status, want, data)
		}
		return assertInputObjectCount(in, 0)
	}
	var stage output.InputStage
	if status != http.StatusOK || json.Unmarshal(data, &stage) != nil || stage.Validate() != nil {
		return fmt.Errorf("node did not stage an input: HTTP %d: %s", status, data)
	}
	if stage.NodeUID != executioncontrol.NodeUID(in.NodeUID) {
		return fmt.Errorf("stage names another node")
	}
	if err := assertInputObjectCount(in, 0); err != nil {
		return fmt.Errorf("staging created an object before logical reservation: %w", err)
	}
	if err := assertInputStages(in, 1); err != nil {
		return err
	}
	request := output.InputPublishRequest{Version: output.InputPublicationVersion, ReservationID: stage.ReservationID, Nonce: uuid.NewString()}
	if mode == "unknown stage" {
		request.ReservationID = output.ReservationID(uuid.NewString())
	}
	if mode == "invalid nonce" {
		request.Nonce = "invalid"
	}
	if mode == "node restart" {
		if err := in.Output.cmd.Process.Kill(); err != nil {
			return err
		}
		<-in.Output.done
		if err := in.Output.restart(in.Ctx, in.HTTP); err != nil {
			return err
		}
	}
	if mode == "expired stage" {
		<-time.After(time.Until(stage.ExpiresAt.Time) + 100*time.Millisecond)
	}
	body, _ := json.Marshal(request)
	if mode == "caller bucket" {
		var value map[string]any
		_ = json.Unmarshal(body, &value)
		value["bucket"] = "caller-must-not-select"
		body, _ = json.Marshal(value)
	}
	status, data, err = call(in.HTTP, "/input/v1/publish", body)
	if err != nil {
		return err
	}
	if mode != "publish" && mode != "deduplicate" {
		want := http.StatusNotFound
		if mode == "invalid nonce" || mode == "caller bucket" {
			want = http.StatusBadRequest
		}
		if status != want {
			return fmt.Errorf("input publish refusal: got %d, want %d: %s", status, want, data)
		}
		if mode == "expired stage" || mode == "node restart" {
			if err := assertInputStages(in, 0); err != nil {
				return err
			}
		}
		return assertInputObjectCount(in, 0)
	}
	var receipt output.InputPublication
	if status != http.StatusOK || json.Unmarshal(data, &receipt) != nil || receipt.Validate() != nil || receipt.Stage != stage || receipt.Nonce != request.Nonce {
		return fmt.Errorf("input publication failed: HTTP %d: %s", status, data)
	}
	ring, err := output.NewReceiptKeyRing(output.EpochKey{KeyID: hangarReceiptKeyID, Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: in.ReceiptPublic, ValidFrom: output.NewTimestamp(time.Now().Add(-time.Hour)), ValidUntil: output.NewTimestamp(time.Now().Add(time.Hour))})
	if err != nil {
		return err
	}
	verifier, err := output.NewReceiptSignatureVerifier(ring, output.ClockFunc(time.Now))
	if err != nil {
		return err
	}
	inputVerifier, ok := any(verifier).(interface {
		VerifyInputPublication(output.InputPublication, output.InputStage, string) error
	})
	if !ok {
		return fmt.Errorf("input publication has no signature verifier")
	}
	if err := inputVerifier.VerifyInputPublication(receipt, stage, request.Nonce); err != nil {
		return fmt.Errorf("input publication signature failed: %w", err)
	}
	altered := receipt
	altered.Attributes.Ref.Generation++
	if inputVerifier.VerifyInputPublication(altered, stage, request.Nonce) == nil || inputVerifier.VerifyInputPublication(receipt, stage, uuid.NewString()) == nil {
		return fmt.Errorf("changed publication identity or nonce retained authority")
	}
	canonical, err := (hangar.Canonicalizer{}).Capture(in.Ctx, bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer canonical.Close()
	if canonical.Digest != receipt.Attributes.Ref.Digest || canonical.ByteSize != receipt.Attributes.LogicalBytes {
		return fmt.Errorf("input publication changed canonical bytes")
	}
	if err := assertInputObjectCount(in, 1); err != nil {
		return err
	}
	if err := assertInputStages(in, 0); err != nil {
		return err
	}
	objects := in.Client.Bucket(in.OutputBucket).Objects(in.Ctx, nil)
	object, err := objects.Next()
	if err != nil {
		return err
	}
	reader, err := in.Client.Bucket(in.OutputBucket).Object(object.Name).Generation(receipt.Attributes.Ref.Generation).NewReader(in.Ctx)
	if err != nil {
		return err
	}
	actual, err := io.ReadAll(reader)
	reader.Close()
	want, readErr := os.ReadFile(canonical.ArchivePath)
	if err != nil || readErr != nil || !bytes.Equal(actual, want) {
		return fmt.Errorf("published exact object differs from canonical upload")
	}
	if mode == "deduplicate" {
		node := jetbridge.NewOutputControlClient(in.Output.URL, in.HTTP, in.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
		again, err := node.StageInput(in.Ctx, executioncontrol.NodeUID(in.NodeUID), bytes.NewReader(archive))
		if err != nil {
			return fmt.Errorf("stage through production client: %w", err)
		}
		duplicate, err := node.PublishInput(in.Ctx, again, uuid.NewString(), verifier)
		if err != nil || duplicate.Attributes.Ref != receipt.Attributes.Ref {
			return fmt.Errorf("identical uploaded bytes did not retain one exact generation: %v", err)
		}
		return assertInputObjectCount(in, 1)
	}
	return nil
}

func assertInputStages(in HangarDaemon, want int) error {
	paths, err := filepath.Glob(filepath.Join(in.Output.Root, "scratch", hangar.CanonicalizerTempPrefix+"*"))
	if err != nil {
		return err
	}
	if len(paths) != want {
		return fmt.Errorf("node retained %d canonical stages, want %d", len(paths), want)
	}
	return nil
}

func assertInputObjectCount(in HangarDaemon, want int) error {
	count := 0
	objects := in.Client.Bucket(in.OutputBucket).Objects(in.Ctx, nil)
	for {
		_, err := objects.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return err
		}
		count++
	}
	if count != want {
		return fmt.Errorf("input bucket holds %d objects, want %d", count, want)
	}
	return nil
}
