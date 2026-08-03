package deploy_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Five places build a Concourse runtime image, and they have drifted before:
// the pipeline's inline copy omitted git, which silently made every
// repository/v1 seal and the ATC direct-Git publisher impossible on the
// deployed cluster while every test stayed green on hosts that happen to have
// git. Worse, the three copies below that no guard covered are the ones the
// k8s test tiers use, so no tier could have proven the capability either.
//
// git is pinned exactly on the ubuntu:22.04 images, which include the one that
// actually ships, so a rebuild is reproducible. The k8s-e2e copies are
// debian:bookworm-slim, whose git version differs and where an Ubuntu version
// string simply does not resolve; byte-identical parity across distros is not
// achievable, so those require presence only. The invariant this guard
// enforces is therefore: NO runtime image silently lacks git, and the shipped
// one is reproducible.
//
// Note the exact pin will eventually vanish from the Ubuntu archive and fail
// the build. That is deliberate: a loud failure beats a silent version drift.
const (
	pinnedGit = "git=1:2.34.1-1ubuntu1.17"
	anyGit    = "git"
)

func ubuntuRuntimePackages() []string {
	return []string{"ca-certificates", "dumb-init", pinnedGit}
}

func debianRuntimePackages() []string {
	return []string{"ca-certificates", "dumb-init", anyGit}
}

// runtimeImageSource is one place a Concourse runtime image is defined, with
// the extraction needed to isolate its Dockerfile from the surrounding file.
type runtimeImageSource struct {
	name     string
	path     string
	extract  func(t *testing.T, contents string) []string
	packages []string
}

func runtimeImageSources() []runtimeImageSource {
	return []runtimeImageSource{
		{
			name:     "concourse-pipeline.yml build-image inline Dockerfile (shipped)",
			path:     "concourse-pipeline.yml",
			extract:  func(t *testing.T, c string) []string { return []string{inlineDockerfile(t, c)} },
			packages: ubuntuRuntimePackages(),
		},
		{
			name:     "Dockerfile.build runtime stage",
			path:     "../Dockerfile.build",
			extract:  func(t *testing.T, c string) []string { return []string{lastDockerfileStage(c)} },
			packages: ubuntuRuntimePackages(),
		},
		{
			name:     "Dockerfile.local (concourse-local:latest, default CONCOURSE_IMAGE for k8s tiers)",
			path:     "../Dockerfile.local",
			extract:  func(t *testing.T, c string) []string { return []string{lastDockerfileStage(c)} },
			packages: ubuntuRuntimePackages(),
		},
		{
			name:     "k8s-e2e-pipeline.yml inline Dockerfiles",
			path:     "k8s-e2e-pipeline.yml",
			extract:  k8sE2EInlineDockerfiles,
			packages: debianRuntimePackages(),
		},
	}
}

func TestEveryRuntimeImageInstallsTheDeclaredPackages(t *testing.T) {
	for _, source := range runtimeImageSources() {
		t.Run(source.name, func(t *testing.T) {
			regions := source.extract(t, readDeployFile(t, source.path))
			if len(regions) == 0 {
				t.Fatalf("%s: extracted no Dockerfile regions", source.path)
			}
			for i, region := range regions {
				assertInstallsPackages(t, region, source.packages, i)
			}
		})
	}
}

func lastDockerfileStage(dockerfile string) string {
	stages := strings.Split(dockerfile, "\nFROM ")
	return stages[len(stages)-1]
}

// k8sE2EInlineDockerfiles extracts every heredoc the k8s-e2e jobs write to
// /tmp/docker-build/Dockerfile. There are two byte-identical build sites; both
// must be checked, so this returns all matches rather than requiring one.
func k8sE2EInlineDockerfiles(t *testing.T, pipeline string) []string {
	t.Helper()
	block := regexp.MustCompile(`(?s)cat <<'DOCKERFILE' > /tmp/docker-build/Dockerfile\n(.*?)\n\s*DOCKERFILE\n`)
	matches := block.FindAllStringSubmatch(pipeline, -1)
	if len(matches) < 2 {
		t.Fatalf("expected at least two inline Dockerfile heredocs in k8s-e2e-pipeline.yml, found %d — "+
			"if a build site was removed, update this guard deliberately", len(matches))
	}
	regions := make([]string, 0, len(matches))
	for _, match := range matches {
		regions = append(regions, match[1])
	}
	return regions
}

// assertInstallsPackages checks the declared packages against only the
// apt-get install argument list within region, not the whole region. The
// region also contains an ENTRYPOINT line naming "dumb-init" as the exec
// target, so asserting against the whole region is satisfied by that line
// regardless of what apt-get actually installs — it would pass even if
// dumb-init were dropped from the install list entirely.
//
// Matching is token-aware rather than substring: a bare "git" requirement must
// not be satisfied by "git-lfs", and an unpinned "git" must still accept a
// pinned "git=<version>" token.
func assertInstallsPackages(t *testing.T, region string, packages []string, index int) {
	t.Helper()
	start := strings.Index(region, "apt-get install")
	if start < 0 {
		t.Fatalf("region %d has no apt-get install invocation:\n%s", index, region)
	}
	end := strings.Index(region, "rm -rf /var/lib/apt/lists/*")
	if end < 0 {
		t.Fatalf("region %d has no apt cleanup (rm -rf /var/lib/apt/lists/*):\n%s", index, region)
	}
	if end < start {
		t.Fatalf("region %d has apt cleanup before apt-get install:\n%s", index, region)
	}
	installList := region[start:end]
	tokens := make(map[string]bool)
	for _, token := range strings.Fields(installList) {
		tokens[strings.TrimSuffix(token, "\\")] = true
	}
	for _, pkg := range packages {
		if !installListHasPackage(tokens, pkg) {
			t.Errorf("region %d apt-get install list does not include %q:\n%s", index, pkg, installList)
		}
	}
}

func installListHasPackage(tokens map[string]bool, pkg string) bool {
	if tokens[pkg] {
		return true
	}
	if strings.Contains(pkg, "=") {
		return false
	}
	for token := range tokens {
		if strings.HasPrefix(token, pkg+"=") {
			return true
		}
	}
	return false
}

func readDeployFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// inlineDockerfile extracts the heredoc the build-image job writes to
// /tmp/Dockerfile. It fails loudly if the marker is missing or appears more
// than once — deploy/test-pipeline.yml already has a byte-identical marker
// for an unrelated job, so a second occurrence in concourse-pipeline.yml
// itself must not be silently resolved by taking the first match.
func inlineDockerfile(t *testing.T, pipeline string) string {
	t.Helper()
	block := regexp.MustCompile(`(?s)cat <<'DOCKERFILE' > /tmp/Dockerfile\n(.*?)\n\s*DOCKERFILE\n`)
	matches := block.FindAllStringSubmatch(pipeline, -1)
	if len(matches) != 1 {
		t.Fatalf("expected exactly one inline Dockerfile heredoc in concourse-pipeline.yml, found %d", len(matches))
	}
	return matches[0][1]
}
