package jetbridge

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// startedBuild is a build the lookup reports running: only its id is read.
type startedBuild struct {
	db.Build
	id int
}

func (b startedBuild) ID() int { return b.id }

type startedBuilds struct {
	ids []int
	err error
}

func (s startedBuilds) GetAllStartedBuilds() ([]db.Build, error) {
	var builds []db.Build
	for _, id := range s.ids {
		builds = append(builds, startedBuild{id: id})
	}
	return builds, s.err
}

// An exact execution's step Job is kept while its outcome may be unresolved,
// as the exec runtime's pause pod is: what keeps a pause pod is its build,
// which a Run does not finish while one of its executions is open, so its
// container is never marked for destruction. A step Job can outlive its pod,
// and the sweep of pod-less step Jobs is the one path that does not go
// through the container, so it asks the same question: an exact step Job of
// a build still running is not taken, nor one whose build cannot be looked
// up. An ordinary step Job is swept as before.
func TestTheReaperKeepsAPodlessExactStepJobWhileItsBuildRuns(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-podlessStepJobGrace - time.Minute))
	job := func(name, build string, exact bool) *batchv1.Job {
		labels := map[string]string{workerLabelKey: "k8s-test-ns", buildIDLabelKey: build}
		if exact {
			labels[exactExecutionLabelKey] = "33333333-3333-4333-8333-333333333333"
		}
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test-ns", UID: types.UID("uid-" + name), CreationTimestamp: old, Labels: labels}}
	}
	for _, tc := range []struct {
		name   string
		lookup RunningBuildLookup
		want   string
	}{
		{"its build is running", startedBuilds{ids: []int{7}}, "exact-running"},
		{"no build is running", startedBuilds{}, ""},
		{"the running builds cannot be listed", startedBuilds{err: errors.New("database is down")}, "exact-finished,exact-running"},
		{"there is no build lookup", nil, "exact-finished,exact-running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			clientset := fake.NewSimpleClientset()
			for _, j := range []*batchv1.Job{
				job("exact-running", "7", true),
				job("exact-finished", "8", true),
				job("ordinary-running", "7", false),
			} {
				if _, err := clientset.BatchV1().Jobs("test-ns").Create(ctx, j, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			reaper := &Reaper{clientset: clientset, cfg: NewConfig("test-ns", "")}
			if tc.lookup != nil {
				reaper.SetBuildLookup(tc.lookup)
			}
			reaper.reapPodlessStepJobs(ctx, lagertest.NewTestLogger("reaper"), "k8s-test-ns")
			remaining, _ := clientset.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
			var names []string
			for _, j := range remaining.Items {
				names = append(names, j.Name)
			}
			sort.Strings(names)
			if got := strings.Join(names, ","); got != tc.want {
				t.Errorf("remaining Jobs = %q, want %q", got, tc.want)
			}
		})
	}
}
