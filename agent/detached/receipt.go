package detached

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	receiptVersion = "detached-invocation/v1"
	// A receipt written before the client was shared by more than one
	// workload. It keeps resuming, as the review submission it always was.
	legacyReviewReceiptVersion = "review-invocation/v1"
	legacyReviewWorkload       = "review"
)

// This local submission receipt intentionally has no field capable of holding
// auth or a grant bearer. A source ID alone permits replay, never new input
// admission.
type submissionReceipt struct {
	Version       string `json:"version"`
	Workload      string `json:"workload,omitempty"`
	Server        string `json:"server"`
	Team          string `json:"team"`
	Template      string `json:"template"`
	InputDigest   string `json:"input_digest"`
	InvocationKey string `json:"invocation_key"`
	SourceID      string `json:"source_id,omitempty"`
	RunID         int    `json:"run_id,omitempty"`
	Number        int    `json:"number,omitempty"`
}

type receiptFile struct {
	root *os.Root
	lock *os.File
	name string
}

func openReceipt(ctx context.Context, path, input string) (*receiptFile, error) {
	if path == "" {
		return nil, errors.New("a saved receipt path is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	input, err = filepath.EvalSymlinks(input)
	if err != nil {
		return nil, err
	}
	input, err = filepath.Abs(input)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(input, filepath.Join(parent, filepath.Base(path)))
	if err != nil {
		return nil, err
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("receipt must be outside the submitted input")
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	file := &receiptFile{root: root, name: filepath.Base(path)}
	file.lock, err = root.OpenFile(file.name+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		root.Close()
		return nil, err
	}
	for {
		locked, err := tryReceiptLock(file.lock)
		if err != nil {
			file.Close()
			return nil, err
		}
		if locked {
			return file, nil
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (f *receiptFile) Close() {
	if f.lock != nil {
		unlockReceipt(f.lock)
		f.lock.Close()
	}
	f.root.Close()
}

// Load returns a receipt in the current version. A legacy review receipt is
// read as the review workload and is rewritten in the current version the next
// time submission saves progress.
func (f *receiptFile) Load() (submissionReceipt, bool, error) {
	var receipt submissionReceipt
	st, err := f.root.Lstat(f.name)
	if errors.Is(err, os.ErrNotExist) {
		return receipt, false, nil
	}
	if err != nil {
		return receipt, false, err
	}
	if !st.Mode().IsRegular() || st.Size() > 16384 || st.Mode().Perm() != 0600 {
		return receipt, false, errors.New("receipt must be a private, bounded regular file")
	}
	file, err := f.root.Open(f.name)
	if err != nil {
		return receipt, false, err
	}
	defer file.Close()
	d := json.NewDecoder(io.LimitReader(file, 16385))
	d.DisallowUnknownFields()
	if err := d.Decode(&receipt); err != nil {
		return receipt, false, errors.New("invalid saved submission receipt")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return receipt, false, errors.New("invalid saved submission receipt")
	}
	switch {
	case receipt.Version == receiptVersion && receipt.Workload != "":
	case receipt.Version == legacyReviewReceiptVersion && receipt.Workload == "":
		receipt.Version, receipt.Workload = receiptVersion, legacyReviewWorkload
	default:
		return receipt, false, errors.New("incomplete saved submission receipt")
	}
	if receipt.InvocationKey == "" || receipt.RunID < 0 || receipt.Number < 0 || (receipt.RunID == 0) != (receipt.Number == 0) || (receipt.RunID > 0 && receipt.SourceID == "") {
		return receipt, false, errors.New("incomplete saved submission receipt")
	}
	return receipt, true, nil
}

func (f *receiptFile) Save(receipt submissionReceipt) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	temp := f.name + ".tmp-" + uuid.NewString()
	file, err := f.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	defer f.root.Remove(temp)
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return f.root.Rename(temp, f.name)
}
