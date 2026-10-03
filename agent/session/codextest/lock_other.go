//go:build !unix

package codextest

// lock is a no-op where there is no flock: concurrent provisioning then
// downloads twice, and the atomic renames keep the cache consistent.
func lock(string) (func(), error) { return func() {}, nil }
