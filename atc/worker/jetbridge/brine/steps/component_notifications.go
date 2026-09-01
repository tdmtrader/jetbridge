package steps

import (
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type ComponentNotificationObservation struct{ Value string }

func ComponentNotificationDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, ComponentNotificationObservation](
			"real PostgreSQL component notifications evaluate profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (ComponentNotificationObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return ComponentNotificationObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeComponentNotifications(database, profile)
				return ComponentNotificationObservation{Value: value}, err
			},
		),
		CheckString[ComponentNotificationObservation]("the component notification result is {string}", "component notification result", func(in ComponentNotificationObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func observeComponentNotifications(database JetbridgeDB, profile string) (string, error) {
	switch profile {
	case "resource":
		return observeResourceNotifications(database)
	case "build-finish":
		return observeBuildFinishNotifications(database)
	case "pipeline":
		return observePipelineNotifications(database)
	case "resource-type":
		return observeResourceTypeNotifications(database)
	default:
		return "", fmt.Errorf("unknown component notification profile %q", profile)
	}
}

func observeResourceNotifications(database JetbridgeDB) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "notification-resource-team"})
	if err != nil {
		return "", err
	}
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "notification-resource-pipeline"},
		atc.Config{
			Resources: atc.ResourceConfigs{{Name: "resource", Type: "registry-image", Source: atc.Source{"repository": "example/image"}}},
			Jobs:      atc.JobConfigs{{Name: "job", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "resource"}}}}},
		}, 0, false,
	)
	if err != nil {
		return "", err
	}
	resource, found, err := pipeline.Resource("resource")
	if err != nil || !found {
		return "", fmt.Errorf("load notification resource: found=%t: %w", found, err)
	}
	configFactory := db.NewResourceConfigFactory(database.Conn, database.LockFactory)
	config, err := configFactory.FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)
	if err != nil {
		return "", err
	}
	resourceID := resource.ID()
	scope, err := config.FindOrCreateScope(&resourceID)
	if err != nil {
		return "", err
	}

	changed, err := notificationFrom(database, atc.ComponentLidarScanner, func() error { return resource.SetResourceConfigScope(scope) })
	if err != nil {
		return "", err
	}
	same, err := notificationFrom(database, atc.ComponentLidarScanner, func() error { return resource.SetResourceConfigScope(scope) })
	if err != nil {
		return "", err
	}
	newVersion, err := notificationFrom(database, atc.ComponentLidarScanner, func() error {
		return scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
	})
	if err != nil {
		return "", err
	}
	existingVersion, err := notificationFrom(database, atc.ComponentLidarScanner, func() error {
		return scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
	})
	if err != nil {
		return "", err
	}
	checkTime, err := notificationFrom(database, atc.ComponentLidarScanner, func() error {
		_, updateErr := scope.UpdateLastCheckEndTime(true)
		return updateErr
	})
	if err != nil {
		return "", err
	}
	version, found, err := scope.FindVersion(atc.Version{"ver": "1"})
	if err != nil || !found {
		return "", fmt.Errorf("find notification version: found=%t: %w", found, err)
	}
	pin, err := notificationFrom(database, atc.ComponentLidarScanner, func() error {
		_, pinErr := resource.PinVersion(version.ID())
		return pinErr
	})
	if err != nil {
		return "", err
	}
	unpin, err := notificationFrom(database, atc.ComponentLidarScanner, resource.UnpinVersion)
	if err != nil {
		return "", err
	}
	disable, err := notificationFrom(database, atc.ComponentLidarScanner, func() error { return resource.DisableVersion(version.ID()) })
	if err != nil {
		return "", err
	}
	enable, err := notificationFrom(database, atc.ComponentLidarScanner, func() error { return resource.EnableVersion(version.ID()) })
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("scope=%t;same=%t;pin=%t;unpin=%t;disable=%t;enable=%t;new=%t;existing=%t;check-time=%t", changed, same, pin, unpin, disable, enable, newVersion, existingVersion, checkTime), nil
}

func observeBuildFinishNotifications(database JetbridgeDB) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "notification-build-team"})
	if err != nil {
		return "", err
	}
	build, err := team.CreateOneOffBuild()
	if err != nil {
		return "", err
	}
	if _, err := build.Start(atc.Plan{}); err != nil {
		return "", err
	}
	channels := []string{
		atc.ComponentSyslogDrainer,
		atc.ComponentBuildReaper,
		atc.ComponentCollectorBuilds,
		atc.ComponentCollectorResourceCacheUses,
		atc.ComponentCollectorChecks,
		atc.ComponentCollectorResourceCaches,
	}
	received, err := notificationsFrom(database, channels, func() error { return build.Finish(db.BuildStatusSucceeded) })
	return fmt.Sprintf("all=%t;count=%d", received, len(channels)), err
}

func observePipelineNotifications(database JetbridgeDB) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "notification-pipeline-team"})
	if err != nil {
		return "", err
	}
	create := func(name string) (db.Pipeline, error) {
		pipeline, _, saveErr := team.SavePipeline(
			atc.PipelineRef{Name: name},
			atc.Config{Jobs: atc.JobConfigs{{Name: "job", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "task", ConfigPath: "task.yml"}}}}}},
			0, false,
		)
		return pipeline, saveErr
	}
	archived, err := create("archive")
	if err != nil {
		return "", err
	}
	archiveBoth, err := notificationsFrom(database, []string{atc.ComponentCollectorPipelines, atc.ComponentCollectorTaskCaches}, archived.Archive)
	if err != nil {
		return "", err
	}
	destroyed, err := create("destroy")
	if err != nil {
		return "", err
	}
	destroy, err := notificationFrom(database, atc.ComponentCollectorPipelines, destroyed.Destroy)
	if err != nil {
		return "", err
	}
	paused, err := create("pause")
	if err != nil {
		return "", err
	}
	pause, err := notificationFrom(database, atc.ComponentCollectorTaskCaches, func() error { return paused.Pause("brine-user") })
	return fmt.Sprintf("archive=%t;destroy=%t;pause=%t", archiveBoth, destroy, pause), err
}

func observeResourceTypeNotifications(database JetbridgeDB) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "notification-type-team"})
	if err != nil {
		return "", err
	}
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "notification-type-pipeline"},
		atc.Config{ResourceTypes: atc.ResourceTypes{{Name: "type", Type: "registry-image", Source: atc.Source{"repository": "example/type"}}}},
		0, false,
	)
	if err != nil {
		return "", err
	}
	resourceType, found, err := pipeline.ResourceType("type")
	if err != nil || !found {
		return "", fmt.Errorf("load notification resource type: found=%t: %w", found, err)
	}
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(resourceType.Type(), resourceType.Source(), nil)
	if err != nil {
		return "", err
	}
	scope, err := config.FindOrCreateScope(nil)
	if err != nil {
		return "", err
	}
	changed, err := notificationFrom(database, atc.ComponentLidarScanner, func() error { return resourceType.SetResourceConfigScope(scope) })
	if err != nil {
		return "", err
	}
	same, err := notificationFrom(database, atc.ComponentLidarScanner, func() error { return resourceType.SetResourceConfigScope(scope) })
	return fmt.Sprintf("changed=%t;same=%t", changed, same), err
}

func notificationFrom(database JetbridgeDB, channel string, action func() error) (bool, error) {
	bus := database.Conn.Bus()
	signal, err := bus.ListenSignal(channel)
	if err != nil {
		return false, err
	}
	defer func() { _ = bus.UnlistenSignal(channel, signal) }()
	if err := action(); err != nil {
		return false, err
	}
	select {
	case <-signal.C():
		return true, nil
	case <-time.After(250 * time.Millisecond):
		return false, nil
	}
}

func notificationsFrom(database JetbridgeDB, channels []string, action func() error) (bool, error) {
	bus := database.Conn.Bus()
	signals := make([]*db.NotifySignal, 0, len(channels))
	for _, channel := range channels {
		signal, err := bus.ListenSignal(channel)
		if err != nil {
			return false, err
		}
		signals = append(signals, signal)
	}
	defer func() {
		for i, channel := range channels {
			_ = bus.UnlistenSignal(channel, signals[i])
		}
	}()
	if err := action(); err != nil {
		return false, err
	}
	for _, signal := range signals {
		select {
		case <-signal.C():
		case <-time.After(time.Second):
			return false, nil
		}
	}
	return true, nil
}
