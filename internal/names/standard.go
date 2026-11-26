package names

// OpenTelemetry attributes ynr reads to find a record's tool and version. They are not ynr's own
// names, and no group in telemetry/registry references them now that ynr declares no metrics, so
// the generator does not emit them.
const (
	AttrServiceName    = "service.name"
	AttrServiceVersion = "service.version"
)
