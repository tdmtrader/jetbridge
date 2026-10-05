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
	b := c.Batch
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	host, _ := os.Hostname()
	return &core.Driver{
			Store: git.NewStore(c), Composer: git.NewComposer(c), Runner: runner, Lander: lander, Notifier: notifier,
			NewStrategy: func() core.Strategy {
				return &core.Serial{Max: b.Max, Policy: core.Policy{RetryNone: b.RetryNone, Order: core.Order(b.Order)}}
			},
			Main: c.Repository.Main, Log: logf, Slots: 1, TTL: time.Minute, Cooldown: c.Pause.Cooldown,
			Admissions: &git.Admissions{Lander: lander, Prefix: c.Admission.Prefix},
			Resumes:    &git.Resumes{Lander: lander, Prefix: c.Admission.ControlPrefix + "resume-"},
			Promotes:   &git.Promotes{Lander: lander, Prefix: c.Admission.ControlPrefix + "promote/"},
			Lifecycle:  &git.Lifecycle{Lander: lander, Prefix: c.Admission.ControlPrefix},
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
