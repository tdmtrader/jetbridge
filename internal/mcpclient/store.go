package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func (c *Client) withLock(ctx context.Context, run func() error) error {
	if err := os.MkdirAll(filepath.Dir(c.config.StatePath), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(c.config.StatePath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("lock MCP credentials: %w", err)
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		locked, err := tryStateLock(file)
		if err != nil {
			return err
		}
		if locked {
			break
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for MCP credential lock: %w", ctx.Err())
		case <-timer.C:
		}
	}
	defer unlockState(file)
	return run()
}

func (c *Client) save(state savedGrant) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(c.config.StatePath), ".mcp-grant-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), c.config.StatePath)
}
