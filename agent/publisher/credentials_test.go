package publisher_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/publisher"
)

func TestFileCredentialProviderReturnsImmutableResolvedAuthorization(t *testing.T) {
	credentialPath := writeCredentialForTest(t, "mounted-secret")
	provider, err := publisher.NewFileCredentialProvider(
		exactPolicy(),
		filepath.Dir(credentialPath),
		map[string]string{"widget-git": credentialPath},
	)
	if err != nil {
		t.Fatalf("NewFileCredentialProvider: %v", err)
	}

	credential, err := provider.AuthorizeDestination(context.Background(), policyGitRequest())
	if err != nil {
		t.Fatalf("AuthorizeDestination: %v", err)
	}
	if credential.Reference != "widget-git" {
		t.Fatalf("credential reference = %q, want widget-git", credential.Reference)
	}
	if credential.AdapterKind() != publisher.AdapterDirectGit {
		t.Fatalf("adapter = %q, want direct-git", credential.AdapterKind())
	}
	if credential.RemoteURL() != "https://git.example/acme/widget.git" {
		t.Fatalf("remote URL = %q", credential.RemoteURL())
	}
	secret := credential.Secret()
	if string(secret) != "mounted-secret" {
		t.Fatalf("secret bytes = %q", secret)
	}
	secret[0] = 'X'
	if got := string(credential.Secret()); got != "mounted-secret" {
		t.Fatalf("credential secret mutated through returned slice: %q", got)
	}
	if rendered := fmt.Sprintf("%+v", credential); strings.Contains(rendered, "mounted-secret") {
		t.Fatalf("formatted credential exposed secret contents: %s", rendered)
	}
}

func TestFileCredentialProviderRejectsMissingAndUnknownCredentialMappings(t *testing.T) {
	credentialRoot := canonicalTempDir(t)
	for name, files := range map[string]map[string]string{
		"missing": nil,
		"unknown": {
			"widget-git": writeCredentialForTest(t, "mounted-secret"),
			"unused":     writeCredentialForTest(t, "unused-secret"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := publisher.NewFileCredentialProvider(exactPolicy(), credentialRoot, files); err == nil {
				t.Fatalf("%s credential mapping was accepted", name)
			}
		})
	}
}

func TestFileCredentialProviderRejectsSymlinkAndWorldReadableSecretFiles(t *testing.T) {
	private := writeCredentialForTest(t, "mounted-secret")
	symlink := filepath.Join(canonicalTempDir(t), "credential")
	if err := os.Symlink(private, symlink); err != nil {
		t.Fatal(err)
	}
	worldReadable := writeCredentialForTest(t, "mounted-secret")
	if err := os.Chmod(worldReadable, 0644); err != nil {
		t.Fatal(err)
	}
	directory := canonicalTempDir(t)

	for name, path := range map[string]string{
		"symlink":        symlink,
		"world readable": worldReadable,
		"directory":      directory,
		"relative path":  "credential",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := publisher.NewFileCredentialProvider(
				exactPolicy(),
				filepath.Dir(path),
				map[string]string{"widget-git": path},
			)
			if err == nil {
				t.Fatalf("%s credential file was accepted", name)
			}
			if strings.Contains(err.Error(), "mounted-secret") {
				t.Fatalf("credential error exposed secret contents: %v", err)
			}
		})
	}
}

func TestFileCredentialProviderRechecksRotatedFileBeforeAuthorization(t *testing.T) {
	credentialPath := writeCredentialForTest(t, "first-secret")
	provider, err := publisher.NewFileCredentialProvider(
		exactPolicy(),
		filepath.Dir(credentialPath),
		map[string]string{"widget-git": credentialPath},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(credentialPath, 0644); err != nil {
		t.Fatal(err)
	}

	_, err = provider.AuthorizeDestination(context.Background(), policyGitRequest())
	if err == nil {
		t.Fatal("authorization used a credential file that became world-readable")
	}
	if strings.Contains(err.Error(), "first-secret") {
		t.Fatalf("credential error exposed secret contents: %v", err)
	}
}

func TestFileCredentialProviderFollowsKubernetesAtomicWriterRotation(t *testing.T) {
	mountPath := canonicalTempDir(t)
	credentialPath := filepath.Join(mountPath, "token")
	writeAtomicWriterGeneration(t, mountPath, "..2026_07_26_01_02_03.000000001", "first-secret")
	if err := os.Symlink("..2026_07_26_01_02_03.000000001", filepath.Join(mountPath, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..data/token", credentialPath); err != nil {
		t.Fatal(err)
	}

	provider, err := publisher.NewFileCredentialProvider(
		exactPolicy(),
		mountPath,
		map[string]string{"widget-git": credentialPath},
	)
	if err != nil {
		t.Fatalf("NewFileCredentialProvider: %v", err)
	}
	credential, err := provider.AuthorizeDestination(context.Background(), policyGitRequest())
	if err != nil {
		t.Fatalf("AuthorizeDestination before rotation: %v", err)
	}
	if got := string(credential.Secret()); got != "first-secret" {
		t.Fatalf("secret before rotation = %q", got)
	}

	writeAtomicWriterGeneration(t, mountPath, "..2026_07_26_01_03_04.000000002", "second-secret")
	nextDataLink := filepath.Join(mountPath, "..data-next")
	if err := os.Symlink("..2026_07_26_01_03_04.000000002", nextDataLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(nextDataLink, filepath.Join(mountPath, "..data")); err != nil {
		t.Fatal(err)
	}

	credential, err = provider.AuthorizeDestination(context.Background(), policyGitRequest())
	if err != nil {
		t.Fatalf("AuthorizeDestination after rotation: %v", err)
	}
	if got := string(credential.Secret()); got != "second-secret" {
		t.Fatalf("secret after rotation = %q", got)
	}
}

func TestFileCredentialProviderFollowsNestedKubernetesAtomicWriterRotation(t *testing.T) {
	mountPath := canonicalTempDir(t)
	credentialPath := filepath.Join(mountPath, "credentials", "token")
	writeNestedAtomicWriterGeneration(
		t,
		mountPath,
		"..2026_07_26_01_02_03.000000001",
		"credentials/token",
		"first-secret",
	)
	if err := os.Symlink("..2026_07_26_01_02_03.000000001", filepath.Join(mountPath, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..data/credentials", filepath.Join(mountPath, "credentials")); err != nil {
		t.Fatal(err)
	}

	provider, err := publisher.NewFileCredentialProvider(
		exactPolicy(),
		mountPath,
		map[string]string{"widget-git": credentialPath},
	)
	if err != nil {
		t.Fatalf("NewFileCredentialProvider: %v", err)
	}
	credential, err := provider.AuthorizeDestination(context.Background(), policyGitRequest())
	if err != nil {
		t.Fatalf("AuthorizeDestination before rotation: %v", err)
	}
	if got := string(credential.Secret()); got != "first-secret" {
		t.Fatalf("secret before rotation = %q", got)
	}

	writeNestedAtomicWriterGeneration(
		t,
		mountPath,
		"..2026_07_26_01_03_04.000000002",
		"credentials/token",
		"second-secret",
	)
	nextDataLink := filepath.Join(mountPath, "..data-next")
	if err := os.Symlink("..2026_07_26_01_03_04.000000002", nextDataLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(nextDataLink, filepath.Join(mountPath, "..data")); err != nil {
		t.Fatal(err)
	}

	credential, err = provider.AuthorizeDestination(context.Background(), policyGitRequest())
	if err != nil {
		t.Fatalf("AuthorizeDestination after rotation: %v", err)
	}
	if got := string(credential.Secret()); got != "second-secret" {
		t.Fatalf("secret after rotation = %q", got)
	}
}

func TestFileCredentialProviderRejectsUnanchoredAtomicWriterSymlinks(t *testing.T) {
	for name, prepare := range map[string]func(*testing.T) string{
		"visible link does not target data directory": func(t *testing.T) string {
			mountPath := canonicalTempDir(t)
			private := writeCredentialForTest(t, "mounted-secret")
			credentialPath := filepath.Join(mountPath, "token")
			if err := os.Symlink(private, credentialPath); err != nil {
				t.Fatal(err)
			}
			return credentialPath
		},
		"data link escapes mount": func(t *testing.T) string {
			mountPath := canonicalTempDir(t)
			outsidePath := canonicalTempDir(t)
			if err := os.WriteFile(filepath.Join(outsidePath, "token"), []byte("mounted-secret"), 0440); err != nil {
				t.Fatal(err)
			}
			credentialPath := filepath.Join(mountPath, "token")
			if err := os.Symlink("..data/token", credentialPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join("..", filepath.Base(outsidePath)), filepath.Join(mountPath, "..data")); err != nil {
				t.Fatal(err)
			}
			return credentialPath
		},
		"data link names arbitrary in-mount directory": func(t *testing.T) string {
			mountPath := canonicalTempDir(t)
			writeAtomicWriterGeneration(t, mountPath, "..attacker", "mounted-secret")
			credentialPath := filepath.Join(mountPath, "token")
			if err := os.Symlink("..data/token", credentialPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("..attacker", filepath.Join(mountPath, "..data")); err != nil {
				t.Fatal(err)
			}
			return credentialPath
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := prepare(t)
			_, err := publisher.NewFileCredentialProvider(
				exactPolicy(),
				filepath.Dir(path),
				map[string]string{"widget-git": path},
			)
			if err == nil {
				t.Fatal("unsafe credential symlink was accepted")
			}
			if strings.Contains(err.Error(), "mounted-secret") {
				t.Fatalf("credential error exposed secret contents: %v", err)
			}
		})
	}
}

func TestFileCredentialProviderRejectsStableIntermediateAncestorSymlink(t *testing.T) {
	credentialRoot := canonicalTempDir(t)
	outsideRoot := canonicalTempDir(t)
	if err := os.WriteFile(filepath.Join(outsideRoot, "token"), []byte("outside-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideRoot, filepath.Join(credentialRoot, "credentials")); err != nil {
		t.Fatal(err)
	}

	_, err := publisher.NewFileCredentialProvider(
		exactPolicy(),
		credentialRoot,
		map[string]string{"widget-git": filepath.Join(credentialRoot, "credentials", "token")},
	)
	if err == nil {
		t.Fatal("credential beneath an intermediate ancestor symlink was accepted")
	}
	if strings.Contains(err.Error(), "outside-secret") {
		t.Fatalf("credential error exposed secret contents: %v", err)
	}
}

func TestFileCredentialProviderRejectsSymlinkedTrustedRoot(t *testing.T) {
	baseDirectory := canonicalTempDir(t)
	actualRoot := filepath.Join(baseDirectory, "actual")
	if err := os.Mkdir(actualRoot, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(actualRoot, "token"), []byte("mounted-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	trustedRoot := filepath.Join(baseDirectory, "trusted")
	if err := os.Symlink(actualRoot, trustedRoot); err != nil {
		t.Fatal(err)
	}

	_, err := publisher.NewFileCredentialProvider(
		exactPolicy(),
		trustedRoot,
		map[string]string{"widget-git": filepath.Join(trustedRoot, "token")},
	)
	if err == nil {
		t.Fatal("symlinked trusted root was accepted")
	}
}

func TestFileCredentialProviderRejectsTrustedRootRotation(t *testing.T) {
	baseDirectory := canonicalTempDir(t)
	trustedRoot := filepath.Join(baseDirectory, "trusted")
	if err := os.Mkdir(trustedRoot, 0750); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(trustedRoot, "token")
	if err := os.WriteFile(credentialPath, []byte("mounted-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	provider, err := publisher.NewFileCredentialProvider(
		exactPolicy(),
		trustedRoot,
		map[string]string{"widget-git": credentialPath},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(trustedRoot, filepath.Join(baseDirectory, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(trustedRoot, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trustedRoot, "token"), []byte("replacement-secret"), 0600); err != nil {
		t.Fatal(err)
	}

	credential, err := provider.AuthorizeDestination(context.Background(), policyGitRequest())
	if err == nil {
		t.Fatalf("authorization followed a replaced trusted root and returned %q", credential.Secret())
	}
	if strings.Contains(err.Error(), "replacement-secret") {
		t.Fatalf("credential error exposed secret contents: %v", err)
	}
}

func TestFileCredentialProviderRejectsIntermediateAncestorRotation(t *testing.T) {
	credentialRoot := canonicalTempDir(t)
	credentialDirectory := filepath.Join(credentialRoot, "credentials")
	if err := os.Mkdir(credentialDirectory, 0750); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(credentialDirectory, "token")
	if err := os.WriteFile(credentialPath, []byte("mounted-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	provider, err := publisher.NewFileCredentialProvider(
		exactPolicy(),
		credentialRoot,
		map[string]string{"widget-git": credentialPath},
	)
	if err != nil {
		t.Fatal(err)
	}

	replacementDirectory := filepath.Join(credentialRoot, "replacement")
	if err := os.Mkdir(replacementDirectory, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replacementDirectory, "token"), []byte("replacement-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(credentialDirectory, filepath.Join(credentialRoot, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacementDirectory, credentialDirectory); err != nil {
		t.Fatal(err)
	}

	credential, err := provider.AuthorizeDestination(context.Background(), policyGitRequest())
	if err == nil {
		t.Fatalf("authorization followed a replaced ancestor and returned %q", credential.Secret())
	}
	if strings.Contains(err.Error(), "replacement-secret") {
		t.Fatalf("credential error exposed secret contents: %v", err)
	}
}

func TestFileCredentialProviderNeverFollowsConcurrentIntermediateAncestorSymlink(t *testing.T) {
	credentialRoot := canonicalTempDir(t)
	credentialDirectory := filepath.Join(credentialRoot, "credentials")
	if err := os.Mkdir(credentialDirectory, 0750); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(credentialDirectory, "token")
	if err := os.WriteFile(credentialPath, []byte("mounted-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	outsideRoot := canonicalTempDir(t)
	if err := os.WriteFile(filepath.Join(outsideRoot, "token"), []byte("outside-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	provider, err := publisher.NewFileCredentialProvider(
		exactPolicy(),
		credentialRoot,
		map[string]string{"widget-git": credentialPath},
	)
	if err != nil {
		t.Fatal(err)
	}

	parkedDirectory := filepath.Join(credentialRoot, "credentials-safe")
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		for attempt := 0; attempt < 500; attempt++ {
			if err := os.Rename(credentialDirectory, parkedDirectory); err != nil {
				done <- err
				return
			}
			if err := os.Symlink(outsideRoot, credentialDirectory); err != nil {
				done <- err
				return
			}
			runtime.Gosched()
			if err := os.Remove(credentialDirectory); err != nil {
				done <- err
				return
			}
			if err := os.Rename(parkedDirectory, credentialDirectory); err != nil {
				done <- err
				return
			}
			runtime.Gosched()
		}
		done <- nil
	}()
	<-started

	for attempt := 0; attempt < 2000; attempt++ {
		credential, authorizeErr := provider.AuthorizeDestination(context.Background(), policyGitRequest())
		if authorizeErr == nil && string(credential.Secret()) != "mounted-secret" {
			t.Fatalf("authorization escaped the trusted root and returned %q", credential.Secret())
		}
		runtime.Gosched()
	}
	if err := <-done; err != nil {
		t.Fatalf("rotate intermediate ancestor: %v", err)
	}
}

func TestFileCredentialProviderDeniesBeforeReadingCredentialFile(t *testing.T) {
	credentialPath := writeCredentialForTest(t, "mounted-secret")
	provider, err := publisher.NewFileCredentialProvider(
		exactPolicy(),
		filepath.Dir(credentialPath),
		map[string]string{"widget-git": credentialPath},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(credentialPath); err != nil {
		t.Fatal(err)
	}
	request := policyGitRequest()
	request.Destination = "git.example/acme/not-allowed"

	_, err = provider.AuthorizeDestination(context.Background(), request)
	if !errors.Is(err, publisher.ErrDestinationNotAllowed) {
		t.Fatalf("denial error = %v, want ErrDestinationNotAllowed", err)
	}
}

func TestFileCredentialProviderRejectsNilAndCancelledContexts(t *testing.T) {
	credentialPath := writeCredentialForTest(t, "mounted-secret")
	provider, err := publisher.NewFileCredentialProvider(
		exactPolicy(),
		filepath.Dir(credentialPath),
		map[string]string{"widget-git": credentialPath},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.AuthorizeDestination(nil, policyGitRequest()); !errors.Is(err, publisher.ErrInvalidRequest) {
		t.Fatalf("nil context error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.AuthorizeDestination(ctx, policyGitRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context error = %v", err)
	}
}

func writeCredentialForTest(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(canonicalTempDir(t), "credential")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func writeAtomicWriterGeneration(t *testing.T, mountPath, generation, contents string) {
	t.Helper()
	generationPath := filepath.Join(mountPath, generation)
	if err := os.Mkdir(generationPath, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generationPath, "token"), []byte(contents), 0440); err != nil {
		t.Fatal(err)
	}
}

func writeNestedAtomicWriterGeneration(
	t *testing.T,
	mountPath string,
	generation string,
	relativePath string,
	contents string,
) {
	t.Helper()
	path := filepath.Join(mountPath, generation, relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0440); err != nil {
		t.Fatal(err)
	}
}
