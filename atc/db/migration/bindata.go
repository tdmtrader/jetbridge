package migration

import (
	"errors"
	"os"
)

func AssetInfo(string) (os.FileInfo, error) {
	return nil, errors.New("no assets")
}

func AssetNames() []string {
	return nil
}

func RestoreAsset(string, string) error {
	return errors.New("no assets")
}

func RestoreAssets(string, string) error {
	return errors.New("no assets")
}
