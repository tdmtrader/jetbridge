package present

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

func Worker(workerInfo db.Worker) atc.Worker {
	version := ""
	if workerInfo.Version() != nil {
		version = *workerInfo.Version()
	}
	activeTasks, err := workerInfo.ActiveTasks()
	if err != nil {
		activeTasks = 0
	}

	atcWorker := atc.Worker{
		ActiveContainers: workerInfo.ActiveContainers(),
		ActiveVolumes:    workerInfo.ActiveVolumes(),
		ActiveTasks:      activeTasks,
		ResourceTypes:    workerInfo.ResourceTypes(),
		Platform:         workerInfo.Platform(),
		Tags:             workerInfo.Tags(),
		Name:             workerInfo.Name(),
		Team:             workerInfo.TeamName(),
		State:            string(workerInfo.State()),
		Version:          version,
		Ephemeral:        workerInfo.Ephemeral(),
	}

	if !workerInfo.StartTime().IsZero() {
		atcWorker.StartTime = workerInfo.StartTime().Unix()
	}

	return atcWorker
}
