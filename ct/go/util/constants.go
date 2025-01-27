package util

import (
	"path"
	"path/filepath"
	"time"

	"go.skia.org/infra/go/swarming"
)

const (
	// Use the CTFE proxy to Google Storage. See skbug.com/6762
	GCS_HTTP_LINK = "https://ct.skia.org/results/"

	// File names and dir names.
	CHROMIUM_BUILDS_DIR_NAME         = "chromium_builds"
	PAGESETS_DIR_NAME                = "page_sets"
	WEB_ARCHIVES_DIR_NAME            = "webpage_archives"
	SKPS_DIR_NAME                    = "skps"
	STORAGE_DIR_NAME                 = "storage"
	REPO_DIR_NAME                    = "skia-repo"
	TASKS_DIR_NAME                   = "tasks"
	BINARIES_DIR_NAME                = "binaries"
	LUA_TASKS_DIR_NAME               = "lua_runs"
	BENCHMARK_TASKS_DIR_NAME         = "benchmark_runs"
	CHROMIUM_PERF_TASKS_DIR_NAME     = "chromium_perf_runs"
	CHROMIUM_ANALYSIS_TASKS_DIR_NAME = "chromium_analysis_runs"
	FIX_ARCHIVE_TASKS_DIR_NAME       = "fix_archive_runs"
	TRACE_DOWNLOADS_DIR_NAME         = "trace_downloads"
	CHROMIUM_BUILD_ZIP_NAME          = "chromium_build.zip"

	// Limit the number of times CT tries to get a remote file before giving up.
	MAX_URI_GET_TRIES = 4

	// Pageset types supported by CT.
	PAGESET_TYPE_ALL             = "All"
	PAGESET_TYPE_100k            = "100k"
	PAGESET_TYPE_MOBILE_100k     = "Mobile100k"
	PAGESET_TYPE_10k             = "10k"
	PAGESET_TYPE_MOBILE_10k      = "Mobile10k"
	PAGESET_TYPE_DUMMY_1k        = "Dummy1k"       // Used for testing.
	PAGESET_TYPE_MOBILE_DUMMY_1k = "DummyMobile1k" // Used for testing.

	// Names of binaries executed by CT.
	BINARY_CHROME          = "chrome"
	BINARY_CHROME_WINDOWS  = "chrome.exe"
	BINARY_RECORD_WPR      = "record_wpr"
	BINARY_RUN_BENCHMARK   = "run_benchmark"
	BINARY_ANALYZE_METRICS = "analyze_metrics_ct.py"
	BINARY_GCLIENT         = "gclient"
	BINARY_NINJA           = "ninja"
	BINARY_LUA_PICTURES    = "lua_pictures"
	BINARY_SKPINFO         = "skpinfo"
	BINARY_ADB             = "adb"
	BINARY_MAIL            = "mail"
	BINARY_LUA             = "lua"

	// Platforms supported by CT.
	PLATFORM_ANDROID = "Android"
	PLATFORM_LINUX   = "Linux"
	PLATFORM_WINDOWS = "Windows"

	// Benchmarks supported by CT.
	BENCHMARK_SKPICTURE_PRINTER        = "skpicture_printer_ct"
	BENCHMARK_RR                       = "rasterize_and_record_micro_ct"
	BENCHMARK_REPAINT                  = "repaint_ct"
	BENCHMARK_LOADING                  = "loading.cluster_telemetry"
	BENCHMARK_SCREENSHOT               = "screenshot_ct"
	BENCHMARK_RENDERING                = "rendering.cluster_telemetry"
	BENCHMARK_USECOUNTER               = "usecounter_ct"
	BENCHMARK_LEAK_DETECTION           = "leak_detection.cluster_telemetry"
	BENCHMARK_MEMORY                   = "memory.cluster_telemetry"
	BENCHMARK_V8_LOADING               = "v8.loading.cluster_telemetry"
	BENCHMARK_V8_LOADING_RUNTIME_STATS = "v8.loading_runtime_stats.cluster_telemetry"
	BENCHMARK_GENERIC_TRACE            = "generic_trace_ct"

	// Logserver link. This is only accessible from Google corp.
	MASTER_LOGSERVER_LINK = "http://uberchromegw.corp.google.com/i/skia-ct-master/"

	// Default browser args when running benchmarks.
	DEFAULT_BROWSER_ARGS = ""
	// Default value column name to use when merging CSVs.
	DEFAULT_VALUE_COLUMN_NAME = "avg"

	// Use live sites flag.
	USE_LIVE_SITES_FLAGS = "--use-live-sites"
	// Pageset repeat flag.
	PAGESET_REPEAT_FLAG = "--pageset-repeat"
	// Run Benchmark timeout flag.
	RUN_BENCHMARK_TIMEOUT_FLAG = "--run-benchmark-timeout"
	// Max pages per bot flag.
	MAX_PAGES_PER_BOT = "--max-pages-per-bot"
	// Num of retries used by analysis task.
	NUM_ANALYSIS_RETRIES = "--num-analysis-retries"

	// Defaults for custom webpages.
	DEFAULT_CUSTOM_PAGE_ARCHIVEPATH = "dummy_path"

	// Timeouts

	PKILL_TIMEOUT              = 5 * time.Minute
	HTTP_CLIENT_TIMEOUT        = 30 * time.Minute
	FETCH_GN_TIMEOUT           = 2 * time.Minute
	GN_GEN_TIMEOUT             = 2 * time.Minute
	UPDATE_DEPOT_TOOLS_TIMEOUT = 5 * time.Minute

	// util.SyncDir
	GIT_PULL_TIMEOUT     = 30 * time.Minute
	GCLIENT_SYNC_TIMEOUT = 30 * time.Minute

	// util.ResetCheckout
	GIT_CHECKOUT_TIMEOUT = 10 * time.Minute
	GIT_REBASE_TIMEOUT   = 10 * time.Minute
	GIT_RESET_TIMEOUT    = 10 * time.Minute
	GIT_CLEAN_TIMEOUT    = 10 * time.Minute

	// util.CreateChromiumBuildOnSwarming
	SYNC_SKIA_IN_CHROME_TIMEOUT = 2 * time.Hour
	GIT_LS_REMOTE_TIMEOUT       = 5 * time.Minute
	GIT_APPLY_TIMEOUT           = 5 * time.Minute
	GN_CHROMIUM_TIMEOUT         = 30 * time.Minute
	NINJA_TIMEOUT               = 2 * time.Hour

	// util.InstallChromeAPK
	ADB_INSTALL_TIMEOUT = 15 * time.Minute

	// Capture Archives
	CAPTURE_ARCHIVES_DEFAULT_CT_BENCHMARK = "rasterize_and_record_micro_ct"

	// Capture SKPs
	REMOVE_INVALID_SKPS_TIMEOUT = 3 * time.Hour

	// Run Chromium Perf
	ADB_VERSION_TIMEOUT            = 5 * time.Minute
	ADB_ROOT_TIMEOUT               = 5 * time.Minute
	CSV_PIVOT_TABLE_MERGER_TIMEOUT = 10 * time.Minute
	CSV_MERGER_TIMEOUT             = 1 * time.Hour
	CSV_COMPARER_TIMEOUT           = 2 * time.Hour

	// Run Lua
	LUA_PICTURES_TIMEOUT   = 2 * time.Hour
	LUA_AGGREGATOR_TIMEOUT = 1 * time.Hour

	// Poller
	MAKE_ALL_TIMEOUT = 15 * time.Minute

	// Swarming constants.
	SWARMING_DIR_NAME               = "swarming"
	SWARMING_POOL                   = "CT"
	BUILD_OUTPUT_FILENAME           = "build_remote_dirs.txt"
	ISOLATE_TELEMETRY_FILENAME      = "isolate_telemetry_hash.txt"
	MAX_SWARMING_HARD_TIMEOUT_HOURS = 24
	// Timeouts.
	BATCHARCHIVE_TIMEOUT = 10 * time.Minute
	XVFB_TIMEOUT         = 5 * time.Minute

	// Isolate files for master scripts.
	CREATE_PAGESETS_MASTER_ISOLATE   = "create_pagesets_on_workers.isolate"
	CAPTURE_ARCHIVES_MASTER_ISOLATE  = "capture_archives_on_workers.isolate"
	CAPTURE_SKPS_MASTER_ISOLATE      = "capture_skps_on_workers.isolate"
	RUN_LUA_MASTER_ISOLATE           = "run_lua_on_workers.isolate"
	CHROMIUM_ANALYSIS_MASTER_ISOLATE = "run_chromium_analysis_on_workers.isolate"
	CHROMIUM_PERF_MASTER_ISOLATE     = "run_chromium_perf_on_workers.isolate"
	METRICS_ANALYSIS_MASTER_ISOLATE  = "metrics_analysis_on_workers.isolate"
	BUILD_CHROMIUM_MASTER_ISOLATE    = "build_chromium.isolate"
	// Isolate files for worker scripts.
	CREATE_PAGESETS_ISOLATE   = "create_pagesets.isolate"
	CAPTURE_ARCHIVES_ISOLATE  = "capture_archives.isolate"
	CAPTURE_SKPS_ISOLATE      = "capture_skps.isolate"
	RUN_LUA_ISOLATE           = "run_lua.isolate"
	CHROMIUM_ANALYSIS_ISOLATE = "chromium_analysis.isolate"
	CHROMIUM_PERF_ISOLATE     = "chromium_perf.isolate"
	METRICS_ANALYSIS_ISOLATE  = "metrics_analysis.isolate"
	// Isolate files for build scripts.
	BUILD_REPO_ISOLATE        = "build_repo.isolate"
	ISOLATE_TELEMETRY_ISOLATE = "isolate_telemetry.isolate"

	// Swarming links and params.
	// TODO(rmistry): The below link contains "st=1262304000000" which is from 2010. This is done so
	// that swarming will not use today's timestamp as default. See if there is a better way to handle
	// this.
	SWARMING_RUN_ID_ALL_TASKS_LINK_TEMPLATE   = "https://chrome-swarming.appspot.com/tasklist?l=500&c=name&c=created_ts&c=bot&c=duration&c=state&f=runid:%s&st=1262304000000"
	SWARMING_RUN_ID_TASK_LINK_PREFIX_TEMPLATE = SWARMING_RUN_ID_ALL_TASKS_LINK_TEMPLATE + "&f=name:%s"

	// Priorities
	TASKS_PRIORITY_HIGH   = swarming.RECOMMENDED_PRIORITY
	TASKS_PRIORITY_MEDIUM = swarming.RECOMMENDED_PRIORITY + 10
	TASKS_PRIORITY_LOW    = swarming.RECOMMENDED_PRIORITY + 20

	// ct-perf.skia.org constants.
	CT_PERF_BUCKET = "cluster-telemetry-perf"
	CT_PERF_REPO   = "https://skia.googlesource.com/perf-ct"

	MASTER_SERVICE_ACCOUNT = "ct-swarming-bots@ct-swarming-bots.iam.gserviceaccount.com"
)

type PagesetTypeInfo struct {
	NumPages                   int
	CSVSource                  string
	UserAgent                  string
	CaptureArchivesTimeoutSecs int
	CreatePagesetsTimeoutSecs  int
	CaptureSKPsTimeoutSecs     int
	RunChromiumPerfTimeoutSecs int
	Description                string
}

var (
	CtUser = "chrome-bot"
	// Whenever the bucket name changes, getGSLink in ctfe.js will have to be
	// updated as well.
	GCSBucketName = "cluster-telemetry"

	// Email address of cluster telemetry admins. They will be notified everytime
	// a task has started and completed.
	CtAdmins = []string{"rmistry@google.com", "benjaminwagner@google.com"}

	// Names of local directories and files.
	StorageDir             = filepath.Join("/", "b", STORAGE_DIR_NAME)
	RepoDir                = filepath.Join("/", "b", REPO_DIR_NAME)
	DepotToolsDir          = filepath.Join("/", "home", "chrome-bot", "depot_tools")
	ChromiumBuildsDir      = filepath.Join(StorageDir, CHROMIUM_BUILDS_DIR_NAME)
	ChromiumSrcDir         = filepath.Join(StorageDir, "chromium", "src")
	TelemetryBinariesDir   = filepath.Join(ChromiumSrcDir, "tools", "perf")
	TelemetrySrcDir        = filepath.Join(ChromiumSrcDir, "tools", "telemetry")
	RelativeCatapultSrcDir = filepath.Join("third_party", "catapult")
	CatapultSrcDir         = filepath.Join(ChromiumSrcDir, RelativeCatapultSrcDir)
	V8SrcDir               = filepath.Join(ChromiumSrcDir, "v8")
	TaskFileDir            = filepath.Join(StorageDir, "current_task")
	ClientSecretPath       = filepath.Join(StorageDir, "client_secret.json")
	GCSTokenPath           = filepath.Join(StorageDir, "google_storage_token.data")
	PagesetsDir            = filepath.Join(StorageDir, PAGESETS_DIR_NAME)
	WebArchivesDir         = filepath.Join(StorageDir, WEB_ARCHIVES_DIR_NAME)
	SkpsDir                = filepath.Join(StorageDir, SKPS_DIR_NAME)
	ApkName                = "ChromePublic.apk"
	SkiaTreeDir            = filepath.Join(RepoDir, "trunk")
	CtTreeDir              = filepath.Join(RepoDir, "go", "src", "go.skia.org", "infra", "ct")

	// Names of local and remote directories and files.
	BinariesDir                    = filepath.Join(BINARIES_DIR_NAME)
	LuaRunsDir                     = filepath.Join(TASKS_DIR_NAME, LUA_TASKS_DIR_NAME)
	BenchmarkRunsDir               = filepath.Join(TASKS_DIR_NAME, BENCHMARK_TASKS_DIR_NAME)
	BenchmarkRunsStorageDir        = path.Join(TASKS_DIR_NAME, BENCHMARK_TASKS_DIR_NAME)
	ChromiumPerfRunsDir            = filepath.Join(TASKS_DIR_NAME, CHROMIUM_PERF_TASKS_DIR_NAME)
	ChromiumPerfRunsStorageDir     = path.Join(TASKS_DIR_NAME, CHROMIUM_PERF_TASKS_DIR_NAME)
	ChromiumAnalysisRunsStorageDir = path.Join(TASKS_DIR_NAME, CHROMIUM_ANALYSIS_TASKS_DIR_NAME)
	FixArchivesRunsDir             = filepath.Join(TASKS_DIR_NAME, FIX_ARCHIVE_TASKS_DIR_NAME)
	TraceDownloadsDir              = filepath.Join(TASKS_DIR_NAME, TRACE_DOWNLOADS_DIR_NAME)

	// Information about the different CT pageset types.
	PagesetTypeToInfo = map[string]*PagesetTypeInfo{
		PAGESET_TYPE_ALL: {
			NumPages:                   1000000,
			CSVSource:                  "csv/top-1m.csv",
			UserAgent:                  "desktop",
			CreatePagesetsTimeoutSecs:  1800,
			CaptureArchivesTimeoutSecs: 300,
			CaptureSKPsTimeoutSecs:     300,
			RunChromiumPerfTimeoutSecs: 300,
			Description:                "Top 1M (with desktop user-agent)",
		},
		PAGESET_TYPE_100k: {
			NumPages:                   100000,
			CSVSource:                  "csv/top-1m.csv",
			UserAgent:                  "desktop",
			CreatePagesetsTimeoutSecs:  1800,
			CaptureArchivesTimeoutSecs: 300,
			CaptureSKPsTimeoutSecs:     300,
			RunChromiumPerfTimeoutSecs: 300,
			Description:                "Top 100K (with desktop user-agent)",
		},
		PAGESET_TYPE_MOBILE_100k: {
			NumPages:                   100000,
			CSVSource:                  "csv/android-top-1m.csv",
			UserAgent:                  "mobile",
			CreatePagesetsTimeoutSecs:  1800,
			CaptureArchivesTimeoutSecs: 300,
			CaptureSKPsTimeoutSecs:     300,
			RunChromiumPerfTimeoutSecs: 300,
			Description:                "Top 100K (with mobile user-agent)",
		},
		PAGESET_TYPE_10k: {
			NumPages:                   10000,
			CSVSource:                  "csv/top-1m.csv",
			UserAgent:                  "desktop",
			CreatePagesetsTimeoutSecs:  1800,
			CaptureArchivesTimeoutSecs: 300,
			CaptureSKPsTimeoutSecs:     300,
			RunChromiumPerfTimeoutSecs: 300,
			Description:                "Top 10K (with desktop user-agent)",
		},
		PAGESET_TYPE_MOBILE_10k: {
			NumPages:                   10000,
			CSVSource:                  "csv/android-top-1m.csv",
			UserAgent:                  "mobile",
			CreatePagesetsTimeoutSecs:  1800,
			CaptureArchivesTimeoutSecs: 300,
			CaptureSKPsTimeoutSecs:     300,
			RunChromiumPerfTimeoutSecs: 300,
			Description:                "Top 10K (with mobile user-agent)",
		},
		PAGESET_TYPE_DUMMY_1k: {
			NumPages:                   1000,
			CSVSource:                  "csv/top-1m.csv",
			UserAgent:                  "desktop",
			CreatePagesetsTimeoutSecs:  1800,
			CaptureArchivesTimeoutSecs: 300,
			CaptureSKPsTimeoutSecs:     300,
			RunChromiumPerfTimeoutSecs: 300,
			Description:                "Top 1K (with desktop user-agent, for testing, hidden from Runs History by default)",
		},
		PAGESET_TYPE_MOBILE_DUMMY_1k: {
			NumPages:                   1000,
			CSVSource:                  "csv/android-top-1m.csv",
			UserAgent:                  "mobile",
			CreatePagesetsTimeoutSecs:  1800,
			CaptureArchivesTimeoutSecs: 300,
			CaptureSKPsTimeoutSecs:     300,
			RunChromiumPerfTimeoutSecs: 300,
			Description:                "Top 1K (with mobile user-agent, for testing, hidden from Runs History by default)",
		},
	}

	// Frontend constants below.
	SupportedBenchmarksToDoc = map[string]string{
		BENCHMARK_RR:                       "https://cs.chromium.org/chromium/src/tools/perf/contrib/cluster_telemetry/rasterize_and_record_micro_ct.py",
		BENCHMARK_REPAINT:                  "https://cs.chromium.org/chromium/src/tools/perf/contrib/cluster_telemetry/repaint.py",
		BENCHMARK_LOADING:                  "https://cs.chromium.org/chromium/src/tools/perf/contrib/cluster_telemetry/v8_loading_ct.py",
		BENCHMARK_USECOUNTER:               "https://docs.google.com/document/d/1FSzJm2L2ow6pZTM_CuyHNJecXuX7Mx3XmBzL4SFHyLA/",
		BENCHMARK_LEAK_DETECTION:           "https://docs.google.com/document/d/1wUWa7dWUdvr6dLdYHFfMQdnvgzt7lrrvzYfpAK-_6e0/",
		BENCHMARK_RENDERING:                "https://cs.chromium.org/chromium/src/tools/perf/contrib/cluster_telemetry/rendering_ct.py",
		BENCHMARK_MEMORY:                   "https://cs.chromium.org/chromium/src/tools/perf/contrib/cluster_telemetry/memory_ct.py",
		BENCHMARK_V8_LOADING:               "https://cs.chromium.org/chromium/src/tools/perf/contrib/cluster_telemetry/v8_loading_ct.py",
		BENCHMARK_V8_LOADING_RUNTIME_STATS: "https://cs.chromium.org/chromium/src/tools/perf/contrib/cluster_telemetry/v8_loading_runtime_stats_ct.py",
		BENCHMARK_GENERIC_TRACE:            "https://docs.google.com/document/d/1vGd7dnrxayMYHPO72wWkwTvjMnIRrel4yxzCr1bMiis/",
	}

	SupportedPlatformsToDesc = map[string]string{
		PLATFORM_LINUX:   "Linux (Ubuntu18.04 machines)",
		PLATFORM_ANDROID: "Android (N5X devices)",
		PLATFORM_WINDOWS: "Windows (2016 DataCenter Server cloud instances)",
	}

	TaskPrioritiesToDesc = map[int]string{
		TASKS_PRIORITY_HIGH:   "High priority",
		TASKS_PRIORITY_MEDIUM: "Medium priority",
		TASKS_PRIORITY_LOW:    "Low priority",
	}

	// Swarming machine dimensions.
	GCE_LINUX_MASTER_DIMENSIONS = map[string]string{"pool": "CTMaster", "os": "Linux", "cores": "4"}

	GCE_LINUX_WORKER_DIMENSIONS   = map[string]string{"pool": SWARMING_POOL, "os": "Linux", "cores": "4"}
	GCE_WINDOWS_WORKER_DIMENSIONS = map[string]string{"pool": SWARMING_POOL, "os": "Windows", "cores": "4"}

	GCE_ANDROID_BUILDER_DIMENSIONS = map[string]string{"pool": "CTAndroidBuilder", "cores": "64"}
	GCE_LINUX_BUILDER_DIMENSIONS   = map[string]string{"pool": "CTLinuxBuilder", "cores": "64"}
	GCE_WINDOWS_BUILDER_DIMENSIONS = map[string]string{"pool": "CTBuilder", "os": "Windows"}

	GOLO_ANDROID_WORKER_DIMENSIONS = map[string]string{"pool": SWARMING_POOL, "os": "Android"}
	GOLO_LINUX_WORKER_DIMENSIONS   = map[string]string{"pool": SWARMING_POOL, "os": "Linux", "cores": "8"}

	// ct-perf.skia.org constants.
	CTPerfWorkDir = filepath.Join(StorageDir, "ct-perf-workdir")
)
