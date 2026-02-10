package tdd_test

import (
	"os"
	"path/filepath"
)

func setupGoModule(dir string) string {
	// Create a minimal go.mod for the test module.
	modContent := "module testmod\n\ngo 1.25\n"
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modContent), 0644)
	return dir
}

func writeGoTest(repoDir, filename, content string) {
	os.WriteFile(filepath.Join(repoDir, filename), []byte(content), 0644)
}

func writeGoFile(repoDir, filename, content string) {
	dir := filepath.Dir(filepath.Join(repoDir, filename))
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(repoDir, filename), []byte(content), 0644)
}
