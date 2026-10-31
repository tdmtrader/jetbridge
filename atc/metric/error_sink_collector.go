package metric

import (
	"code.cloudfoundry.org/lager/v3"
)

// ErrorSink is a lager sink that counts every ERROR-level log line by its
// message.
type ErrorSink struct{}

func (ErrorSink) Log(f lager.LogFormat) {
	if f.LogLevel != lager.ERROR {
		return
	}

	errorLogs.WithLabelValues(f.Message).Inc()
}
