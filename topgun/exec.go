package topgun

import (
	"fmt"
	"os"
	"os/exec"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/onsi/gomega/gexec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func Start(env []string, command string, argv ...string) *gexec.Session {
	TimestampedBy("running: " + command + " " + strings.Join(argv, " "))

	cmd := exec.Command(command, argv...)
	cwd, err := os.Getwd()
	Expect(err).ToNot(HaveOccurred())
	cmd.Dir = filepath.Join(cwd, "..")
	cmd.Env = env

	session, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
	Expect(err).ToNot(HaveOccurred())

	return session
}

func SpawnInteractive(stdin io.Reader, env []string, command string, argv ...string) *gexec.Session {
	TimestampedBy("interactively running: " + command + " " + strings.Join(argv, " "))

	cmd := exec.Command(command, argv...)
	cwd, err := os.Getwd()
	Expect(err).ToNot(HaveOccurred())
	cmd.Dir = filepath.Join(cwd, "..")
	cmd.Stdin = stdin
	// Inherit the parent process environment, then apply overrides.
	cmd.Env = mergeEnv(os.Environ(), env)

	session, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
	Expect(err).ToNot(HaveOccurred())
	return session
}

// mergeEnv takes a base environment and a set of overrides, returning
// a new environment slice with overrides applied. If an override key
// already exists in base, it is replaced; otherwise it is appended.
func mergeEnv(base, overrides []string) []string {
	result := make([]string, len(base))
	copy(result, base)
	for _, override := range overrides {
		key := override
		if idx := strings.IndexByte(override, '='); idx >= 0 {
			key = override[:idx]
		}
		found := false
		for i, existing := range result {
			if strings.HasPrefix(existing, key+"=") {
				result[i] = override
				found = true
				break
			}
		}
		if !found {
			result = append(result, override)
		}
	}
	return result
}

func TimestampedBy(msg string) {
	By(fmt.Sprintf("[%.9f] %s", float64(time.Now().UnixNano())/1e9, msg))
}

func Wait(session *gexec.Session) {
	<-session.Exited
	Expect(session.ExitCode()).To(Equal(0))
}

func Run(env []string, command string, argv ...string) *gexec.Session {
	session := Start(env, command, argv...)
	Wait(session)
	return session
}
