package concourse

import (
	"strconv"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
	"github.com/tedsuo/rata"
)

// HangarStatus reads the Hangar output plane's operator status (admin only).
func (client *client) HangarStatus() (atc.HangarStatus, error) {
	var status atc.HangarStatus
	err := client.connection.Send(internal.Request{
		RequestName: atc.GetHangarStatus,
	}, &internal.Response{
		Result: &status,
	})
	return status, err
}

// ResolveHangarFinding closes one open integrity finding by id (admin only).
func (client *client) ResolveHangarFinding(id int64) error {
	return client.connection.Send(internal.Request{
		RequestName: atc.ResolveHangarFinding,
		Params:      rata.Params{"finding_id": strconv.FormatInt(id, 10)},
	}, &internal.Response{})
}
