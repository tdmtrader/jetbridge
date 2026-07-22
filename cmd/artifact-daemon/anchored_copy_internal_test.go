package main

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

func replacementArchive(t *testing.T, name, contents string) []byte {
	t.Helper()
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(contents)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func TestPeerExtractionPublishesThroughOpenedParentAfterPathSwap(t *testing.T) {
	base := t.TempDir()
	parentPath := filepath.Join(base, "authorized")
	victimPath := filepath.Join(base, "other-build")
	if err := os.Mkdir(parentPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(victimPath, 0755); err != nil {
		t.Fatal(err)
	}
	parent, err := openDirectoryNoFollow(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if err := os.Rename(parentPath, parentPath+"-opened"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other-build", parentPath); err != nil {
		t.Fatal(err)
	}

	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	contents := []byte("authorized")
	if err := tw.WriteHeader(&tar.Header{Name: "data", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := extractTarIntoOpenedDirectory(t.Context(), bytes.NewReader(archive.Bytes()), parent, "result"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(parentPath+"-opened", "result", "data"))
	if err != nil || string(got) != "authorized" {
		t.Fatalf("opened parent result = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(victimPath, "result")); !os.IsNotExist(err) {
		t.Fatalf("peer extraction escaped to swapped parent: %v", err)
	}
}

func TestCopyArtifactKeepsOpenedSourceWhenPathIsSwappedToAnotherBuild(t *testing.T) {
	storage := t.TempDir()
	steps := filepath.Join(storage, "steps")
	source := filepath.Join(steps, "producer", "output")
	victim := filepath.Join(steps, "other-build", "output")
	dest := filepath.Join(steps, "consumer", "input")
	for _, dir := range []string{source, victim} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("authorized"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "data"), []byte("other-build-secret"), 0644); err != nil {
		t.Fatal(err)
	}

	server := NewServer(lagertest.NewTestLogger("source-swap"), storage, "node")
	server.copyHooks.sourceOpened = func() {
		original := source + "-opened"
		if err := os.Rename(source, original); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "..", "other-build", "output"), source); err != nil {
			t.Fatal(err)
		}
	}
	if err := server.copyArtifact(source, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "authorized" {
		t.Fatalf("copied swapped source bytes %q", got)
	}
}

func TestPutAndDeleteRejectDestinationParentSwap(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			storage := t.TempDir()
			parent := filepath.Join(storage, "steps", "consumer")
			victim := filepath.Join(storage, "steps", "other-build")
			for _, dir := range []string{parent, victim} {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			victimArtifact := filepath.Join(victim, "artifact")
			if err := os.WriteFile(victimArtifact, []byte("other-build-secret"), 0644); err != nil {
				t.Fatal(err)
			}
			if method == http.MethodDelete {
				if err := os.WriteFile(filepath.Join(parent, "artifact"), []byte("authorized"), 0644); err != nil {
					t.Fatal(err)
				}
			}

			server := NewServer(lagertest.NewTestLogger("mutation-swap"), storage, "node")
			server.mutationHooks.destinationParentOpened = func() {
				if err := os.Rename(parent, parent+"-opened"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("other-build", parent); err != nil {
					t.Fatal(err)
				}
			}
			ts := httptest.NewServer(server.Handler())
			defer ts.Close()
			req, err := http.NewRequest(method, ts.URL+"/artifacts/steps/consumer/artifact", io.NopCloser(strings.NewReader("replacement")))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode < 400 {
				t.Fatalf("swapped parent status = %d, want rejection", resp.StatusCode)
			}
			if got, err := os.ReadFile(victimArtifact); err != nil || string(got) != "other-build-secret" {
				t.Fatalf("other build mutated: %q, %v", got, err)
			}
		})
	}
}

func TestGetArtifactStreamsOpenedTreeWhenSourcePathIsSwapped(t *testing.T) {
	storage := t.TempDir()
	source := filepath.Join(storage, "steps", "producer", "output")
	victim := filepath.Join(storage, "steps", "other-build", "output")
	for _, dir := range []string{source, victim} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("authorized"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "data"), []byte("other-build-secret"), 0644); err != nil {
		t.Fatal(err)
	}

	server := NewServer(lagertest.NewTestLogger("get-swap"), storage, "node")
	server.serveHooks.sourceOpened = func() {
		if err := os.Rename(source, source+"-opened"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "..", "other-build", "output"), source); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	resp, err := ts.Client().Get(ts.URL + "/artifacts/steps/producer/output")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	tr := tar.NewReader(resp.Body)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(tr)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Name != "data" || string(got) != "authorized" {
		t.Fatalf("GET leaked swapped source: %q=%q", hdr.Name, got)
	}
}

func TestArtifactGetAcquiresReadGuardBeforeOpeningReplaceableSource(t *testing.T) {
	for _, tc := range []struct {
		name       string
		requestURL string
		register   bool
	}{
		{name: "generic artifact", requestURL: "/artifacts/steps/producer/output"},
		{name: "resource cache alias", requestURL: "/resource-caches/rc-1", register: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := t.TempDir()
			source := filepath.Join(storage, "steps", "producer", "output")
			if err := os.MkdirAll(source, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "old.txt"), []byte("complete-old-tree"), 0644); err != nil {
				t.Fatal(err)
			}
			server := NewServer(lagertest.NewTestLogger("guard-before-open"), storage, "node")
			if tc.register {
				server.Registry().RegisterAlias("rc-1", source)
			}
			descriptorOpened := make(chan struct{})
			allowGet := make(chan struct{})
			var openedOnce sync.Once
			server.serveHooks.sourceDescriptorOpened = func() {
				openedOnce.Do(func() { close(descriptorOpened) })
				<-allowGet
			}

			getDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				recorder := httptest.NewRecorder()
				server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.requestURL, nil))
				getDone <- recorder
			}()
			<-descriptorOpened

			replacement := replacementArchive(t, "new.txt", "replacement")
			putDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPut, "/stream-in/producer/output", bytes.NewReader(replacement))
				server.Handler().ServeHTTP(recorder, request)
				putDone <- recorder
			}()
			select {
			case put := <-putDone:
				close(allowGet)
				t.Fatalf("stream-in replaced an opened GET source before its read guard; status=%d", put.Code)
			case <-time.After(100 * time.Millisecond):
			}
			close(allowGet)

			get := <-getDone
			if get.Code != http.StatusOK {
				t.Fatalf("GET status = %d: %s", get.Code, get.Body.String())
			}
			tr := tar.NewReader(bytes.NewReader(get.Body.Bytes()))
			header, err := tr.Next()
			if err != nil {
				t.Fatalf("GET did not return a complete tar: %v", err)
			}
			contents, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			if header.Name != "old.txt" || string(contents) != "complete-old-tree" {
				t.Fatalf("guarded GET = %q:%q, want complete old tree", header.Name, contents)
			}
			put := <-putDone
			if put.Code != http.StatusCreated {
				t.Fatalf("stream-in status = %d: %s", put.Code, put.Body.String())
			}
			if got, err := os.ReadFile(filepath.Join(source, "new.txt")); err != nil || string(got) != "replacement" {
				t.Fatalf("replacement bytes = %q, %v", got, err)
			}
		})
	}
}

func TestDottedStepHandleReadCoordinatesWithSweeperGuard(t *testing.T) {
	storage := t.TempDir()
	source := filepath.Join(storage, "steps", "..producer", "output")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("complete-tree"), 0644); err != nil {
		t.Fatal(err)
	}
	server := NewServer(lagertest.NewTestLogger("dotted-handle-guard"), storage, "node")
	descriptorOpened := make(chan struct{})
	allowGet := make(chan struct{})
	server.serveHooks.sourceDescriptorOpened = func() {
		close(descriptorOpened)
		<-allowGet
	}

	getDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/artifacts/steps/..producer/output", nil))
		getDone <- recorder
	}()
	<-descriptorOpened

	sweepAcquired := make(chan struct{})
	releaseSweep := make(chan struct{})
	sweepDone := make(chan struct{})
	go func() {
		release := server.Guard().BeginSweep("..producer")
		close(sweepAcquired)
		<-releaseSweep
		release()
		close(sweepDone)
	}()
	select {
	case <-sweepAcquired:
		close(releaseSweep)
		close(allowGet)
		<-getDone
		<-sweepDone
		t.Fatal("sweeper acquired dotted handle while artifact GET held its read guard")
	case <-time.After(100 * time.Millisecond):
	}

	close(allowGet)
	get := <-getDone
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d: %s", get.Code, get.Body.String())
	}
	select {
	case <-sweepAcquired:
	case <-time.After(time.Second):
		t.Fatal("sweeper did not acquire dotted handle after GET released it")
	}
	close(releaseSweep)
	<-sweepDone
}

func TestResourceCacheAliasSurvivesTransientStreamInReplacementGap(t *testing.T) {
	storage := t.TempDir()
	source := filepath.Join(storage, "steps", "producer", "output")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "old.txt"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	server := NewServer(lagertest.NewTestLogger("alias-replacement-gap"), storage, "node")
	server.Registry().RegisterAlias("rc-1", source)
	destinationRemoved := make(chan struct{})
	allowReplacement := make(chan struct{})
	server.mutationHooks.streamInDestinationRemoved = func() {
		close(destinationRemoved)
		<-allowReplacement
	}

	putDone := make(chan *httptest.ResponseRecorder, 1)
	replacement := replacementArchive(t, "new.txt", "new-complete-tree")
	go func() {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/stream-in/producer/output", bytes.NewReader(replacement))
		server.Handler().ServeHTTP(recorder, request)
		putDone <- recorder
	}()
	<-destinationRemoved

	getDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/resource-caches/rc-1", nil))
		getDone <- recorder
	}()
	select {
	case get := <-getDone:
		close(allowReplacement)
		t.Fatalf("resource-cache GET observed transient missing destination: status=%d body=%s", get.Code, get.Body.String())
	case <-time.After(100 * time.Millisecond):
	}
	if path, found := server.Registry().Lookup("rc-1"); !found || path != source {
		close(allowReplacement)
		t.Fatalf("valid alias was removed during replacement gap: %q, %v", path, found)
	}
	close(allowReplacement)
	if put := <-putDone; put.Code != http.StatusCreated {
		t.Fatalf("stream-in status = %d: %s", put.Code, put.Body.String())
	}
	get := <-getDone
	if get.Code != http.StatusOK {
		t.Fatalf("resource-cache GET status = %d: %s", get.Code, get.Body.String())
	}
	tr := tar.NewReader(bytes.NewReader(get.Body.Bytes()))
	header, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(tr)
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "new.txt" || string(contents) != "new-complete-tree" {
		t.Fatalf("guarded alias GET = %q:%q, want complete replacement", header.Name, contents)
	}
	if path, found := server.Registry().Lookup("rc-1"); !found || path != source {
		t.Fatalf("valid alias was not retained after replacement: %q, %v", path, found)
	}
}

func TestResolveAliasWaitsForStreamInPublicationBeforeOpeningSource(t *testing.T) {
	storage := t.TempDir()
	source := filepath.Join(storage, "steps", "producer", "output")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "old.txt"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	server := NewServer(lagertest.NewTestLogger("resolve-alias-replacement-gap"), storage, "node")
	server.Registry().RegisterAlias("rc-1", source)
	destinationRemoved := make(chan struct{})
	allowReplacement := make(chan struct{})
	server.mutationHooks.streamInDestinationRemoved = func() {
		close(destinationRemoved)
		<-allowReplacement
	}

	putDone := make(chan *httptest.ResponseRecorder, 1)
	replacement := replacementArchive(t, "new.txt", "new-complete-tree")
	go func() {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/stream-in/producer/output", bytes.NewReader(replacement))
		server.Handler().ServeHTTP(recorder, request)
		putDone <- recorder
	}()
	<-destinationRemoved

	destination := filepath.Join(storage, "steps", "consumer", "input")
	resolved := make(chan resolveResponse, 1)
	go func() {
		resolved <- server.resolveOne(t.Context(), "rc-1", destination, "")
	}()
	select {
	case response := <-resolved:
		close(allowReplacement)
		<-putDone
		t.Fatalf("resolve observed transient missing alias source: %+v", response)
	case <-time.After(100 * time.Millisecond):
	}
	if path, found := server.Registry().Lookup("rc-1"); !found || path != source {
		close(allowReplacement)
		<-putDone
		t.Fatalf("valid alias was removed during replacement gap: %q, %v", path, found)
	}

	close(allowReplacement)
	if put := <-putDone; put.Code != http.StatusCreated {
		t.Fatalf("stream-in status = %d: %s", put.Code, put.Body.String())
	}
	response := <-resolved
	if response.Status != "ok" || response.Method != "registry" {
		t.Fatalf("resolve response = %+v, want ok via registry", response)
	}
	if got, err := os.ReadFile(filepath.Join(destination, "new.txt")); err != nil || string(got) != "new-complete-tree" {
		t.Fatalf("resolved replacement = %q, %v", got, err)
	}
	if path, found := server.Registry().Lookup("rc-1"); !found || path != source {
		t.Fatalf("valid alias was not retained after resolve: %q, %v", path, found)
	}
}

func TestResolveFilesystemFallbackWaitsForGuardedReplacementBeforeOpeningSource(t *testing.T) {
	storage := t.TempDir()
	source := filepath.Join(storage, "steps", "producer", "output")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "old.txt"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	server := NewServer(lagertest.NewTestLogger("resolve-filesystem-replacement-gap"), storage, "node")
	releaseReplacement := server.Guard().BeginSweep(server.stepHandle(source))
	released := false
	defer func() {
		if !released {
			releaseReplacement()
		}
	}()
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(storage, "steps", "consumer", "input")
	resolved := make(chan resolveResponse, 1)
	go func() {
		resolved <- server.resolveOne(t.Context(), "producer/output", destination, "")
	}()
	select {
	case response := <-resolved:
		releaseReplacement()
		released = true
		t.Fatalf("filesystem resolve observed transient missing source: %+v", response)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "new.txt"), []byte("new-complete-tree"), 0644); err != nil {
		t.Fatal(err)
	}
	releaseReplacement()
	released = true

	response := <-resolved
	if response.Status != "ok" || response.Method != "filesystem" {
		t.Fatalf("resolve response = %+v, want ok via filesystem", response)
	}
	if got, err := os.ReadFile(filepath.Join(destination, "new.txt")); err != nil || string(got) != "new-complete-tree" {
		t.Fatalf("resolved replacement = %q, %v", got, err)
	}
}

func TestCopyArtifactRejectsDestinationParentSwapWithoutMutatingOtherBuild(t *testing.T) {
	storage := t.TempDir()
	steps := filepath.Join(storage, "steps")
	source := filepath.Join(steps, "producer", "output")
	destParent := filepath.Join(steps, "consumer")
	dest := filepath.Join(destParent, "input")
	victimParent := filepath.Join(steps, "other-build")
	for _, dir := range []string{source, destParent, victimParent} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("authorized"), 0644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(victimParent, "marker")
	if err := os.WriteFile(marker, []byte("untouched"), 0644); err != nil {
		t.Fatal(err)
	}

	server := NewServer(lagertest.NewTestLogger("destination-swap"), storage, "node")
	server.copyHooks.destinationParentOpened = func() {
		if err := os.Rename(destParent, destParent+"-opened"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("other-build", destParent); err != nil {
			t.Fatal(err)
		}
	}
	if err := server.copyArtifact(source, dest); err == nil {
		t.Fatal("destination parent replacement was accepted")
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "untouched" {
		t.Fatalf("other build was mutated: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(victimParent, "input")); !os.IsNotExist(err) {
		t.Fatalf("copy published into swapped destination: %v", err)
	}
}

func TestCopyArtifactRejectsTemporaryTreeSwapBeforePublish(t *testing.T) {
	storage := t.TempDir()
	steps := filepath.Join(storage, "steps")
	source := filepath.Join(steps, "producer", "output")
	dest := filepath.Join(steps, "consumer", "input")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("authorized"), 0644); err != nil {
		t.Fatal(err)
	}

	server := NewServer(lagertest.NewTestLogger("temporary-swap"), storage, "node")
	server.copyHooks.temporaryReady = func(name string) {
		original := filepath.Join(steps, name)
		if err := os.Rename(original, original+"-opened"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(original, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(original, "data"), []byte("substituted"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if err := server.copyArtifact(source, dest); err == nil {
		t.Fatal("substituted temporary tree was accepted")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("substituted tree was published: %v", err)
	}
}

func TestPutArtifactStagesOutsideTheTaskWritableDestination(t *testing.T) {
	storage := t.TempDir()
	destinationParent := filepath.Join(storage, "steps", "consumer")
	if err := os.MkdirAll(destinationParent, 0755); err != nil {
		t.Fatal(err)
	}

	server := NewServer(lagertest.NewTestLogger("private-put-stage"), storage, "node")
	stagingObserved := false
	server.mutationHooks.temporaryOpened = func(parent *os.File, _ string) {
		stagingObserved = true
		parentInfo, err := parent.Stat()
		if err != nil {
			t.Fatal(err)
		}
		destinationInfo, err := os.Stat(destinationParent)
		if err != nil {
			t.Fatal(err)
		}
		if os.SameFile(parentInfo, destinationInfo) {
			t.Fatal("PUT temporary was created inside the task-writable destination parent")
		}
		if parentInfo.Mode().Perm() != 0700 {
			t.Fatalf("staging directory mode = %v, want 0700", parentInfo.Mode().Perm())
		}
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/artifacts/steps/consumer/data", strings.NewReader("authorized"))
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d, want 201", recorder.Code)
	}
	if !stagingObserved {
		t.Fatal("PUT did not expose a staging descriptor to the test hook")
	}
	if got, err := os.ReadFile(filepath.Join(destinationParent, "data")); err != nil || string(got) != "authorized" {
		t.Fatalf("published bytes = %q, %v", got, err)
	}
}
