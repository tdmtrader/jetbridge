package behavioral_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// loadImageStreaming keeps the original export/import format and serial image
// order, but connects the commands directly instead of writing two tar copies.
// Both commands must finish successfully; either failure cancels its peer.
func loadImageStreaming(ctx context.Context, containerID string, images ...string) error {
	if containerID == "" || len(images) == 0 {
		return fmt.Errorf("streaming image import requires a container and images")
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("image stream pipe: %w", err)
	}
	defer reader.Close()
	defer writer.Close()
	save := exec.CommandContext(childCtx, "docker", append([]string{"image", "save", "--"}, images...)...)
	importer := exec.CommandContext(childCtx, "docker", "exec", "-i", containerID,
		"ctr", "-n=k8s.io", "images", "import", "--all-platforms", "-")
	var saveError, importOutput bytes.Buffer
	save.Stdout = writer
	save.Stderr = &saveError
	importer.Stdin = reader
	importer.Stdout = &importOutput
	importer.Stderr = &importOutput
	if err := importer.Start(); err != nil {
		return fmt.Errorf("start image importer: %w", err)
	}
	if err := save.Start(); err != nil {
		cancel()
		_ = importer.Wait()
		return fmt.Errorf("start image exporter: %w", err)
	}
	// Only the child processes retain pipe ends. EOF/SIGPIPE can now propagate.
	_ = reader.Close()
	_ = writer.Close()
	type completion struct {
		exporter bool
		err      error
	}
	completed := make(chan completion, 2)
	go func() { completed <- completion{true, save.Wait()} }()
	go func() { completed <- completion{false, importer.Wait()} }()
	var exportErr, importErr error
	for i := 0; i < 2; i++ {
		result := <-completed
		if result.err != nil {
			cancel()
		}
		if result.exporter {
			exportErr = result.err
		} else {
			importErr = result.err
		}
	}
	if err := errors.Join(exportErr, importErr); err != nil {
		return fmt.Errorf("streaming images %v: %w; exporter: %s; importer: %s", images, err, saveError.String(), importOutput.String())
	}
	return nil
}
