package migration

import (
	"errors"
)

func AssetNames() []string {
	return nil
}

func RestoreAsset(string, string) error {
	return errors.New("no assets")
}

func RestoreAssets(string, string) error {
	return errors.New("no assets")
}
