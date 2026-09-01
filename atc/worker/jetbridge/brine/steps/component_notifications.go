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
			"real PostgreSQL component notifications evaluate strict profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (ComponentNotificationObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return ComponentNotificationObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				received, err := observeStrictComponentNotification(database, profile)
				return ComponentNotificationObservation{Value: fmt.Sprintf("received=%t", received)}, err
			},
		),
		CheckString[ComponentNotificationObservation]("the component notification result is {string}", "component notification result", func(in ComponentNotificationObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func observeStrictComponentNotification(database JetbridgeDB, profile string) (bool, error) {
	switch profile {
	case "resource-scope-changed", "resource-scope-same", "resource-pin", "resource-unpin", "resource-disable", "resource-enable", "resource-save-new", "resource-save-existing", "resource-check-time":
		return observeStrictResourceNotification(database, profile)
	case "finish-syslog":
		return observeStrictBuildFinishNotification(database, atc.ComponentSyslogDrainer)
	case "finish-build-reaper":
		return observeStrictBuildFinishNotification(database, atc.ComponentBuildReaper)
	case "finish-builds":
		return observeStrictBuildFinishNotification(database, atc.ComponentCollectorBuilds)
	case "finish-cache-uses":
		return observeStrictBuildFinishNotification(database, atc.ComponentCollectorResourceCacheUses)
	case "finish-checks":
		return observeStrictBuildFinishNotification(database, atc.ComponentCollectorChecks)
	case "finish-resource-caches":
		return observeStrictBuildFinishNotification(database, atc.ComponentCollectorResourceCaches)
	case "archive-pipelines":
		return observeStrictPipelineNotification(database, "archive", atc.ComponentCollectorPipelines)
	case "archive-task-caches":
		return observeStrictPipelineNotification(database, "archive", atc.ComponentCollectorTaskCaches)
	case "destroy-pipelines":
		return observeStrictPipelineNotification(database, "destroy", atc.ComponentCollectorPipelines)
	case "pause-task-caches":
		return observeStrictPipelineNotification(database, "pause", atc.ComponentCollectorTaskCaches)
	case "resource-type-scope-changed", "resource-type-scope-same":
		return observeStrictResourceTypeNotification(database, profile)
	default:
		return false, fmt.Errorf("unknown strict component notification profile %q", profile)
	}
}

func observeStrictResourceNotification(database JetbridgeDB, profile string) (bool, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "strict-notification-resource-team"})
	if err != nil {
		return false, err
	}
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "strict-notification-resource-pipeline"},
		atc.Config{
			Resources: atc.ResourceConfigs{{Name: "resource", Type: "registry-image", Source: atc.Source{"repository": "example/image"}}},
			Jobs:      atc.JobConfigs{{Name: "job", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "resource"}}}}},
		}, 0, false,
	)
	if err != nil {
		return false, err
	}
	resource, found, err := pipeline.Resource("resource")
	if err != nil || !found {
		return false, fmt.Errorf("load strict notification resource: found=%t: %w", found, err)
	}
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)
	if err != nil {
		return false, err
	}
	resourceID := resource.ID()
	scope, err := config.FindOrCreateScope(&resourceID)
	if err != nil {
		return false, err
	}

	switch profile {
	case "resource-scope-changed":
		return notificationFrom(database, atc.ComponentLidarScanner, func() error { return resource.SetResourceConfigScope(scope) })
	case "resource-scope-same":
		if err := resource.SetResourceConfigScope(scope); err != nil {
			return false, err
		}
		return notificationFrom(database, atc.ComponentLidarScanner, func() error { return resource.SetResourceConfigScope(scope) })
	case "resource-save-new":
		return notificationFrom(database, atc.ComponentLidarScanner, func() error {
			return scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
		})
	case "resource-save-existing":
		if err := scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}}); err != nil {
			return false, err
		}
		return notificationFrom(database, atc.ComponentLidarScanner, func() error {
			return scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
		})
	case "resource-check-time":
		return notificationFrom(database, atc.ComponentLidarScanner, func() error {
			_, updateErr := scope.UpdateLastCheckEndTime(true)
			return updateErr
		})
	}

	if err := scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}}); err != nil {
		return false, err
	}
	version, found, err := scope.FindVersion(atc.Version{"ver": "1"})
	if err != nil || !found {
		return false, fmt.Errorf("find strict notification version: found=%t: %w", found, err)
	}
	switch profile {
	case "resource-pin":
		return notificationFrom(database, atc.ComponentLidarScanner, func() error {
			_, pinErr := resource.PinVersion(version.ID())
			return pinErr
		})
	case "resource-unpin":
		if _, err := resource.PinVersion(version.ID()); err != nil {
			return false, err
		}
		return notificationFrom(database, atc.ComponentLidarScanner, resource.UnpinVersion)
	case "resource-disable":
		return notificationFrom(database, atc.ComponentLidarScanner, func() error { return resource.DisableVersion(version.ID()) })
	case "resource-enable":
		if err := resource.DisableVersion(version.ID()); err != nil {
			return false, err
		}
		return notificationFrom(database, atc.ComponentLidarScanner, func() error { return resource.EnableVersion(version.ID()) })
	default:
		return false, fmt.Errorf("unhandled strict resource notification profile %q", profile)
	}
}

func observeStrictBuildFinishNotification(database JetbridgeDB, channel string) (bool, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "strict-notification-build-team"})
	if err != nil {
		return false, err
	}
	build, err := team.CreateOneOffBuild()
	if err != nil {
		return false, err
	}
	if _, err := build.Start(atc.Plan{}); err != nil {
		return false, err
	}
	return notificationFrom(database, channel, func() error { return build.Finish(db.BuildStatusSucceeded) })
}

func observeStrictPipelineNotification(database JetbridgeDB, action, channel string) (bool, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "strict-notification-pipeline-team"})
	if err != nil {
		return false, err
	}
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "strict-notification-pipeline"},
		atc.Config{Jobs: atc.JobConfigs{{Name: "job", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "task", ConfigPath: "task.yml"}}}}}},
		0, false,
	)
	if err != nil {
		return false, err
	}
	var mutate func() error
	switch action {
	case "archive":
		mutate = pipeline.Archive
	case "destroy":
		mutate = pipeline.Destroy
	case "pause":
		mutate = func() error { return pipeline.Pause("brine-user") }
	default:
		return false, fmt.Errorf("unknown strict pipeline notification action %q", action)
	}
	return notificationFrom(database, channel, mutate)
}

func observeStrictResourceTypeNotification(database JetbridgeDB, profile string) (bool, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "strict-notification-type-team"})
	if err != nil {
		return false, err
	}
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "strict-notification-type-pipeline"},
		atc.Config{ResourceTypes: atc.ResourceTypes{{Name: "type", Type: "registry-image", Source: atc.Source{"repository": "example/type"}}}},
		0, false,
	)
	if err != nil {
		return false, err
	}
	resourceType, found, err := pipeline.ResourceType("type")
	if err != nil || !found {
		return false, fmt.Errorf("load strict notification resource type: found=%t: %w", found, err)
	}
	config, err := db.NewResourceConfigFactory(database.Conn, database.LockFactory).FindOrCreateResourceConfig(resourceType.Type(), resourceType.Source(), nil)
	if err != nil {
		return false, err
	}
	scope, err := config.FindOrCreateScope(nil)
	if err != nil {
		return false, err
	}
	if profile == "resource-type-scope-same" {
		if err := resourceType.SetResourceConfigScope(scope); err != nil {
			return false, err
		}
	}
	return notificationFrom(database, atc.ComponentLidarScanner, func() error { return resourceType.SetResourceConfigScope(scope) })
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
