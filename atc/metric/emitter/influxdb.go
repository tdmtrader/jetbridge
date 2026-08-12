package emitter

import (
	"fmt"
	"maps"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/metric"

	influxclient "github.com/influxdata/influxdb1-client/v2"
)

type InfluxDBEmitter struct {
	Client        influxclient.Client
	Database      string
	BatchSize     int
	BatchDuration time.Duration

	batchMutex    sync.Mutex
	batch         []metric.Event
	lastBatchTime time.Time
}

type InfluxDBConfig struct {
	URL string `long:"influxdb-url" description:"InfluxDB server address to emit points to."`

	Database string `long:"influxdb-database" description:"InfluxDB database to write points to."`

	Username string `long:"influxdb-username" description:"InfluxDB server username."`
	Password string `long:"influxdb-password" description:"InfluxDB server password."`

	InsecureSkipVerify bool `long:"influxdb-insecure-skip-verify" description:"Skip SSL verification when emitting to InfluxDB."`

	BatchSize     uint32        `long:"influxdb-batch-size" default:"5000" description:"Number of points to batch together when emitting to InfluxDB."`
	BatchDuration time.Duration `long:"influxdb-batch-duration" default:"300s" description:"The duration to wait before emitting a batch of points to InfluxDB, disregarding influxdb-batch-size."`
}

func init() {
	metric.Metrics.RegisterEmitter(&InfluxDBConfig{})
}

func (config *InfluxDBConfig) Description() string { return "InfluxDB" }
func (config *InfluxDBConfig) IsConfigured() bool  { return config.URL != "" }

func (config *InfluxDBConfig) NewEmitter(_ map[string]string) (metric.Emitter, error) {
	client, err := influxclient.NewHTTPClient(influxclient.HTTPConfig{
		Addr:               config.URL,
		Username:           config.Username,
		Password:           config.Password,
		InsecureSkipVerify: config.InsecureSkipVerify,
		Timeout:            time.Minute,
	})
	if err != nil {
		return &InfluxDBEmitter{}, err
	}

	return &InfluxDBEmitter{
		Client:        client,
		Database:      config.Database,
		BatchSize:     int(config.BatchSize),
		BatchDuration: config.BatchDuration,
	}, nil
}

func emitBatch(emitter *InfluxDBEmitter, logger lager.Logger, events []metric.Event) {

	logger.Debug("influxdb-emit-batch", lager.Data{
		"size": len(events),
	})
	bp, err := influxclient.NewBatchPoints(influxclient.BatchPointsConfig{
		Database: emitter.Database,
	})
	if err != nil {
		logger.Error("failed-to-construct-batch-points", err)
		return
	}

	for _, event := range events {
		tags := map[string]string{
			"host": event.Host,
		}

		maps.Copy(tags, event.Attributes)

		point, err := influxclient.NewPoint(
			event.Name,
			tags,
			map[string]any{
				"value": event.Value,
			},
			event.Time,
		)
		if err != nil {
			logger.Error("failed-to-construct-point", err)
			continue
		}

		bp.AddPoint(point)
	}

	err = emitter.Client.Write(bp)
	if err != nil {
		logger.Error("failed-to-send-points",
			fmt.Errorf("%w: %v", metric.ErrFailedToEmit, err))
		return
	}
}

func (emitter *InfluxDBEmitter) Emit(logger lager.Logger, event metric.Event) {
	emitter.batchMutex.Lock()
	defer emitter.batchMutex.Unlock()

	if emitter.lastBatchTime.IsZero() {
		emitter.lastBatchTime = time.Now()
	}

	emitter.batch = append(emitter.batch, event)
	duration := time.Since(emitter.lastBatchTime)
	if len(emitter.batch) >= emitter.BatchSize || duration >= emitter.BatchDuration {
		logger.Debug("influxdb-pre-emit-batch", lager.Data{
			"influxdb-batch-size":     emitter.BatchSize,
			"current-batch-size":      len(emitter.batch),
			"influxdb-batch-duration": emitter.BatchDuration,
			"current-duration":        duration,
		})
		emitter.submitBatch(logger)
	}
}

func (emitter *InfluxDBEmitter) SubmitBatch(logger lager.Logger) {
	emitter.batchMutex.Lock()
	defer emitter.batchMutex.Unlock()

	emitter.submitBatch(logger)
}

func (emitter *InfluxDBEmitter) submitBatch(logger lager.Logger) {
	batchToSubmit := make([]metric.Event, len(emitter.batch))
	copy(batchToSubmit, emitter.batch)
	emitter.batch = make([]metric.Event, 0)
	emitter.lastBatchTime = time.Now()
	go emitBatch(emitter, logger, batchToSubmit)
}
