package steps

// expectedStreamInCommand is the argv a scenario expects Volume.StreamIn to
// exec for an upload, stated here independently of production: an upload to
// the mount root itself is a bare tar, so an image with no shell still takes
// inputs; a destination below the root is created first, under sh, with the
// path travelling as an argument rather than as shell source.
func expectedStreamInCommand(mount, destination string) []string {
	if destination == mount {
		return []string{"tar", "xf", "-", "-C", destination}
	}
	return []string{"sh", "-c", `mkdir -p -- "$1" && exec tar xf - -C "$1"`, "stream-in", destination}
}
