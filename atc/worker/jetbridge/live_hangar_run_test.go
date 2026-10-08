//go:build live
// +build live

package jetbridge_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/go-concourse/concourse"
	"github.com/concourse/concourse/hangar"
	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The Hangar checks of hangar_stores_enabled_in_cluster (B2): the disk store's
// input namespace behind a strict input, and its output namespace behind a v2
// Run's managed input and captured result.
//
// Each check reads the deployment manifest first. While every feature it
// covers expects off it skips, so the tier stays green until the rollout turns
// the feature on; once one expects on, every missing precondition fails it.

const (
	// liveHangarRunPipeline is fixed rather than per-invocation: a re-run
	// downloads the earlier Runs' results before it makes its own, which is how
	// a store restart between two runs of the tier is checked.
	liveHangarRunPipeline = "live-hangar-run"
	liveHangarRunTaskID   = "6f1d3c2a-8b4e-4c5d-9a7f-2e1b0c3d4e5f"
	liveHangarRunUser     = "live-tests"
	liveHangarScope       = "live"
)

// liveHangarRequire skips the check while every feature it covers expects
// off, and otherwise returns. A feature missing from the manifest counts as
// off; TestLiveDeployedFeatureSet is what refuses an unlisted observer.
func liveHangarRequire(t *testing.T, features ...string) {
	t.Helper()
	manifest := loadLiveManifest(t)
	for _, feature := range features {
		if manifest.Features[feature].Expect == "on" {
			return
		}
	}
	t.Skipf("%s all expect off in the live deployment manifest", strings.Join(features, ", "))
}

// TestLiveHangarStrictInputFromTheInputNamespace publishes a tree through the
// deployed artifact daemon into the disk store's input namespace, then runs a
// task whose only input is that tree as a strict input. The task's pod is the
// runtime's own, built by an in-process worker from the deployed
// configuration: the ATC hands a job build no Hangar tree, so this is the path
// a strict input takes on a deployed cluster.
func TestLiveHangarStrictInputFromTheInputNamespace(t *testing.T) {
	liveHangarRequire(t, "daemon.hangar", "web.hangar", "store.disk")
	if _, on := deployed.daemonFlag("hangar-enabled"); !on {
		t.Fatal("the manifest expects strict inputs on, but the artifact daemon runs without --hangar-enabled")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	clientset, cfg := kubeClient(t)
	signer := liveHangarSigner(t, ctx, clientset)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	payload := "strict input " + suffix
	tree := liveHangarTar(t, map[string]string{"payload": payload, "nested/second": "second"})
	daemon := liveHangarDaemonClient(t, ctx, clientset)
	published := daemon.publish(t, ctx, tree, http.StatusCreated)
	if published.Ref.Scope != liveHangarScope || published.Ref.Generation <= 0 {
		t.Fatalf("publication attributes = %+v", published)
	}
	if again := daemon.publish(t, ctx, tree, http.StatusOK); again.Ref != published.Ref {
		t.Fatalf("republishing the same tree returned %+v, want %+v", again.Ref, published.Ref)
	}

	worker, delegate, _, _ := newLiveWorker(t, nil, nil, func(c *jetbridge.Config) {
		c.HangarEnabled = true
		c.HangarSigner = signer
	})
	handle := "live-hangar-strict-" + suffix
	cleanupPod(t, clientset, cfg.Namespace, handle)
	ref := published.Ref
	container, _, err := worker.FindOrCreateContainer(ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			Dir:       "/work",
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
			Inputs:    []runtime.Input{{HangarTree: &ref, DestinationPath: "/work/exact"}},
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("create the strict-input task: %v", err)
	}
	script := fmt.Sprintf(`set -eu
test "$(cat /work/exact/payload)" = '%s'
test "$(cat /work/exact/nested/second)" = 'second'
test -f /work/exact/.hangar-materialized
if touch /work/exact/must-not-write 2>/dev/null; then exit 91; fi
`, payload)
	var stderr bytes.Buffer
	process, err := container.Run(ctx, runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", script}},
		runtime.ProcessIO{Stderr: &stderr})
	if err != nil {
		t.Fatalf("run the strict-input task: %v", err)
	}
	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("wait for the strict-input task: %v", err)
	}
	if result.ExitStatus != 0 {
		t.Fatalf("the strict-input task exited %d: %s", result.ExitStatus, stderr.String())
	}
}

// TestLiveHangarRunConsumesAManagedInputAndDownloadsItsResult logs in as the
// live-tests user, sets a template whose task copies a managed input into its
// declared result, and creates a v2 Run with an uploaded input. The task
// copies the two files by name: a materialized input is read-only, and a
// recursive copy would carry that onto directories it still has to fill. The
// Run must succeed and its result download byte-for-byte; earlier Runs'
// results must still download, and the new one again after the disk store
// restarts when this identity may restart it.
func TestLiveHangarRunConsumesAManagedInputAndDownloadsItsResult(t *testing.T) {
	password := liveHangarRequireRunResults(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	clientset, _ := kubeClient(t)
	api := liveHangarLogin(t, ctx, password)
	api.setTemplate(t)

	// Earlier Runs first: their results were written before whatever has
	// happened to the store since.
	expected := liveHangarRunTree(t, "fixed")
	for _, earlier := range api.succeededRuns(t, ctx, 3) {
		body := api.downloadResult(t, ctx, earlier, "copy")
		api.requireResultTree(t, earlier, body, "fixed")
		t.Logf("Run %d's result still downloads (%d bytes)", earlier.Number, len(body))
	}

	source := api.uploadInput(t, ctx, expected)
	run := api.createRun(t, ctx, atc.CreatePipelineRunV2Request{
		InvocationKey: "live-hangar-run-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Inputs:        map[string]atc.RunInputSource{"change": source},
	})
	run = api.waitForTerminal(t, ctx, run.Number)
	if run.Status != atc.RunStatusSucceeded || run.Terminal == nil {
		t.Fatalf("Run %d finished %q (terminal %+v), want succeeded", run.Number, run.Status, run.Terminal)
	}
	first := api.downloadResult(t, ctx, run, "copy")
	api.requireResultTree(t, run, first, "fixed")

	if !liveHangarRestartStore(t, ctx, clientset) {
		return
	}
	again := api.downloadResult(t, ctx, run, "copy")
	if !bytes.Equal(again, first) {
		t.Fatalf("after the store restarted, Run %d's result downloads as %d bytes that differ from the %d before", run.Number, len(again), len(first))
	}
}

// liveHangarRequireRunResults gates a check that runs a v2 Run to a published
// result: it skips while web.hangarOutputCapture and web.runResults both
// expect off, and once either expects on, every other feature a Run's result
// needs must expect on too and the live-tests password must be set. It
// returns the password.
func liveHangarRequireRunResults(t *testing.T) string {
	t.Helper()
	liveHangarRequire(t, "web.hangarOutputCapture", "web.runResults")
	manifest := loadLiveManifest(t)
	for _, feature := range []string{"daemon.hangarOutput", "web.hangarOutput", "web.hangarOutputCapture", "web.runResults", "store.disk"} {
		if manifest.Features[feature].Expect != "on" {
			t.Fatalf("the manifest expects Run results on but %s %q; a Run's result needs it", feature, manifest.Features[feature].Expect)
		}
	}
	password := os.Getenv("CONCOURSE_LIVE_PASSWORD")
	if password == "" {
		t.Fatal("CONCOURSE_LIVE_PASSWORD is unset; the pipeline passes ((live-tests-password)) to the live tier")
	}
	return password
}

// liveHangarRunTree is the managed input the template copies: a fixed tree,
// so every Run's result is the same bytes and an earlier Run can be checked.
func liveHangarRunTree(t *testing.T, marker string) []byte {
	t.Helper()
	return liveHangarTar(t, map[string]string{"payload": "managed input " + marker, "nested/second": "second"})
}

func liveHangarTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	for name, content := range files {
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content)), ModTime: time.Unix(123456789, 0)}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

// liveHangarSigner signs warrants with the Hangar key the deployed daemon
// verifies them against, read from the Secret it mounts.
func liveHangarSigner(t *testing.T, ctx context.Context, clientset kubernetes.Interface) *hangar.Signer {
	t.Helper()
	secretName := deployed.daemonSecretVolume("hangar-key")
	if secretName == "" {
		secretName = deployed.daemonSecretVolume("daemon-tls")
	}
	secret, err := clientset.CoreV1().Secrets(deployed.namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read the daemon's Hangar key Secret %s/%s: %v", deployed.namespace, secretName, err)
	}
	key := secret.Data["hangar.key"]
	signer, err := hangar.NewSigner(key, 5*time.Minute, nil)
	if err != nil {
		t.Fatalf("the Hangar key in %s/%s: %v", deployed.namespace, secretName, err)
	}
	return signer
}

type liveHangarDaemon struct {
	url    string
	client *http.Client
}

// liveHangarDaemonClient dials one running artifact daemon pod with the
// client certificate the deployment issues, verifying the server as
// <service>.<namespace>.svc the way web does.
func liveHangarDaemonClient(t *testing.T, ctx context.Context, clientset kubernetes.Interface) liveHangarDaemon {
	t.Helper()
	pods, err := clientset.CoreV1().Pods(deployed.namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=artifact-daemon"})
	if err != nil {
		t.Fatalf("list artifact daemon pods: %v", err)
	}
	var address string
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil && pod.Status.PodIP != "" {
			address = net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(deployed.port))
			break
		}
	}
	if address == "" {
		t.Fatalf("no running artifact daemon pod in %s", deployed.namespace)
	}
	certificate, err := tls.LoadX509KeyPair(filepath.Join(deployed.tlsDir, "client.crt"), filepath.Join(deployed.tlsDir, "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(filepath.Join(deployed.tlsDir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("the daemon TLS Secret's ca.crt holds no certificate")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		RootCAs:      roots,
		ServerName:   deployed.service + "." + deployed.namespace + ".svc",
		Certificates: []tls.Certificate{certificate},
	}
	return liveHangarDaemon{url: "https://" + address, client: &http.Client{Transport: transport, Timeout: time.Minute}}
}

// publish posts a raw tar to the strict publication route. A 503 is the
// daemon's answer to every infrastructure refusal and is retried for a
// bounded minute; any other status is final.
func (daemon liveHangarDaemon) publish(t *testing.T, ctx context.Context, archive []byte, want int) hangar.TreeAttributes {
	t.Helper()
	target := daemon.url + "/hangar/v1/scopes/" + liveHangarScope + "/trees"
	deadline := time.Now().Add(time.Minute)
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(archive))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/octet-stream")
		response, err := daemon.client.Do(request)
		if err != nil {
			t.Fatalf("publish to the artifact daemon at %s: %v", target, err)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode == http.StatusServiceUnavailable && time.Now().Before(deadline) {
			t.Logf("publication refused 503, retrying: %s", strings.TrimSpace(string(body)))
			liveHangarPause(t, ctx, 5*time.Second)
			continue
		}
		if response.StatusCode != want {
			t.Fatalf("publication returned %d, want %d: %s", response.StatusCode, want, strings.TrimSpace(string(body)))
		}
		var attributes hangar.TreeAttributes
		if err := json.Unmarshal(body, &attributes); err != nil {
			t.Fatalf("decode publication attributes %q: %v", body, err)
		}
		return attributes
	}
}

type liveHangarAPI struct {
	base string
	// token is the bearer the client sends, for a CLI's saved target.
	token  string
	http   *http.Client
	client concourse.Client
}

// liveHangarLogin takes a token for the live-tests user with fly's password
// grant and returns a client that sends it. Web is dialled at its pod
// address from inside the cluster, and at its external URL from outside.
func liveHangarLogin(t *testing.T, ctx context.Context, password string) liveHangarAPI {
	t.Helper()
	base, ok := deployed.webFlag("external-url")
	if inCluster() {
		port, err := deployed.webPort("http")
		if err != nil {
			t.Fatal(err)
		}
		base, ok = "http://"+net.JoinHostPort(deployed.webPod.Status.PodIP, port), true
	}
	if !ok || base == "" {
		t.Fatal("web has no --external-url and the tier is not in the cluster")
	}
	config := oauth2.Config{
		ClientID:     "fly",
		ClientSecret: "Zmx5",
		Endpoint:     oauth2.Endpoint{TokenURL: base + "/sky/issuer/token"},
		Scopes:       []string{"openid", "profile", "email", "federated:id", "groups"},
	}
	token, err := config.PasswordCredentialsToken(ctx, liveHangarRunUser, password)
	if err != nil {
		t.Fatalf("log in to %s as %s: %v", base, liveHangarRunUser, err)
	}
	// Dex's ID token carries the claims web authorizes on, as fly sends it.
	if idToken, _ := token.Extra("id_token").(string); idToken != "" {
		token = &oauth2.Token{AccessToken: idToken, TokenType: "Bearer", Expiry: token.Expiry}
	}
	httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(token))
	httpClient.Timeout = 2 * time.Minute
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return liveHangarAPI{base: base, token: token.AccessToken, http: httpClient, client: concourse.NewClient(base, httpClient, false)}
}

// setTemplate sets and unpauses the template, updating it in place when an
// earlier run of the tier left it.
func (api liveHangarAPI) setTemplate(t *testing.T) {
	t.Helper()
	config := fmt.Sprintf(`template: true
run_retention:
  keep_last: 10
jobs:
- name: copy
  plan:
  - task: copy
    task_id: %s
    run_inputs:
    - name: change
      input: source
    run_result:
      name: copy
      output: result
    config:
      platform: linux
      rootfs_uri: docker:///busybox
      inputs:
      - name: source
      outputs:
      - name: result
      run:
        path: /bin/sh
        args: [-ec, "mkdir -p result/nested && cat source/payload > result/payload && cat source/nested/second > result/nested/second"]
`, liveHangarRunTaskID)
	api.installTemplate(t, liveHangarRunPipeline, config)
}

// installTemplate sets and unpauses the named template in team main.
func (api liveHangarAPI) installTemplate(t *testing.T, name, config string) {
	t.Helper()
	team := api.client.Team("main")
	ref := atc.PipelineRef{Name: name}
	_, version, _, err := team.PipelineConfig(ref)
	if err != nil {
		t.Fatalf("read the %s template: %v", name, err)
	}
	if _, _, warnings, err := team.CreateOrUpdatePipelineConfig(ref, version, []byte(config), false); err != nil {
		t.Fatalf("set the %s template: %v", name, err)
	} else if len(warnings) > 0 {
		t.Logf("set the %s template with warnings: %+v", name, warnings)
	}
	if _, err := team.UnpausePipeline(ref); err != nil {
		t.Fatalf("unpause the %s template: %v", name, err)
	}
}

func (api liveHangarAPI) endpoint(version string) string {
	return api.endpointOf(liveHangarRunPipeline, version)
}

func (api liveHangarAPI) endpointOf(template, version string) string {
	return api.base + "/api/" + version + "/teams/main/pipelines/" + url.PathEscape(template)
}

// do sends the request and returns the body, failing on any status not in
// want.
func (api liveHangarAPI) do(t *testing.T, request *http.Request, want ...int) (*http.Response, []byte) {
	t.Helper()
	response, err := api.http.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", request.Method, request.URL.Path, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		t.Fatalf("read %s %s: %v", request.Method, request.URL.Path, err)
	}
	for _, status := range want {
		if response.StatusCode == status {
			return response, body
		}
	}
	t.Fatalf("%s %s returned %d: %s", request.Method, request.URL.Path, response.StatusCode, strings.TrimSpace(string(body)))
	return nil, nil
}

func (api liveHangarAPI) uploadInput(t *testing.T, ctx context.Context, archive []byte) atc.RunInputSource {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, api.endpoint("v1")+"/run-inputs/change", bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-tar")
	_, body := api.do(t, request, http.StatusCreated)
	var source atc.RunInputSource
	if err := json.Unmarshal(body, &source); err != nil || source.SourceID == "" || source.Bearer == "" {
		t.Fatalf("the input upload returned %q (%v), want a source id and bearer", body, err)
	}
	return source
}

func (api liveHangarAPI) createRun(t *testing.T, ctx context.Context, intent atc.CreatePipelineRunV2Request) atc.PipelineRun {
	t.Helper()
	payload, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, api.endpoint("v2")+"/runs", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	_, body := api.do(t, request, http.StatusCreated)
	var run atc.PipelineRun
	if err := json.Unmarshal(body, &run); err != nil {
		t.Fatalf("decode the created Run %q: %v", body, err)
	}
	if run.Number <= 0 || run.ContractVersion != atc.RunContractV2 || run.ActivationEpoch <= 0 {
		t.Fatalf("the created Run = %+v, want a numbered v2 Run in an activation epoch", run)
	}
	t.Logf("created Run %d in activation epoch %d", run.Number, run.ActivationEpoch)
	return run
}

func (api liveHangarAPI) run(t *testing.T, ctx context.Context, number int) atc.PipelineRun {
	t.Helper()
	return api.runOf(t, ctx, liveHangarRunPipeline, number)
}

func (api liveHangarAPI) runOf(t *testing.T, ctx context.Context, template string, number int) atc.PipelineRun {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, api.endpointOf(template, "v1")+"/runs/"+strconv.Itoa(number), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, body := api.do(t, request, http.StatusOK)
	var run atc.PipelineRun
	if err := json.Unmarshal(body, &run); err != nil {
		t.Fatalf("decode Run %d %q: %v", number, body, err)
	}
	return run
}

func (api liveHangarAPI) waitForTerminal(t *testing.T, ctx context.Context, number int) atc.PipelineRun {
	t.Helper()
	return api.waitForRun(t, ctx, liveHangarRunPipeline, number)
}

func (api liveHangarAPI) waitForRun(t *testing.T, ctx context.Context, template string, number int) atc.PipelineRun {
	t.Helper()
	deadline := time.Now().Add(10 * time.Minute)
	for {
		run := api.runOf(t, ctx, template, number)
		if run.Status != atc.RunStatusRunning {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("Run %d still running after 10m; captures %+v", number, run.Captures)
		}
		liveHangarPause(t, ctx, 3*time.Second)
	}
}

// succeededRuns returns up to limit of the template's succeeded Runs, newest
// first, with their terminal results.
func (api liveHangarAPI) succeededRuns(t *testing.T, ctx context.Context, limit int) []atc.PipelineRun {
	t.Helper()
	listed, _, err := api.client.Team("main").PipelineRuns(liveHangarRunPipeline, concourse.Page{Limit: 20})
	if err != nil {
		t.Fatalf("list the %s template's Runs: %v", liveHangarRunPipeline, err)
	}
	var runs []atc.PipelineRun
	for _, run := range listed {
		if run.Status != atc.RunStatusSucceeded || run.Reclaimed || len(runs) == limit {
			continue
		}
		runs = append(runs, api.run(t, ctx, run.Number))
	}
	return runs
}

// downloadResult reads a Run's result. Web answers 503 with Retry-After while
// its download slots are busy, which is retried for a bounded two minutes.
func (api liveHangarAPI) downloadResult(t *testing.T, ctx context.Context, run atc.PipelineRun, name string) []byte {
	t.Helper()
	return api.downloadResultOf(t, ctx, liveHangarRunPipeline, run, name)
}

func (api liveHangarAPI) downloadResultOf(t *testing.T, ctx context.Context, template string, run atc.PipelineRun, name string) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, api.endpointOf(template, "v1")+"/runs/"+strconv.Itoa(run.Number)+"/results/"+url.PathEscape(name), nil)
		if err != nil {
			t.Fatal(err)
		}
		response, body := api.do(t, request, http.StatusOK, http.StatusServiceUnavailable)
		if response.StatusCode == http.StatusOK {
			if response.ContentLength != int64(len(body)) {
				t.Fatalf("Run %d's result declared %d bytes and sent %d", run.Number, response.ContentLength, len(body))
			}
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("Run %d's result still refused 503 after 2m: %s", run.Number, strings.TrimSpace(string(body)))
		}
		liveHangarPause(t, ctx, 5*time.Second)
	}
}

// requireResultTree checks a downloaded result against the Run's terminal
// binding -- the canonical archive of exactly the bound digest -- and against
// the input the template copied into it.
func (api liveHangarAPI) requireResultTree(t *testing.T, run atc.PipelineRun, body []byte, marker string) {
	t.Helper()
	liveHangarRequireBoundResult(t, run, "copy", body)
	files := map[string]string{}
	reader := tar.NewReader(bytes.NewReader(body))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read Run %d's result: %v", run.Number, err)
		}
		if header.Typeflag == tar.TypeReg {
			content, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			files[strings.TrimPrefix(header.Name, "./")] = string(content)
		}
	}
	if files["payload"] != "managed input "+marker || files["nested/second"] != "second" {
		t.Fatalf("Run %d's result holds %v, want the managed input copied", run.Number, files)
	}
}

// liveHangarRequireBoundResult checks a downloaded result against the Run's
// terminal binding the way agent/runclient/result.go does before it trusts a
// result: the body must canonicalize to exactly the bound digest, at the
// downloaded size. The binding must also name the read claim that keeps the
// tree from reclaim, and a tree in a scope.
func liveHangarRequireBoundResult(t *testing.T, run atc.PipelineRun, name string, body []byte) atc.RunResultBinding {
	t.Helper()
	if run.Terminal == nil {
		t.Fatalf("Run %d has no terminal result", run.Number)
	}
	binding, ok := run.Terminal.Results[name]
	if !ok {
		t.Fatalf("Run %d's terminal results %+v name no %s result", run.Number, run.Terminal.Results, name)
	}
	if err := binding.ClaimID.Validate(); err != nil {
		t.Fatalf("Run %d's %s binding %+v holds no claim: %v", run.Number, name, binding, err)
	}
	if binding.Ref.Scope == "" || binding.Ref.Generation <= 0 {
		t.Fatalf("Run %d's %s binding %+v names no stored tree", run.Number, name, binding)
	}
	tree, err := (hangar.Canonicalizer{MaxContentBytes: 32 << 20, MaxEntries: 64, TempDir: t.TempDir()}).Capture(context.Background(), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Run %d's %s result is not a canonical tree: %v", run.Number, name, err)
	}
	defer tree.Close()
	if tree.Digest != binding.Ref.Digest || tree.ByteSize != int64(len(body)) {
		t.Fatalf("Run %d's %s result is %s (%d bytes); its binding is %s", run.Number, name, tree.Digest, tree.ByteSize, binding.Ref.Digest)
	}
	return binding
}

// liveHangarRestartStore deletes the disk store's pod and waits for its
// replacement to be Ready. It reports false, without failing, when this
// identity may not delete pods in the release namespace: runbook S15 then
// restarts the store and re-runs the tier, whose earlier-Run downloads are the
// check.
func liveHangarRestartStore(t *testing.T, ctx context.Context, clientset kubernetes.Interface) bool {
	t.Helper()
	const selector = "app.kubernetes.io/component=hangar-store"
	pods, err := clientset.CoreV1().Pods(deployed.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		t.Fatalf("list the disk store's pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("want one disk store pod in %s, found %d", deployed.namespace, len(pods.Items))
	}
	previous := pods.Items[0]
	if err := clientset.CoreV1().Pods(deployed.namespace).Delete(ctx, previous.Name, metav1.DeleteOptions{}); err != nil {
		if apierrors.IsForbidden(err) {
			t.Logf("this identity may not restart the disk store (%v); the re-run after runbook S15's restart downloads this Run's result", err)
			return false
		}
		t.Fatalf("delete the disk store pod %s: %v", previous.Name, err)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		pods, err := clientset.CoreV1().Pods(deployed.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			t.Fatalf("list the disk store's pods: %v", err)
		}
		for _, pod := range pods.Items {
			if pod.UID == previous.UID || pod.DeletionTimestamp != nil {
				continue
			}
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					t.Logf("disk store restarted: %s replaced %s", pod.Name, previous.Name)
					return true
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no Ready disk store pod replaced %s within 5m", previous.Name)
		}
		liveHangarPause(t, ctx, 2*time.Second)
	}
}

func liveHangarPause(t *testing.T, ctx context.Context, d time.Duration) {
	t.Helper()
	select {
	case <-ctx.Done():
		t.Fatalf("timed out: %v", ctx.Err())
	case <-time.After(d):
	}
}
