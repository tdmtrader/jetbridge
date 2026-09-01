package steps

import (
	"fmt"
	"sync"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
)

type MetricPrimitiveObservation struct{ Value string }

func MetricPrimitiveDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, MetricPrimitiveObservation](
			"the production metric primitive evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (MetricPrimitiveObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return MetricPrimitiveObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeMetricPrimitive(database, profile)
				return MetricPrimitiveObservation{Value: value}, err
			},
		),
		CheckString[MetricPrimitiveObservation]("the metric primitive result is {string}", "metric primitive result", func(in MetricPrimitiveObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func observeMetricPrimitive(database JetbridgeDB, profile string) (string, error) {
	switch profile {
	case "counter-inc", "counter-delta", "counter-reset":
		counter := &metric.Counter{}
		if profile == "counter-delta" {
			counter.IncDelta(3)
		} else {
			counter.Inc()
			counter.Inc()
			counter.Inc()
		}
		first := counter.Delta()
		if profile == "counter-reset" {
			return fmt.Sprintf("first=%g;second=%g", first, counter.Delta()), nil
		}
		return fmt.Sprintf("value=%g", first), nil
	case "gauge-max", "gauge-reset":
		gauge := &metric.Gauge{}
		gauge.Inc()
		gauge.Inc()
		gauge.Dec()
		first := gauge.Max()
		if profile == "gauge-reset" {
			return fmt.Sprintf("first=%g;second=%g", first, gauge.Max()), nil
		}
		return fmt.Sprintf("value=%g", first), nil
	case "gauge-concurrent":
		gauge := &metric.Gauge{}
		var wg sync.WaitGroup
		wg.Add(30)
		for range 30 {
			go func() { defer wg.Done(); gauge.Inc() }()
		}
		wg.Wait()
		return fmt.Sprintf("value=%g", gauge.Max()), nil
	case "query-passthrough":
		metric.Metrics.DatabaseQueries.Delta()
		counting := metric.CountQueries(database.Conn)
		pingErr := counting.Ping()
		return fmt.Sprintf("ping=%t;name=%t;count=%g", pingErr == nil, counting.Name() != "", metric.Metrics.DatabaseQueries.Delta()), nil
	case "query-errors":
		closed, err := database.ClosedConn()
		if err != nil {
			return "", err
		}
		counting := metric.CountQueries(closed)
		pingErr := counting.Ping()
		rows, queryErr := counting.Query("SELECT $1::int", 1)
		if rows != nil {
			_ = rows.Close()
		}
		metric.Metrics.DatabaseQueries.Delta()
		return fmt.Sprintf("ping-error=%t;query-error=%t", pingErr != nil, queryErr != nil), nil
	case "query-count":
		metric.Metrics.DatabaseQueries.Delta()
		counting := metric.CountQueries(database.Conn)
		rows, err := counting.Query("SELECT $1::int", 1)
		if err != nil {
			return "", err
		}
		if err = rows.Close(); err != nil {
			return "", err
		}
		queryCount := metric.Metrics.DatabaseQueries.Delta()
		if _, err = counting.Exec("SELECT $1::int", 1); err != nil {
			return "", err
		}
		var value int
		if err = counting.QueryRow("SELECT $1::int", 1).Scan(&value); err != nil {
			return "", err
		}
		execRowCount := metric.Metrics.DatabaseQueries.Delta()
		tx, err := counting.Begin()
		if err != nil {
			return "", err
		}
		defer db.Rollback(tx)
		rows, err = tx.Query("SELECT $1::int", 1)
		if err != nil {
			return "", err
		}
		if err = rows.Close(); err != nil {
			return "", err
		}
		return fmt.Sprintf("query=%g;exec-row=%g;transaction-query=%g", queryCount, execRowCount, metric.Metrics.DatabaseQueries.Delta()), nil
	default:
		return "", fmt.Errorf("unknown metric primitive profile %q", profile)
	}
}
