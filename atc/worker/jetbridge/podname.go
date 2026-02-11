package jetbridge

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/concourse/concourse/atc/db"
)

const maxPodNameLen = 63

// nonAlphanumHyphen matches any character that is not lowercase alphanumeric
// or a hyphen.
var nonAlphanumHyphen = regexp.MustCompile(`[^a-z0-9-]`)

// multiHyphen matches consecutive hyphens.
var multiHyphen = regexp.MustCompile(`-{2,}`)

// GeneratePodName produces a human-readable Kubernetes pod name from
// container metadata and the database handle. The name encodes
// pipeline/job/build/step context for easy identification via kubectl.
//
// Format for build steps:      <pipeline>-<job>-b<build>-<type>-<suffix>
// Format for checks:           chk-<step-name>-<suffix>
// Format for resource type ops: rt-<step-name>-<type>-<suffix>
// Fallback:                     the raw handle (UUID) when metadata is insufficient
//
// The suffix is the first 8 hex characters of the handle (hyphens stripped).
// The total name is capped at 63 characters (DNS label safe).
func GeneratePodName(metadata db.ContainerMetadata, handle string) string {
	suffix := hexSuffix(handle)

	// Check containers: chk-<resource>-<suffix>
	if metadata.Type == db.ContainerTypeCheck {
		if metadata.StepName == "" {
			return handle
		}
		// "chk-" (4) + suffix (8) + hyphen (1) = 13 fixed chars
		maxResource := maxPodNameLen - 13
		resource := sanitizeSegment(metadata.StepName, maxResource)
		if resource == "" {
			return handle
		}
		return fmt.Sprintf("chk-%s-%s", resource, suffix)
	}

	// Resource type image get/put steps: have StepName but no job context.
	// These are get/put steps from in-memory check builds that fetch resource
	// type images. Format: rt-<step-name>-<type>-<suffix>
	if (metadata.PipelineName == "" || metadata.JobName == "") && metadata.StepName != "" &&
		(metadata.Type == db.ContainerTypeGet || metadata.Type == db.ContainerTypePut) {
		stepType := string(metadata.Type)
		// "rt-" (3) + "-" + stepType + "-" + suffix(8) = 13 + len(stepType)
		maxStep := maxPodNameLen - 13 - len(stepType)
		step := sanitizeSegment(metadata.StepName, maxStep)
		if step != "" {
			return fmt.Sprintf("rt-%s-%s-%s", step, stepType, suffix)
		}
	}

	// Build step containers need pipeline + job to be meaningful.
	if metadata.PipelineName == "" || metadata.JobName == "" {
		return handle
	}

	stepType := string(metadata.Type)
	if stepType == "" {
		stepType = "task"
	}

	buildPart := ""
	if metadata.BuildName != "" {
		buildPart = "b" + metadata.BuildName
	}

	// Calculate available space for pipeline + job segments.
	// Fixed parts: 4 hyphens + suffix(8) + stepType(<=5) + buildPart
	fixedLen := 4 + len(suffix) + len(stepType)
	if buildPart != "" {
		fixedLen += len(buildPart)
	} else {
		fixedLen-- // one fewer hyphen when no build part
	}

	available := maxPodNameLen - fixedLen
	if available < 2 {
		return handle
	}

	// Split available space evenly between pipeline and job, capping
	// each segment at 20 characters for readability.
	maxEach := available / 2
	if maxEach > 20 {
		maxEach = 20
	}
	pipeline := sanitizeSegment(metadata.PipelineName, maxEach)
	job := sanitizeSegment(metadata.JobName, maxEach)

	if pipeline == "" || job == "" {
		return handle
	}

	if buildPart != "" {
		return fmt.Sprintf("%s-%s-%s-%s-%s", pipeline, job, buildPart, stepType, suffix)
	}
	return fmt.Sprintf("%s-%s-%s-%s", pipeline, job, stepType, suffix)
}

// sanitizeSegment converts a string into a valid K8s name segment:
// lowercase, replace non-alphanumeric with hyphens, collapse consecutive
// hyphens, trim leading/trailing hyphens, and truncate to maxLen.
func sanitizeSegment(s string, maxLen int) string {
	s = strings.ToLower(s)
	// Replace common separators with hyphens.
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, " ", "-")
	// Strip anything that isn't lowercase alphanumeric or hyphen.
	s = nonAlphanumHyphen.ReplaceAllString(s, "")
	// Collapse consecutive hyphens.
	s = multiHyphen.ReplaceAllString(s, "-")
	// Trim leading/trailing hyphens.
	s = strings.Trim(s, "-")
	// Truncate.
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	// Trim trailing hyphen after truncation.
	s = strings.TrimRight(s, "-")
	return s
}

// hexSuffix extracts the first 8 hex characters from the handle by
// stripping hyphens first (handles are typically UUIDs).
func hexSuffix(handle string) string {
	hex := strings.ReplaceAll(handle, "-", "")
	if len(hex) > 8 {
		return hex[:8]
	}
	return hex
}
