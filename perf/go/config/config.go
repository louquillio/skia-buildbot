package config

const (
	// MAX_SAMPLE_TRACES_PER_CLUSTER  is the maximum number of traces stored in a
	// ClusterSummary.
	MAX_SAMPLE_TRACES_PER_CLUSTER = 50

	// MIN_STDDEV is the smallest standard deviation we will normalize, smaller
	// than this and we presume it's a standard deviation of zero.
	MIN_STDDEV = 0.001

	// GOTO_RANGE is the number of commits on either side of a target
	// commit we will display when going through the goto redirector.
	GOTO_RANGE = 10

	CONSTRUCTOR_NANO        = "nano"
	CONSTRUCTOR_NANO_TRYBOT = "nano-trybot"
)

// PerfBigTableConfig contains all the info needed by btts.BigTableTraceStore.
//
// May eventually move to a separate config file.
type PerfBigTableConfig struct {
	TileSize int32
	Project  string
	Instance string
	Table    string
	Topic    string
	GitUrl   string
	Shards   int32
	Sources  []string // List of gs: locations.
	Branches []string // If populated then restrict to ingesting just these branches.

	// FileIngestionTopicName is the PubSub topic name we should use if doing
	// event driven regression detection. The ingesters use this to know where
	// to emit events to, and the clusterers use this to know where to make a
	// subscription.
	//
	// Should only be turned on for instances that have a huge amount of data,
	// i.e. >500k traces, and that have sparse data.
	FileIngestionTopicName string
}

const (
	NANO         = "nano"
	ANDROID_PROD = "android-prod"
	CT_PROD      = "ct-prod"
	ANDROID_X    = "android-x"
)

var (
	PERF_BIGTABLE_CONFIGS = map[string]*PerfBigTableConfig{
		NANO: {
			TileSize:               256,
			Project:                "skia-public",
			Instance:               "production",
			Table:                  "perf-skia",
			Topic:                  "perf-ingestion-skia-production",
			GitUrl:                 "https://skia.googlesource.com/skia",
			Shards:                 8,
			Sources:                []string{"gs://skia-perf/nano-json-v1", "gs://skia-perf/task-duration", "gs://skia-perf/buildstats-json-v1"},
			Branches:               []string{},
			FileIngestionTopicName: "",
		},
		ANDROID_PROD: {
			TileSize:               8192,
			Project:                "skia-public",
			Instance:               "production",
			Table:                  "perf-android",
			Topic:                  "perf-ingestion-android-production",
			GitUrl:                 "https://skia.googlesource.com/perf-buildid/android-master",
			Shards:                 8,
			Sources:                []string{"gs://skia-perf/android-master-ingest"},
			Branches:               []string{},
			FileIngestionTopicName: "perf-ingestion-complete-android-production",
		},
		CT_PROD: {
			TileSize:               256,
			Project:                "skia-public",
			Instance:               "production",
			Table:                  "perf-ct",
			Topic:                  "perf-ingestion-ct-production",
			GitUrl:                 "https://skia.googlesource.com/perf-ct",
			Shards:                 8,
			Sources:                []string{"gs://cluster-telemetry-perf/ingest"},
			Branches:               []string{},
			FileIngestionTopicName: "",
		},
		ANDROID_X: { // https://bug.skia.org/9315
			TileSize:               512,
			Project:                "skia-public",
			Instance:               "production",
			Table:                  "perf-android-x",
			Topic:                  "perf-ingestion-android-x-production",
			GitUrl:                 "https://skia.googlesource.com/perf-buildid/android-master",
			Shards:                 8,
			Sources:                []string{"gs://skia-perf/android-master-ingest"},
			Branches:               []string{"aosp-androidx-master-dev"},
			FileIngestionTopicName: "",
		},
	}
)
