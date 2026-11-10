// Command jb-launcher is the launcher: the entrypoint of a step pod's main
// container in the JetBridge runtime.
//
// A step pod is the pod that runs one step's container. Web never execs into
// it; the container's command is in the pod spec, and what the step needs
// beyond that (today, its stdin) arrives through the step envelope, a small
// document the launcher pulls from web before it starts the step's process.
// An init container copies this binary into an emptyDir shared with the main
// container, whose command is then
//
//	/jb/jb-launcher [flags] -- <command> [args...]
//
// The launcher does four things and nothing else:
//
//   - fetch the step envelope (when --envelope-url is set), authenticating
//     with the pod's projected token and retrying connection errors and 5xx
//     responses until --envelope-timeout;
//   - start the command in its own process group, feeding it the envelope's
//     stdin, optionally capturing its stdout to --stdout-file;
//   - forward SIGTERM and SIGINT to that process group, escalating to
//     SIGKILL after --kill-grace, so that a pod deletion ends the step's
//     whole tree and not just its leader;
//   - exit with the command's exit code (128+signal when it died by signal)
//     and leave a one-line JSON record in the container's termination
//     message for web to read back.
//
// Exit codes 125, 126 and 127 are reserved for the launcher's own failures,
// in the convention of docker run and the POSIX shells: 127 when the command
// was not found, 126 when it could not be executed, 125 for everything else
// (bad usage, envelope fetch failure, exec failure). A command that could
// not be started, or not set up once its envelope was served (stdin staged,
// stdout file opened), still leaves the termination record, with that code
// (it did not run); a failure before the envelope, such as bad usage, the
// token or the fetch, leaves none. Any other code is the
// step's.
//
// The binary is stdlib-only and built static (CGO_ENABLED=0) because it runs
// inside the step's image, whatever that image contains.
package launcher
