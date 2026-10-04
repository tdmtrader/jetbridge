// Package wire builds a Driver from a queue's config, for every command that runs the queue.
package wire

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/adapters/lognotify"
	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

// Driver wires the driver with runner; it logs through logf and a "-" log notifier writes to out.
func Driver(c config.Config, out io.Writer, logf func(string, ...any), runner core.Runner) (d *core.Driver, closeFn func(), err error) {
	notifier, err := newNotifier(c.Notify, out)
	if err != nil {
		return nil, nil, err
	}
	lander, err := git.New(c)
	if err != nil {
		return nil, nil, err
	}
	var legacy *git.Legacy
	if c.Admission.LegacyRefs != "" {
		legacy = &git.Legacy{Lander: lander, Prefix: c.Admission.LegacyRefs, Store: git.NewStore(c)}
	}
	b := c.Batch
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	host, _ := os.Hostname()
	return &core.Driver{
			Store: git.NewStore(c), Composer: git.NewComposer(c), Runner: runner, Lander: lander, Notifier: notifier,
			NewStrategy: func() core.Strategy {
				s := &core.Serial{Max: b.Max, Policy: core.Policy{RetryNone: b.RetryNone, Order: core.Order(b.Order)}}
				if a := b.Adaptive; a != nil {
					s.Adaptive = &core.Adaptive{Start: a.Start, Min: a.Min, GrowAfter: a.GrowAfter}
				}
				return s
			},
			Main: c.Repository.Main, Log: logf, Slots: 1, TTL: time.Minute, MaxFailures: c.Lander.MaxFailures, Cooldown: c.Pause.Cooldown,
			Admissions: &git.Admissions{Lander: lander, Prefix: c.Admission.Prefix, Operators: c.Admission.OperatorsFile, Legacy: legacy},
			Resumes:    &git.Resumes{Lander: lander, Prefix: c.Admission.ControlPrefix + "resume-", Operators: c.Admission.OperatorsFile},
			Promotes:   &git.Promotes{Lander: lander, Prefix: c.Admission.ControlPrefix + "promote/", Operators: c.Admission.OperatorsFile},
			Lifecycle:  &git.Lifecycle{Lander: lander, Prefix: c.Admission.ControlPrefix, Operators: c.Admission.OperatorsFile, Legacy: legacy},
			Owner:      fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(suffix)),
		}, func() {
			lander.Close()
			if c, ok := notifier.(io.Closer); ok {
				c.Close()
			}
		}, nil
}

type nopNotifier struct{}

func (nopNotifier) Notify(context.Context, core.Event) error { return nil }

func newNotifier(n yaml.Node, out io.Writer) (core.Notifier, error) {
	var k struct{ Kind string }
	if err := n.Decode(&k); err != nil {
		return nil, err
	}
	switch k.Kind {
	case "":
		return nopNotifier{}, nil
	case "log":
		c, err := lognotify.Parse(&n)
		if err != nil {
			return nil, err
		}
		return lognotify.Open(c, out)
	}
	return nil, fmt.Errorf("notify.kind %q is not supported by this command", k.Kind)
}
