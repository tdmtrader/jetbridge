package jetbridge

// The start journal is a mutex, and these specs race it.
//
// A signed start is owed exactly one outcome writer, and the producer is owed
// at most one run. Deliveries of the wrapper and closings of an undelivered
// start all claim the start with the same O_EXCL creation; whichever wins owns
// the journal and every other one stands down. They run the real scripts
// under the local sh -- dash and BusyBox ash when the suite binary is run in
// those images -- because the property is the shell's noclobber open.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/google/uuid"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("The exact start journal's claim", func() {
	var marker string

	BeforeEach(func() {
		marker = filepath.Join(GinkgoT().TempDir(), "producer-ran")
	})

	// race runs every command at once and returns their exit codes.
	race := func(commands [][]string) []int {
		codes := make([]int, len(commands))
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i, command := range commands {
			wg.Add(1)
			go func(i int, command []string) {
				defer GinkgoRecover()
				defer wg.Done()
				<-start
				out, err := exec.Command(command[0], command[1:]...).CombinedOutput()
				var exited *exec.ExitError
				switch {
				case err == nil:
				case errors.As(err, &exited):
					codes[i] = exited.ExitCode()
				default:
					Fail(fmt.Sprintf("%v: %s", err, out))
				}
			}(i, command)
		}
		close(start)
		wg.Wait()
		return codes
	}

	// expectOneOwner is the property: the producer ran at most once, and the
	// journal's exit is the producer's own if it did and the stopped exit if
	// it did not.
	expectOneOwner := func(state string, stopped int) {
		journal, err := os.ReadFile(filepath.Join(state, "exit"))
		Expect(err).NotTo(HaveOccurred(), "nobody journaled an exit")
		ran, err := os.ReadFile(marker)
		if os.IsNotExist(err) {
			Expect(string(journal)).To(Equal(fmt.Sprintf("%d\n", stopped)))
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(string(ran)).To(Equal("x"), "the producer ran more than once")
		Expect(string(journal)).To(Equal("0\n"))
	}

	It("lets one of a task supervisor's racing deliveries and closings own the journal", func() {
		if _, err := exec.LookPath("setsid"); err != nil {
			Skip("the exact supervisor launches its producer under setsid")
		}
		id := "claim-race-" + uuid.NewString()
		spec := runtime.ProcessSpec{Path: "sh", Args: []string{"-c", `printf x >> "$1"`, "producer", marker}}
		_, state := supervisorCommandParts(id, spec)
		DeferCleanup(func() { Expect(os.RemoveAll(state)).To(Succeed()) })

		var commands [][]string
		for i := 0; i < 4; i++ {
			commands = append(commands, exactSupervisorCommand(id, spec), exactCloseCommand(state, false))
		}
		for _, code := range race(commands) {
			Expect(code).To(BeElementOf(0, exactTaskStoppedExit, ExactUnresolvedExitCode, 255))
		}
		expectOneOwner(state, exactTaskStoppedExit)
	})

	It("lets one of a resource session's racing deliveries and closings own the journal", func() {
		requireResourceSessions()
		state := exactResourceStateDir(executioncontrol.Identity{
			ExecutionID: executioncontrol.ExecutionID(uuid.NewString()), Fence: 1})
		DeferCleanup(func() { Expect(os.RemoveAll(state)).To(Succeed()) })

		var commands [][]string
		for i := 0; i < 4; i++ {
			commands = append(commands,
				journaledResourceCommand([]string{"sh", "-c", `printf x >> "$1"`, "producer", marker}, state),
				exactCloseCommand(state, true))
		}
		race(commands)
		expectOneOwner(state, exactResourceStoppedExit)
	})

	It("closes an undelivered start once, and leaves a claimed one alone", func() {
		state := filepath.Join("/tmp", taskStateDirPrefix[len("/tmp/"):]+"close-"+uuid.NewString())
		DeferCleanup(func() { Expect(os.RemoveAll(state)).To(Succeed()) })

		// A claimed start with no exit: a command still running. Closing it
		// writes nothing, so its own exit is the one that will be read.
		Expect(os.MkdirAll(state, 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(state, "start"), []byte("started\n"), 0o600)).To(Succeed())
		command := exactCloseCommand(state, false)
		Expect(exec.Command(command[0], command[1:]...).Run()).To(Succeed())
		_, err := os.Stat(filepath.Join(state, "exit"))
		Expect(os.IsNotExist(err)).To(BeTrue(), "a close wrote an exit for a command that claimed its start")

		// An unclaimed one is closed as stopped, once.
		Expect(os.Remove(filepath.Join(state, "start"))).To(Succeed())
		for i := 0; i < 2; i++ {
			Expect(exec.Command(command[0], command[1:]...).Run()).To(Succeed())
		}
		journal, err := os.ReadFile(filepath.Join(state, "exit"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(journal)).To(Equal("143\n"))
	})

	// The stopped delivery is the one exec sent when the stop may not be a
	// separate request. It never names the producer, and its own exit is the
	// journal's.
	DescribeTable("delivers a stop that closes the start and reports its exit",
		func(resource bool, stopped int, stopFile string) {
			state := exactResourceStateDir(executioncontrol.Identity{
				ExecutionID: executioncontrol.ExecutionID(uuid.NewString()), Fence: 1})
			if !resource {
				state = strings.Replace(state, resourceStateDirPrefix, taskStateDirPrefix, 1)
			}
			DeferCleanup(func() { Expect(os.RemoveAll(state)).To(Succeed()) })

			command := exactStoppedDelivery(state, resource)
			err := exec.Command(command[0], command[1:]...).Run()
			var exited *exec.ExitError
			Expect(errors.As(err, &exited)).To(BeTrue(), "%v", err)
			Expect(exited.ExitCode()).To(Equal(stopped))
			_, err = os.Stat(filepath.Join(state, stopFile))
			Expect(err).NotTo(HaveOccurred(), "the stop was not written before the claim")
			journal, err := os.ReadFile(filepath.Join(state, "exit"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(journal)).To(Equal(fmt.Sprintf("%d\n", stopped)))

			// A second one finds the journal closed and reports the same exit.
			err = exec.Command(command[0], command[1:]...).Run()
			Expect(errors.As(err, &exited)).To(BeTrue(), "%v", err)
			Expect(exited.ExitCode()).To(Equal(stopped))
		},
		Entry("task", false, exactTaskStoppedExit, "stop"),
		Entry("resource", true, exactResourceStoppedExit, "cancel"),
	)

	It("reports unresolved from a stopped delivery that finds a running command's start", func() {
		state := filepath.Join("/tmp", taskStateDirPrefix[len("/tmp/"):]+"running-"+uuid.NewString())
		DeferCleanup(func() { Expect(os.RemoveAll(state)).To(Succeed()) })
		Expect(os.MkdirAll(state, 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(state, "start"), []byte("started\n"), 0o600)).To(Succeed())

		command := exactStoppedDelivery(state, false)
		done := make(chan error, 1)
		go func() { done <- exec.Command(command[0], command[1:]...).Run() }()
		var err error
		Eventually(done, 10*time.Second).Should(Receive(&err))
		var exited *exec.ExitError
		Expect(errors.As(err, &exited)).To(BeTrue(), "%v", err)
		Expect(exited.ExitCode()).To(Equal(ExactUnresolvedExitCode))
		_, err = os.Stat(filepath.Join(state, "exit"))
		Expect(os.IsNotExist(err)).To(BeTrue(), "a stop invented an outcome for a running command")
		_, err = os.Stat(filepath.Join(state, "stop"))
		Expect(err).NotTo(HaveOccurred())
	})
})
