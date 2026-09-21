package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"encoding/base64"
	"github.com/brine-dev/brine-go/pkg/brine"
	reviewclient "github.com/concourse/concourse/agent/review/client"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/pipelinerunserver"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runinput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type credentialAdmitter interface {
	SetCredentialHandoffConfig(runs.CredentialHandoffConfig)
	InspectCredentialHandoff(context.Context, runs.TemplateRef, runs.Principal, int, string, int64) (atc.RunCredentialSession, error)
	HandoffCredentials(context.Context, runs.TemplateRef, runs.Principal, int, string, int64, io.ReadCloser) (atc.RunCredentialSession, error)
}

func RunCredentialDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{brine.DefineMapUsing[RunOutputRuntime, RunOutputRuntime]("its owner-bound credential handoff encounters {string}", []string{"review-binaries", "review-workspace", "auth-server"}, func(in RunOutputRuntime, p brine.Params, rec *brine.Recorder, res brine.Resources) (RunOutputRuntime, error) {
		mode, _ := p.GetString(0)
		return in, exerciseRunCredential(in, mode, rec, res)
	})}
}

func exerciseRunCredential(in RunOutputRuntime, mode string, rec *brine.Recorder, res brine.Resources) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
	display, err := skycmd.NewSkyDisplayUserIdGenerator(nil)
	if err != nil {
		return err
	}
	basePort := runs.NewAdmitter(in.Start.DB.Conn, factory, in.Start.DB.TeamFactory, display, nil)
	port, ok := any(basePort).(credentialAdmitter)
	if !ok {
		return fmt.Errorf("Run admission has no owner-bound, one-use credential handoff")
	}
	team, _, err := in.Start.DB.TeamFactory.FindTeam("output-start")
	if err != nil {
		return err
	}
	if err = team.UpdateProviderAuth(atc.TeamAuth{"owner": {"users": {"local:owner", "local:other"}}}); err != nil {
		return err
	}
	ownerSubject := "local:owner"
	var httpAuth *AuthFixture
	if strings.HasPrefix(mode, "HTTP ") {
		if httpAuth, err = authServer(res); err != nil {
			return err
		}
		if _, err = httpAuth.fly("login", "-c", httpAuth.URL, "-n", "auth-team", "-u", "owner", "-p", authPassword); err != nil {
			return err
		}
		token, err := httpAuth.savedFlyToken()
		if err != nil {
			return err
		}
		request, _ := http.NewRequest(http.MethodGet, httpAuth.URL, nil)
		request.Header.Set("Authorization", token.Type+" "+token.Value)
		claims, err := httpAuth.Verifier.Verify(request)
		if err != nil {
			return err
		}
		ownerSubject, _ = claims["sub"].(string)
		if ownerSubject == "" {
			return fmt.Errorf("real login has no subject")
		}
	}
	template, _, err := team.Pipeline(atc.PipelineRef{Name: "review"})
	if err != nil {
		return err
	}
	// A credential is delivered only into an operator-pinned worker image, and
	// the Run snapshots its producer's image at creation.
	if mode != "unpinned producer image" {
		if template, err = pinProducerImage(in.Start.DB.TeamFactory, template, brineCredentialWorkerImage); err != nil {
			return err
		}
	}
	tx, err := in.Start.DB.Conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	in.Start.Creation, err = factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "owner", db.RunCreationOpts{ActivationEpoch: int64(hangarEpoch), Invocation: &db.RunInvocationIdentity{PrincipalDigest: runinput.PrincipalDigest(ownerSubject), KeyDigest: strings.Repeat("a", 64)}})
	if err != nil {
		db.Rollback(tx)
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	task := in.Start.Creation.Config.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
	in.Start.Plan = atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunResult: task.RunResult, Config: task.Config}
	if in.Start.Creation.EntryBuilds[0].JobName() != in.Start.Creation.Config.Jobs[0].Name {
		in.Start.Creation.EntryBuilds[0], in.Start.Creation.EntryBuilds[1] = in.Start.Creation.EntryBuilds[1], in.Start.Creation.EntryBuilds[0]
	}
	in.Control, err = in.prepare()
	if err != nil {
		return err
	}
	in.Start.Record, err = in.readSource()
	if err != nil {
		return err
	}
	build := in.Start.Creation.EntryBuilds[0]
	tx, err = in.Start.DB.Conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	a, _, err := factory.AdmitRunExecution(ctx, tx, db.RunExecutionRequest{BuildID: build.ID(), PlanID: "credential-step", Kind: db.ContainerTypeTask, Epoch: int64(hangarEpoch), NodeName: in.Node.Name, NodeUID: string(in.Node.UID), HandoffID: in.Start.Record.HandoffID})
	if err != nil {
		db.Rollback(tx)
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	pod, err := in.Client.CoreV1().Pods(in.Config.Namespace).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: "credential-"}, Spec: corev1.PodSpec{NodeName: in.Node.Name, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "main", Image: "busybox:1.37"}}}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	TrackDisposer(rec, "the credential pod "+pod.Name, func() error {
		return releasedIfGone(in.Client.CoreV1().Pods(pod.Namespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{}))
	})
	now := metav1.Now()
	pod.Status.Phase, pod.Status.StartTime = corev1.PodRunning, &now
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", ContainerID: "brine://" + freshUUID(), State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}}}}
	if _, err = in.Client.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
		return err
	}
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	if mode != "waiting for start" {
		start, err := client.RecordStart(ctx, a.Identity, executioncontrol.PodUID(pod.UID), "credential-process")
		if err != nil {
			return err
		}
		tx, err := in.Start.DB.Conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		keys := hangaroutput.ControlKeyRing{ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), Keys: []hangaroutput.ControlKeyEntry{{Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: base64.StdEncoding.EncodeToString(in.Start.Daemon.ControlPublic)}}}
		if err = factory.RecordRunExecutionWitness(ctx, tx, build.ID(), a.PlanID, start, keys); err != nil {
			db.Rollback(tx)
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		if mode == "closed execution" {
			finish, err := client.RecordOutcome(ctx, a.Identity, executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0})
			if err != nil {
				return err
			}
			tx, err := in.Start.DB.Conn.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			if err = factory.RecordRunExecutionWitness(ctx, tx, build.ID(), a.PlanID, finish, keys); err != nil {
				db.Rollback(tx)
				return err
			}
			if err = tx.Commit(); err != nil {
				return err
			}
		}
	}
	change, err := newReviewChange(res)
	if err != nil {
		return err
	}
	if _, stderr, err := reviewCommand(change.Binaries.CLI, change.captureArgs(change.Input), ""); err != nil {
		return fmt.Errorf("capture: %v: %s", err, stderr)
	}
	if err := change.memoryRuntime(); err != nil {
		return err
	}
	socket := filepath.Join(change.Workspace.Runtime, "auth.sock")
	change.RunID = in.Start.Creation.Run.ID()
	worker := exec.CommandContext(ctx, change.Binaries.Worker, "--input", change.Input, "--output", change.Output, "--runtime-dir", change.Workspace.Runtime, "--codex", change.Binaries.Provider, "--model", "handoff-wait", "--timeout", "20s", "--auth-socket", socket, "--handoff-timeout", "20s", "--run-id", strconv.Itoa(change.RunID))
	var workerErr bytes.Buffer
	worker.Stderr = &workerErr
	if err := worker.Start(); err != nil {
		return err
	}
	joined := false
	defer func() {
		if !joined {
			_ = worker.Process.Signal(os.Interrupt)
			_ = worker.Wait()
		}
	}()
	for {
		if st, err := os.Lstat(socket); err == nil && st.Mode()&os.ModeSocket != 0 {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("worker socket absent")
		case <-time.After(10 * time.Millisecond):
		}
	}
	source := in.source()
	source.SetExecutor(localExecutor{client: in.Client})
	pins := []string{brineCredentialWorkerImage}
	switch mode {
	case "a different pinned image":
		pins = []string{"registry.brine.test/other-worker@sha256:" + strings.Repeat("f", 64)}
	case "no pinned image":
		pins = nil
	case "a tag-only pin":
		pins = []string{strings.SplitN(brineCredentialWorkerImage, "@", 2)[0] + ":latest"}
	}
	port.SetCredentialHandoffConfig(runs.CredentialHandoffConfig{Source: source, Helper: change.Binaries.Worker, Socket: socket, Lifetime: time.Minute, WorkerImages: pins})
	if httpAuth != nil {
		err = exerciseCredentialHTTP(in, httpAuth, basePort, strings.TrimPrefix(mode, "HTTP "), rec)
		_ = worker.Process.Signal(os.Interrupt)
		_ = worker.Wait()
		joined = true
		if err != nil {
			return err
		}
		change.Stderr = workerErr.Bytes()
		return reviewNoCredentials(change)
	}
	ref := runs.TemplateRef{Team: team.Name(), Pipeline: template.PipelineRef()}
	principal := invocationPrincipal("owner")
	result := in.Start.Plan.RunResult.Name
	switch mode {
	case "foreign principal":
		principal = invocationPrincipal("other")
	case "same display name":
		principal = invocationPrincipal("other")
		principal.Claims["name"] = "owner"
	case "revoked role":
		err = team.UpdateProviderAuth(atc.TeamAuth{"viewer": {"users": {"local:owner"}}})
	case "unknown result":
		result = "missing"
	case "cancelled Run":
		_, err = acceptRunCancellation(in.Start, "owner", nil, false)
	case "aborted build":
		_, err = in.Start.DB.Conn.Exec(`UPDATE builds SET aborted=true WHERE id=$1`, build.ID())
	case "activation hold":
		_, err = in.Start.DB.Conn.Exec(`UPDATE pipeline_run_activation SET admission_enabled=false WHERE singleton`)
	case "claimed replay":
		_, err = in.Start.DB.Conn.Exec(`INSERT INTO pipeline_run_credential_handoffs(run_id,handoff_id) VALUES($1,$2)`, change.RunID, string(in.Start.Record.HandoffID))
	}
	if err != nil {
		return err
	}
	body := reviewSyntheticAuth
	if mode == "oversized input" {
		body = strings.Repeat("x", 65537)
	}
	input := bytes.NewReader([]byte(body))
	var state atc.RunCredentialSession
	if strings.HasSuffix(mode, "during input") {
		reader, writer := io.Pipe()
		defer reader.Close()
		defer writer.Close()
		done := make(chan error, 1)
		go func() {
			var err error
			state, err = port.HandoffCredentials(ctx, ref, principal, in.Start.Creation.Run.Number(), result, int64(hangarEpoch), reader)
			done <- err
		}()
		if _, err = writer.Write([]byte(body[:1])); err != nil {
			return err
		}
		if mode == "role revoked during input" {
			err = team.UpdateProviderAuth(atc.TeamAuth{"viewer": {"users": {"local:owner"}}})
		} else {
			_, err = acceptRunCancellation(in.Start, "owner", nil, false)
		}
		if err != nil {
			return err
		}
		if _, err = writer.Write([]byte(body[1:])); err != nil {
			return err
		}
		writer.Close()
		err = <-done
	} else if mode == "concurrent delivery" {
		in.Start.DB.Conn.SetMaxOpenConns(4)
		var states [2]atc.RunCredentialSession
		var errs [2]error
		var wg sync.WaitGroup
		for index := range states {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				states[i], errs[i] = port.HandoffCredentials(ctx, ref, principal, in.Start.Creation.Run.Number(), result, int64(hangarEpoch), io.NopCloser(strings.NewReader(body)))
			}(index)
		}
		wg.Wait()
		ready := 0
		for i, s := range states {
			if errs[i] != nil {
				return errs[i]
			}
			if s.Status == "ready" {
				ready++
			} else if s.Status != "claimed" {
				return fmt.Errorf("concurrent delivery: %s", s.Status)
			}
		}
		if ready == 0 {
			return fmt.Errorf("no concurrent delivery became ready")
		}
		state.Status = "ready"
	} else {
		state, err = port.HandoffCredentials(ctx, ref, principal, in.Start.Creation.Run.Number(), result, int64(hangarEpoch), io.NopCloser(input))
	}
	want := "refused"
	switch mode {
	case "accepted", "ready replay", "concurrent delivery":
		want = "ready"
	case "waiting for start":
		want = "waiting"
	case "claimed replay":
		want = "claimed"
	}
	if want == "refused" {
		if err == nil {
			return fmt.Errorf("%s was admitted: %s", mode, state.Status)
		}
	} else if err != nil || state.Status != want {
		return fmt.Errorf("%s: status=%s, err=%v", mode, state.Status, err)
	}
	var count int
	if err := in.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_credential_handoffs WHERE run_id=$1`, change.RunID).Scan(&count); err != nil {
		return err
	}
	wantCount := 0
	if want == "ready" || want == "claimed" {
		wantCount = 1
	}
	if count != wantCount {
		return fmt.Errorf("%s retained %d claims, want %d", mode, count, wantCount)
	}
	if want == "waiting" || want == "claimed" || mode == "foreign principal" || mode == "same display name" || mode == "revoked role" || mode == "unknown result" || mode == "cancelled Run" || mode == "aborted build" || mode == "closed execution" || mode == "activation hold" || unpinnedCredentialImage(mode) {
		if unpinnedCredentialImage(mode) && !errors.Is(err, runs.ErrCredentialDelivery) {
			return fmt.Errorf("%s was refused for another reason: %v", mode, err)
		}
		if input.Len() != len(body) {
			return fmt.Errorf("%s read credential bytes", mode)
		}
	}
	if mode == "ready replay" {
		input = bytes.NewReader([]byte("must not read or reseed"))
		fresh := runs.NewAdmitter(in.Start.DB.Conn, factory, in.Start.DB.TeamFactory, display, nil).(credentialAdmitter)
		replay, err := fresh.HandoffCredentials(ctx, ref, principal, in.Start.Creation.Run.Number(), result, int64(hangarEpoch), io.NopCloser(input))
		if err != nil || replay != state || input.Len() != len("must not read or reseed") {
			return fmt.Errorf("ready replay reseeded or lost identity: %v", err)
		}
	}
	files, err := filepath.Glob(filepath.Join(change.Workspace.Runtime, "jb-review-*", "codex", "auth.json"))
	if err != nil {
		return err
	}
	if want == "ready" {
		if len(files) != 1 {
			return fmt.Errorf("readiness left %d credential files", len(files))
		}
		data, err := os.ReadFile(files[0])
		if err != nil || string(data) != reviewSyntheticAuth {
			return fmt.Errorf("session did not stage owner credentials")
		}
	} else if len(files) != 0 {
		return fmt.Errorf("refused handoff staged credentials")
	}
	_ = worker.Process.Signal(os.Interrupt)
	_ = worker.Wait()
	joined = true
	change.Stderr = workerErr.Bytes()
	return reviewNoCredentials(change)
}

// unpinnedCredentialImage names the cases whose producer image no operator pin
// admits: the owner's credentials must not reach that container at all.
func unpinnedCredentialImage(mode string) bool {
	switch mode {
	case "unpinned producer image", "a different pinned image", "no pinned image", "a tag-only pin":
		return true
	}
	return false
}

func exerciseCredentialHTTP(in RunOutputRuntime, auth *AuthFixture, port runs.Admitter, mode string, rec *brine.Recorder) error {
	old := atc.EnablePipelineRunCreation
	atc.EnablePipelineRunCreation = mode != "operator hold"
	TrackDisposer(rec, "the pipeline-run creation setting", func() error { atc.EnablePipelineRunCreation = old; return nil })
	team, _, err := in.Start.DB.TeamFactory.FindTeam("output-start")
	if err != nil {
		return err
	}
	want := http.StatusOK
	switch mode {
	case "anonymous":
		want = http.StatusUnauthorized
	case "viewer":
		want = http.StatusForbidden
		if _, err = auth.fly("login", "-c", auth.URL, "-n", "auth-team", "-u", "viewer", "-p", authPassword); err != nil {
			return err
		}
	case "foreign owner":
		want = http.StatusForbidden
		if err = team.UpdateProviderAuth(atc.TeamAuth{"owner": {"users": {"local:owner", "local:viewer"}}}); err != nil {
			return err
		}
		if _, err = auth.fly("login", "-c", auth.URL, "-n", "auth-team", "-u", "viewer", "-p", authPassword); err != nil {
			return err
		}
	case "custom create role":
		want = http.StatusForbidden
		if err = team.UpdateProviderAuth(atc.TeamAuth{"member": {"users": {"local:owner"}}}); err != nil {
			return err
		}
		display, err := skycmd.NewSkyDisplayUserIdGenerator(nil)
		if err != nil {
			return err
		}
		port = runs.NewAdmitter(in.Start.DB.Conn, db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory), in.Start.DB.TeamFactory, display, map[string]string{atc.CreatePipelineRunV2: "owner"})
	case "operator hold":
		want = http.StatusConflict
	}
	auth.mu.Lock()
	auth.RunServices = pipelinerunserver.Services{Admitter: port, Epoch: int64(hangarEpoch)}
	auth.API, err = auth.apiHandler(auth.Verifier)
	auth.mu.Unlock()
	if err != nil {
		return err
	}
	token, err := auth.savedFlyToken()
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/api/v2/teams/output-start/pipelines/review/runs/%d/credentials/%s", in.Start.Creation.Run.Number(), in.Start.Plan.RunResult.Name)
	if strings.HasPrefix(mode, "shared client") {
		makeClient := func() (*reviewclient.Client, error) {
			transport := oauth2.NewClient(context.WithValue(context.Background(), oauth2.HTTPClient, auth.Client), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token.Value, TokenType: token.Type}))
			return reviewclient.New(auth.URL, transport)
		}
		client, err := makeClient()
		if err != nil {
			return err
		}
		handle := reviewclient.Handle{Team: "output-start", Template: "review", Number: in.Start.Creation.Run.Number()}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		state, err := client.CredentialSession(ctx, handle, in.Start.Plan.RunResult.Name)
		if err != nil || state.Status != "available" || state.RunID != in.Start.Creation.Run.ID() {
			return fmt.Errorf("shared client did not discover its available session: %v", err)
		}
		state, err = client.HandoffCredentials(ctx, handle, in.Start.Plan.RunResult.Name, io.NopCloser(strings.NewReader(reviewSyntheticAuth)))
		if err != nil || state.Status != "ready" {
			return fmt.Errorf("shared client handoff: %v", err)
		}
		fresh, err := makeClient()
		if err != nil {
			return err
		}
		var replay atc.RunCredentialSession
		if mode == "shared client replay" {
			replay, err = fresh.HandoffCredentials(ctx, handle, in.Start.Plan.RunResult.Name, io.NopCloser(strings.NewReader("not credentials")))
		} else {
			replay, err = fresh.CredentialSession(ctx, handle, in.Start.Plan.RunResult.Name)
		}
		if err != nil || replay != state {
			return fmt.Errorf("fresh client lost credential readiness: %v", err)
		}
		var count int
		if err := in.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_credential_handoffs WHERE run_id=$1`, state.RunID).Scan(&count); err != nil || count != 1 {
			return fmt.Errorf("shared client did not retain exactly one handoff: %v", err)
		}
		return nil
	}
	send := func(method, body string) (int, atc.RunCredentialSession, error) {
		var state atc.RunCredentialSession
		r, err := http.NewRequest(method, auth.URL+path, strings.NewReader(body))
		if err != nil {
			return 0, state, err
		}
		r.Header.Set("Content-Type", "application/json")
		if mode != "anonymous" {
			r.Header.Set("Authorization", token.Type+" "+token.Value)
		}
		response, err := auth.Client.Do(r)
		if err != nil {
			return 0, state, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 4097))
		if err != nil {
			return 0, state, err
		}
		if bytes.Contains(data, []byte("synthetic-")) {
			return 0, state, fmt.Errorf("credential leaked in HTTP response")
		}
		if response.StatusCode == http.StatusOK {
			if response.Header.Get("Cache-Control") != "private, no-store" || json.Unmarshal(data, &state) != nil {
				return 0, state, fmt.Errorf("invalid credential receipt")
			}
		}
		return response.StatusCode, state, nil
	}
	status, state, err := send(http.MethodPost, reviewSyntheticAuth)
	if err != nil || status != want {
		return fmt.Errorf("%s: HTTP %d, want %d: %v", mode, status, want, err)
	}
	if want == http.StatusOK {
		if state.Status != "ready" || state.RunID != in.Start.Creation.Run.ID() || state.Result != in.Start.Plan.RunResult.Name {
			return fmt.Errorf("HTTP handoff lost its ready Run identity")
		}
		if mode == "ready replay" {
			auth.Client.CloseIdleConnections()
			status, replay, err := send(http.MethodPost, "not credentials")
			if err != nil || status != http.StatusOK || replay != state {
				return fmt.Errorf("HTTP replay changed ready receipt: %v", err)
			}
		}
		status, replay, err := send(http.MethodGet, "")
		if err != nil || status != http.StatusOK || replay != state {
			return fmt.Errorf("fresh HTTP status lost readiness: %v", err)
		}
	}
	var count int
	if err := in.Start.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_credential_handoffs WHERE run_id=$1`, in.Start.Creation.Run.ID()).Scan(&count); err != nil {
		return err
	}
	if (want == http.StatusOK && count != 1) || (want != http.StatusOK && count != 0) {
		return fmt.Errorf("HTTP handoff left %d claims", count)
	}
	return nil
}
