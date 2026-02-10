package implement_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement"
)

var _ = Describe("TaskTracker", func() {
	var (
		phases  []implement.Phase
		tracker *implement.TaskTracker
	)

	BeforeEach(func() {
		phases = []implement.Phase{
			{
				Name: "Setup",
				Tasks: []implement.PlanTask{
					{ID: "1.1", Description: "Task A", Phase: "Setup"},
					{ID: "1.2", Description: "Task B", Phase: "Setup"},
				},
			},
			{
				Name: "Core",
				Tasks: []implement.PlanTask{
					{ID: "2.1", Description: "Task C", Phase: "Core"},
				},
			},
		}
		tracker = implement.NewTaskTracker(phases)
	})

	It("initializes all tasks as pending", func() {
		summary := tracker.Summary()
		Expect(summary.Pending).To(Equal(3))
		Expect(summary.Total).To(Equal(3))
	})

	It("initializes already-completed tasks as committed", func() {
		phases[0].Tasks[0].Completed = true
		tracker = implement.NewTaskTracker(phases)
		summary := tracker.Summary()
		Expect(summary.Pending).To(Equal(2))
		Expect(summary.Committed).To(Equal(1))
	})

	Describe("Advance", func() {
		It("transitions pending → red → green → committed", func() {
			err := tracker.Advance("1.1")
			Expect(err).NotTo(HaveOccurred())
			Expect(tracker.StatusOf("1.1")).To(Equal(implement.StatusRed))

			err = tracker.Advance("1.1")
			Expect(err).NotTo(HaveOccurred())
			Expect(tracker.StatusOf("1.1")).To(Equal(implement.StatusGreen))

			err = tracker.Advance("1.1")
			Expect(err).NotTo(HaveOccurred())
			Expect(tracker.StatusOf("1.1")).To(Equal(implement.StatusCommitted))
		})

		It("returns error on unknown task", func() {
			err := tracker.Advance("99.99")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("Skip", func() {
		It("marks task as skipped with reason", func() {
			tracker.Skip("1.1", "already satisfied")
			Expect(tracker.StatusOf("1.1")).To(Equal(implement.StatusSkipped))
		})
	})

	Describe("Fail", func() {
		It("marks task as failed with reason", func() {
			tracker.Fail("1.1", "compilation error")
			Expect(tracker.StatusOf("1.1")).To(Equal(implement.StatusFailed))
		})
	})

	Describe("NextPending", func() {
		It("returns the first pending task", func() {
			task := tracker.NextPending()
			Expect(task).NotTo(BeNil())
			Expect(task.TaskID).To(Equal("1.1"))
		})

		It("skips non-pending tasks", func() {
			tracker.Skip("1.1", "done")
			task := tracker.NextPending()
			Expect(task).NotTo(BeNil())
			Expect(task.TaskID).To(Equal("1.2"))
		})

		It("returns nil when all tasks are done", func() {
			tracker.Skip("1.1", "done")
			tracker.Skip("1.2", "done")
			tracker.Skip("2.1", "done")
			task := tracker.NextPending()
			Expect(task).To(BeNil())
		})
	})

	Describe("IsComplete", func() {
		It("returns false when tasks remain", func() {
			Expect(tracker.IsComplete()).To(BeFalse())
		})

		It("returns true when all tasks are resolved", func() {
			tracker.Skip("1.1", "done")
			_ = tracker.Advance("1.2")
			_ = tracker.Advance("1.2")
			_ = tracker.Advance("1.2")
			tracker.Fail("2.1", "failed")
			Expect(tracker.IsComplete()).To(BeTrue())
		})
	})

	Describe("CanContinue", func() {
		It("returns true when no consecutive failures", func() {
			Expect(tracker.CanContinue(3)).To(BeTrue())
		})

		It("returns false when consecutive failures exceed threshold", func() {
			tracker.Fail("1.1", "err")
			tracker.Fail("1.2", "err")
			tracker.Fail("2.1", "err")
			Expect(tracker.CanContinue(3)).To(BeFalse())
		})

		It("resets consecutive count on success", func() {
			tracker.Fail("1.1", "err")
			_ = tracker.Advance("1.2")
			_ = tracker.Advance("1.2")
			_ = tracker.Advance("1.2")
			tracker.Fail("2.1", "err")
			Expect(tracker.CanContinue(2)).To(BeTrue())
		})
	})

	Describe("Save and Load", func() {
		It("round-trips to JSON", func() {
			dir := GinkgoT().TempDir()
			tracker.Skip("1.1", "test-reason")
			_ = tracker.Advance("1.2")

			err := tracker.Save(dir)
			Expect(err).NotTo(HaveOccurred())

			loaded, err := implement.LoadTracker(filepath.Join(dir, "progress.json"))
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.StatusOf("1.1")).To(Equal(implement.StatusSkipped))
			Expect(loaded.StatusOf("1.2")).To(Equal(implement.StatusRed))
			Expect(loaded.Summary().Pending).To(Equal(1))
		})

		It("produces valid JSON", func() {
			dir := GinkgoT().TempDir()
			err := tracker.Save(dir)
			Expect(err).NotTo(HaveOccurred())

			data, err := os.ReadFile(filepath.Join(dir, "progress.json"))
			Expect(err).NotTo(HaveOccurred())

			var parsed map[string]interface{}
			Expect(json.Unmarshal(data, &parsed)).To(Succeed())
		})
	})

	Describe("Summary", func() {
		It("returns accurate counts", func() {
			tracker.Skip("1.1", "done")
			tracker.Fail("1.2", "err")
			_ = tracker.Advance("2.1")
			_ = tracker.Advance("2.1")
			_ = tracker.Advance("2.1")

			s := tracker.Summary()
			Expect(s.Total).To(Equal(3))
			Expect(s.Pending).To(Equal(0))
			Expect(s.Skipped).To(Equal(1))
			Expect(s.Failed).To(Equal(1))
			Expect(s.Committed).To(Equal(1))
		})
	})
})
