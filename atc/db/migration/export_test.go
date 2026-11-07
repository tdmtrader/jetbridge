package migration

// EncryptSigningKeysOnStart puts signing_keys into the start-up encryption
// pass, as SigningKeysEncryptedAtRest will, and returns a func that restores
// the registry.
func EncryptSigningKeysOnStart() (restore func()) {
	previous := append([]encryptedColumn(nil), encryptedColumns...)
	for i := range encryptedColumns {
		if encryptedColumns[i].Table == "signing_keys" {
			encryptedColumns[i].RotateOnly = false
		}
	}
	return func() { encryptedColumns = previous }
}
