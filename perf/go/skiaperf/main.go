package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"math/rand"
	"net/http"
	"net/http/pprof"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/bigtable"
	"cloud.google.com/go/datastore"
	storage "cloud.google.com/go/storage"
	"contrib.go.opencensus.io/exporter/stackdriver"
	"github.com/gorilla/mux"
	"go.opencensus.io/trace"
	"go.skia.org/infra/go/auth"
	"go.skia.org/infra/go/calc"
	"go.skia.org/infra/go/common"
	"go.skia.org/infra/go/ds"
	"go.skia.org/infra/go/email"
	"go.skia.org/infra/go/git/gitinfo"
	"go.skia.org/infra/go/httputils"
	"go.skia.org/infra/go/login"
	"go.skia.org/infra/go/paramtools"
	"go.skia.org/infra/go/query"
	"go.skia.org/infra/go/sklog"
	"go.skia.org/infra/go/util"
	"go.skia.org/infra/go/vcsinfo"
	"go.skia.org/infra/perf/go/activitylog"
	"go.skia.org/infra/perf/go/alertfilter"
	"go.skia.org/infra/perf/go/alerts"
	"go.skia.org/infra/perf/go/btts"
	"go.skia.org/infra/perf/go/bug"
	"go.skia.org/infra/perf/go/cid"
	"go.skia.org/infra/perf/go/config"
	"go.skia.org/infra/perf/go/dataframe"
	"go.skia.org/infra/perf/go/dfbuilder"
	"go.skia.org/infra/perf/go/dryrun"
	"go.skia.org/infra/perf/go/notify"
	"go.skia.org/infra/perf/go/psrefresh"
	"go.skia.org/infra/perf/go/regression"
	"go.skia.org/infra/perf/go/shortcut2"
	"go.skia.org/infra/perf/go/types"
	"google.golang.org/api/option"
)

const (
	GMAIL_TOKEN_CACHE_FILE = "google_email_token.data"
	FROM_ADDRESS           = "alertserver@skia.org"

	// REGRESSION_COUNT_DURATION is how far back we look for regression in the /_/reg/count endpoint.
	REGRESSION_COUNT_DURATION = -14 * 24 * time.Hour

	// DEFAULT_ALERT_CATEGORY is the category that will be used by the /_/alerts/ endpoint.
	DEFAULT_ALERT_CATEGORY = "Prod"

	// PARAMSET_REFRESHER_PERIOD is how often we refresh our canonical paramset from the OPS's
	// stored in the last two bigtable tiles.
	PARAMSET_REFRESHER_PERIOD = 5 * time.Minute

	// START_CLUSTER_DELAY is the time we wait between starting each clusterer, to avoid hammering
	// the trace store all at once.
	START_CLUSTER_DELAY = 2 * time.Second
)

var (
	// TODO(jcgregorio) Make into a flag.
	BEGINNING_OF_TIME = time.Date(2014, time.June, 18, 0, 0, 0, 0, time.UTC)

	DEFAULT_BUG_URI_TEMPLATE = "https://bugs.chromium.org/p/skia/issues/entry?comment=This+bug+was+found+via+SkiaPerf.%0A%0AVisit+this+URL+to+see+the+details+of+the+suspicious+cluster%3A%0A%0A++{cluster_url}%0A%0AThe+suspect+commit+is%3A%0A%0A++{commit_url}%0A%0A++{message}&labels=FromSkiaPerf%2CType-Defect%2CPriority-Medium"
)

var (
	activityHandlerPath = regexp.MustCompile(`/activitylog/([0-9]*)$`)

	vcs vcsinfo.VCS

	cidl *cid.CommitIDLookup = nil
)

// flags
var (
	algo                           = flag.String("algo", "kmeans", "The algorithm to use for detecting regressions (kmeans|stepfit).")
	bigTableConfig                 = flag.String("big_table_config", "nano", "The name of the config to use when using a BigTable trace store.")
	clusterOnly                    = flag.Bool("cluster_only", true, "If true then run continuous clustering and not the UI.")
	commitRangeURL                 = flag.String("commit_range_url", "", "A URI Template to be used for expanding details on a range of commits, from {begin} to {end} git hash. See cluster-summary2-sk.")
	dataFrameSize                  = flag.Int("dataframe_size", dataframe.DEFAULT_NUM_COMMITS, "The number of commits to include in the default dataframe.")
	defaultSparse                  = flag.Bool("default_sparse", false, "The default value for 'Sparse' in Alerts.")
	doClustering                   = flag.Bool("do_clustering", true, "If true then run continuous clustering over all the alerts.")
	noemail                        = flag.Bool("noemail", false, "Do not send emails.")
	emailClientIdFlag              = flag.String("email_clientid", "", "OAuth Client ID for sending email.")
	emailClientSecretFile          = flag.String("email_client_secret_file", "client_secret.json", "OAuth client secret JSON file for sending email.")
	emailClientSecretFlag          = flag.String("email_clientsecret", "", "OAuth Client Secret for sending email.")
	emailTokenCacheFile            = flag.String("email_token_cache_file", "client_token.json", "OAuth token cache file for sending email.")
	eventDrivenRegressionDetection = flag.Bool("event_driven_regression_detection", false, "If true then regression detection is done based on PubSub events.")
	gitRepoDir                     = flag.String("git_repo_dir", "../../../skia", "Directory location for the Skia repo.")
	interesting                    = flag.Float64("interesting", 50.0, "The threshold value beyond which StepFit.Regression values become interesting, i.e. they may indicate real regressions or improvements.")
	internalOnly                   = flag.Bool("internal_only", false, "Require the user to be logged in to see any page.")
	keyOrder                       = flag.String("key_order", "build_flavor,name,sub_result,source_type", "The order that keys should be presented in for searching. All keys that don't appear here will appear after, in alphabetical order.")
	local                          = flag.Bool("local", false, "Running locally if true. As opposed to in production.")
	namespace                      = flag.String("namespace", "", "The Cloud Datastore namespace, such as 'perf'.")
	numContinuous                  = flag.Int("num_continuous", 50, "The number of commits to do continuous clustering over looking for regressions.")
	numContinuousParallel          = flag.Int("num_continuous_parallel", 3, "The number of parallel copies of continuous clustering to run.")
	numShift                       = flag.Int("num_shift", 10, "The number of commits the shift navigation buttons should jump.")
	numTilesRefresher              = flag.Int("num_tiles_refresher", 2, "The number of tiles to load in the dataframe.Refresher.")
	port                           = flag.String("port", ":8000", "HTTP service address (e.g., ':8000')")
	projectName                    = flag.String("project_name", "google.com:skia-buildbots", "The Google Cloud project name.")
	promPort                       = flag.String("prom_port", ":20000", "Metrics service address (e.g., ':10110')")
	internalPort                   = flag.String("internal_port", ":9000", "HTTP service address for internal clients, e.g. probers. No authentication on this port.")
	radius                         = flag.Int("radius", 7, "The number of commits to include on either side of a commit when clustering.")
	resourcesDir                   = flag.String("resources_dir", "", "The directory to find templates, JS, and CSS files. If blank the current directory will be used.")
	stepUpOnly                     = flag.Bool("step_up_only", false, "Only regressions that look like a step up will be reported.")
	subdomain                      = flag.String("subdomain", "perf", "The public subdomain of the server, i.e. 'perf' for perf.skia.org.")
	tracing                        = flag.Bool("tracing", false, "If true then send traces to stackdriver.")
)

var (
	templates *template.Template

	frameRequests *dataframe.RunningFrameRequests

	clusterRequests *regression.RunningClusterRequests

	regStore *regression.Store

	continuous []*regression.Continuous

	storageClient *storage.Client

	alertStore *alerts.Store

	configProvider regression.ConfigProvider

	notifier *notify.Notifier

	traceStore *btts.BigTableTraceStore

	emailAuth *email.GMail

	btConfig *config.PerfBigTableConfig

	dryrunRequests *dryrun.Requests

	paramsetRefresher *psrefresh.ParamSetRefresher

	dfBuilder dataframe.DataFrameBuilder
)

func loadTemplates() {
	templates = template.Must(template.New("").ParseFiles(
		filepath.Join(*resourcesDir, "dist/newindex.html"),
		filepath.Join(*resourcesDir, "dist/clusters2.html"),
		filepath.Join(*resourcesDir, "dist/triage.html"),
		filepath.Join(*resourcesDir, "dist/alerts.html"),
		filepath.Join(*resourcesDir, "dist/offline.html"),
		filepath.Join(*resourcesDir, "dist/help.html"),
		filepath.Join(*resourcesDir, "dist/activitylog.html"),
		filepath.Join(*resourcesDir, "dist/dryRunAlert.html"),
		filepath.Join(*resourcesDir, "dist/service-worker-bundle.js"),
	))
}

// SkPerfConfig is the configuration data that will appear
// in Javascript under the sk.perf variable.
type SkPerfConfig struct {
	Radius         int      `json:"radius"`           // The number of commits when doing clustering.
	KeyOrder       []string `json:"key_order"`        // The order of the keys to appear first in query-sk elements.
	NumShift       int      `json:"num_shift"`        // The number of commits the shift navigation buttons should jump.
	Interesting    float32  `json:"interesting"`      // The threshold for a cluster to be interesting.
	StepUpOnly     bool     `json:"step_up_only"`     // If true then only regressions that are a step up are displayed.
	CommitRangeURL string   `json:"commit_range_url"` // A URI Template to be used for expanding details on a range of commits. See cluster-summary2-sk.
}

func templateHandler(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if *local {
			loadTemplates()
		}
		context := SkPerfConfig{
			Radius:         *radius,
			KeyOrder:       strings.Split(*keyOrder, ","),
			NumShift:       *numShift,
			Interesting:    float32(*interesting),
			StepUpOnly:     *stepUpOnly,
			CommitRangeURL: *commitRangeURL,
		}
		b, err := json.MarshalIndent(context, "", "  ")
		if err != nil {
			sklog.Errorf("Failed to JSON encode sk.perf context: %s", err)
		}
		if err := templates.ExecuteTemplate(w, name, map[string]template.JS{"context": template.JS(string(b))}); err != nil {
			sklog.Error("Failed to expand template:", err)
		}
	}
}

// scriptHandler serves up a template as a script.
func scriptHandler(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		if *local {
			loadTemplates()
		}
		if err := templates.ExecuteTemplate(w, name, nil); err != nil {
			sklog.Error("Failed to expand template:", err)
		}
	}
}

// newParamsetProvider returns a regression.ParamsetProvider which produces a paramset
// for the current tiles.
//
func newParamsetProvider(pf *psrefresh.ParamSetRefresher) regression.ParamsetProvider {
	return func() paramtools.ParamSet {
		return pf.Get()
	}
}

// newAlertsConfigProvider returns a regression.ConfigProvider which produces a slice
// of alerts.Config to run continuous clustering against.
func newAlertsConfigProvider(clusterAlgo types.ClusterAlgo) regression.ConfigProvider {
	return func() ([]*alerts.Config, error) {
		return alertStore.List(false)
	}
}

func Init() {
	rand.Seed(time.Now().UnixNano())

	sampler := trace.NeverSample()
	if *tracing {
		sampler = trace.AlwaysSample()
	}
	exporter, err := stackdriver.NewExporter(stackdriver.Options{
		BundleDelayThreshold: time.Second / 10,
		BundleCountThreshold: 10})
	if err != nil {
		sklog.Fatal(err)
	}
	trace.RegisterExporter(exporter)
	trace.ApplyConfig(trace.Config{DefaultSampler: sampler})
	_, span := trace.StartSpan(context.Background(), "main")
	defer span.End()

	ctx := context.Background()
	if *resourcesDir == "" {
		_, filename, _, _ := runtime.Caller(0)
		*resourcesDir = filepath.Join(filepath.Dir(filename), "../..")
	}

	if *namespace == "" {
		sklog.Fatal("The --namespace flag is required. See infra/DATASTORE.md for format details.\n")
	}
	if !*local && !util.In(*namespace, []string{ds.PERF_NS, ds.PERF_ANDROID_NS, ds.PERF_ANDROID_X_NS, ds.PERF_ANDROID_MASTER_NS, ds.PERF_CT_NS}) {
		sklog.Fatal("When running in prod the datastore namespace must be a known value.")
	}

	scopes := []string{storage.ScopeReadOnly, datastore.ScopeDatastore, bigtable.Scope}

	sklog.Info("About to create token source.")
	ts, err := auth.NewDefaultTokenSource(*local, scopes...)
	if err != nil {
		sklog.Fatalf("Failed to get TokenSource: %s", err)
	}

	sklog.Info("About to init datastore.")
	if err := ds.InitWithOpt(*projectName, *namespace, option.WithTokenSource(ts)); err != nil {
		sklog.Fatalf("Failed to init Cloud Datastore: %s", err)
	}

	sklog.Info("About to init GCS.")
	storageClient, err = storage.NewClient(ctx, option.WithTokenSource(ts))
	if err != nil {
		sklog.Fatalf("Failed to authenicate to cloud storage: %s", err)
	}

	clusterAlgo, err := types.ToClusterAlgo(*algo)
	if err != nil {
		sklog.Fatalf("The --algo flag value is invalid: %s", err)
	}

	sklog.Info("About to parse templates.")
	loadTemplates()

	var ok bool
	if btConfig, ok = config.PERF_BIGTABLE_CONFIGS[*bigTableConfig]; !ok {
		sklog.Fatalf("Not a valid BigTable config: %q", *bigTableConfig)
	}

	sklog.Info("About to clone repo.")

	/*
		gitstoreConfig := &gitstore.BTConfig{
			ProjectID:  btConfig.Project,
			InstanceID: btConfig.Instance,
			TableID:    "git-repos2",
			AppProfile: "skiaperf",
		}

		gs, err := gitstore.NewBTGitStore(ctx, gitstoreConfig, btConfig.GitUrl)
		if err != nil {
			sklog.Fatal(err)
		}
		vcs, err := gitstore.NewVCS(gs, "master", nil)
		if err != nil {
			sklog.Fatal(err)
		}
	*/
	vcs, err = gitinfo.CloneOrUpdate(ctx, btConfig.GitUrl, *gitRepoDir, false)
	if err != nil {
		sklog.Fatal(err)
	}

	// TODO(jcgregorio) Remove this once we move to gitsync.
	// Keep the repo synced.
	go func() {
		for range time.Tick(time.Minute) {
			if err := vcs.Update(context.Background(), true, false); err != nil {
				sklog.Errorf("Failed to update repo: %s", err)
			}
		}
	}()

	sklog.Info("About to build dataframebuilder.")

	traceStore, err = btts.NewBigTableTraceStoreFromConfig(ctx, btConfig, ts, false)
	if err != nil {
		sklog.Fatalf("Failed to open trace store: %s", err)
	}

	paramsetRefresher = psrefresh.NewParamSetRefresher(traceStore)
	if err := paramsetRefresher.Start(PARAMSET_REFRESHER_PERIOD); err != nil {
		sklog.Fatalf("Failed to build paramsetRefresher: %s", err)
	}

	dfBuilder = dfbuilder.NewDataFrameBuilderFromBTTS(vcs, traceStore)

	sklog.Info("About to build cidl.")
	cidl = cid.New(ctx, vcs, btConfig.GitUrl)

	alerts.DefaultSparse = *defaultSparse

	sklog.Info("About to build alertStore.")
	alertStore = alerts.NewStore()

	if !*noemail {
		emailAuth, err = email.NewFromFiles(*emailTokenCacheFile, *emailClientSecretFile)
		if err != nil {
			sklog.Fatalf("Failed to create email auth: %v", err)
		}
		notifier = notify.New(emailAuth, *subdomain)
	} else {
		notifier = notify.New(notify.NoEmail{}, *subdomain)
	}

	frameRequests = dataframe.NewRunningFrameRequests(vcs, dfBuilder)
	clusterRequests = regression.NewRunningClusterRequests(vcs, cidl, float32(*interesting), dfBuilder)
	regStore = regression.NewStore()
	configProvider = newAlertsConfigProvider(clusterAlgo)
	paramsProvider := newParamsetProvider(paramsetRefresher)

	dryrunRequests = dryrun.New(cidl, dfBuilder, paramsProvider, vcs)

	if *doClustering {
		go func() {
			for i := 0; i < *numContinuousParallel; i++ {
				// Start running continuous clustering looking for regressions.
				time.Sleep(START_CLUSTER_DELAY)
				c := regression.NewContinuous(vcs, cidl, configProvider, regStore, *numContinuous, *radius, notifier, paramsProvider, dfBuilder,
					*local, btConfig.Project, btConfig.FileIngestionTopicName, *eventDrivenRegressionDetection)
				continuous = append(continuous, c)
				go c.Run(context.Background())
			}
		}()
	}
}

// activityHandler serves the HTML for the /activitylog/ page.
//
// If an optional number n is appended to the path, returns the most recent n
// activities. Otherwise returns the most recent 100 results.
//
func activityHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	if *local {
		loadTemplates()
	}

	match := activityHandlerPath.FindStringSubmatch(r.URL.Path)
	if r.Method != "GET" || match == nil || len(match) != 2 {
		http.NotFound(w, r)
		return
	}
	n := 100
	if len(match[1]) > 0 {
		num, err := strconv.ParseInt(match[1], 10, 0)
		if err != nil {
			httputils.ReportError(w, err, "Failed parsing the given number.", http.StatusInternalServerError)
			return
		}
		n = int(num)
	}
	a, err := activitylog.GetRecent(n)
	if err != nil {
		httputils.ReportError(w, err, "Failed to retrieve activity.", http.StatusInternalServerError)
		return
	}
	if err := templates.ExecuteTemplate(w, "activitylog.html", a); err != nil {
		sklog.Error("Failed to expand template:", err)
	}
}

// helpHandler handles the GET of the main page.
func helpHandler(w http.ResponseWriter, r *http.Request) {
	sklog.Infof("Help Handler: %q\n", r.URL.Path)
	if *local {
		loadTemplates()
	}
	if r.Method == "GET" {
		w.Header().Set("Content-Type", "text/html")
		ctx := calc.NewContext(nil, nil)
		if err := templates.ExecuteTemplate(w, "help.html", ctx); err != nil {
			sklog.Error("Failed to expand template:", err)
		}
	}
}

// offlineHandler handles the GET of the offline page.
func offlineHandler(w http.ResponseWriter, r *http.Request) {
	sklog.Infof("Help Handler: %q\n", r.URL.Path)
	if *local {
		loadTemplates()
	}
	if r.Method == "GET" {
		w.Header().Set("Content-Type", "text/html")
		if err := templates.ExecuteTemplate(w, "offline.html", nil); err != nil {
			sklog.Error("Failed to expand template:", err)
		}
	}
}

type AlertsStatus struct {
	Alerts int `json:"alerts"`
}

func alertsHandler(w http.ResponseWriter, r *http.Request) {
	count, err := regressionCount(DEFAULT_ALERT_CATEGORY)
	if err != nil {
		httputils.ReportError(w, err, "Failed to load untriaged count.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("Access-Control-Allow-Origin", "*")
	resp := AlertsStatus{
		Alerts: count,
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		sklog.Errorf("Failed to encode paramset: %s", err)
	}
}

func initpageHandler(w http.ResponseWriter, r *http.Request) {
	resp := &dataframe.FrameResponse{
		DataFrame: &dataframe.DataFrame{
			ParamSet: paramsetRefresher.Get(),
		},
		Ticks: []interface{}{},
		Skps:  []int{},
		Msg:   "",
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		sklog.Errorf("Failed to encode paramset: %s", err)
	}
}

// RangeRequest is used in cidRangeHandler and is used to query for a range of
// cid.CommitIDs that include the range between [begin, end) and include the
// explicit CommitID of "Source, Offset".
type RangeRequest struct {
	Source string `json:"source"`
	Offset int    `json:"offset"`
	Begin  int64  `json:"begin"`
	End    int64  `json:"end"`
}

// cidRangeHandler accepts a POST'd JSON serialized RangeRequest
// and returns a serialized JSON slice of cid.CommitDetails.
func cidRangeHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var rr RangeRequest
	if err := json.NewDecoder(r.Body).Decode(&rr); err != nil {
		httputils.ReportError(w, err, "Failed to decode JSON.", http.StatusInternalServerError)
		return
	}

	now := time.Now()
	begin := now.Add(-24 * time.Hour).Unix()
	end := now.Unix()
	var err error
	if rr.Begin != 0 || rr.End != 0 {
		if rr.Begin != 0 {
			begin = rr.Begin
		}
		if rr.End != 0 {
			end = rr.End
		}
	}
	df := dataframe.NewHeaderOnly(vcs, time.Unix(begin, 0), time.Unix(end, 0), false)

	found := false
	cids := []*cid.CommitID{}
	for _, h := range df.Header {
		cids = append(cids, &cid.CommitID{
			Offset: int(h.Offset),
			Source: h.Source,
		})
		if int(h.Offset) == rr.Offset && h.Source == rr.Source {
			found = true
		}
	}
	if !found && rr.Source != "" && rr.Offset != -1 {
		cids = append(cids, &cid.CommitID{
			Offset: rr.Offset,
			Source: rr.Source,
		})
	}

	resp, err := cidl.Lookup(context.Background(), cids)
	if err != nil {
		httputils.ReportError(w, err, "Failed to lookup all commit ids", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		sklog.Errorf("Failed to encode paramset: %s", err)
	}
}

// FrameStartResponse is serialized as JSON for the response in
// frameStartHandler.
type FrameStartResponse struct {
	ID string `json:"id"`
}

// frameStartHandler starts a FrameRequest running and returns the ID
// of the Go routine doing the work.
//
// Building a DataFrame can take a long time to complete, so we run the request
// in a Go routine and break the building of DataFrames into three separate
// requests:
//  * Start building the DataFrame (_/frame/start), which returns an identifier of the long
//    running request, {id}.
//  * Query the status of the running request (_/frame/status/{id}).
//  * Finally return the constructed DataFrame (_/frame/results/{id}).
func frameStartHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fr := &dataframe.FrameRequest{}
	if err := json.NewDecoder(r.Body).Decode(fr); err != nil {
		httputils.ReportError(w, err, "Failed to decode JSON.", http.StatusInternalServerError)
		return
	}
	// Remove all empty queries.
	q := []string{}
	for _, s := range fr.Queries {
		if strings.TrimSpace(s) != "" {
			q = append(q, s)
		}
	}
	fr.Queries = q

	if len(fr.Formulas) == 0 && len(fr.Queries) == 0 && fr.Keys == "" {
		httputils.ReportError(w, fmt.Errorf("Invalid query."), "Empty queries are not allowed.", http.StatusInternalServerError)
		return
	}

	ctx, span := trace.StartSpan(context.Background(), "frameStartRequest")
	defer span.End()
	resp := FrameStartResponse{
		ID: frameRequests.Add(ctx, fr),
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		sklog.Errorf("Failed to encode paramset: %s", err)
	}
}

// FrameStatus is used to serialize a JSON response in frameStatusHandler.
type FrameStatus struct {
	State   dataframe.ProcessState `json:"state"`
	Message string                 `json:"message"`
	Percent float32                `json:"percent"`
}

// frameStatusHandler returns the status of a pending FrameRequest.
//
// See frameStartHandler for more details.
func frameStatusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	id := mux.Vars(r)["id"]
	state, message, percent, err := frameRequests.Status(id)
	if err != nil {
		httputils.ReportError(w, err, message, http.StatusInternalServerError)
		return
	}

	resp := FrameStatus{
		State:   state,
		Message: message,
		Percent: percent,
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		sklog.Errorf("Failed to encode response: %s %#v", err, resp)
	}
}

// frameResultsHandler returns the results of a pending FrameRequest.
//
// See frameStatusHandler for more details.
func frameResultsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	id := mux.Vars(r)["id"]
	df, err := frameRequests.Response(id)
	if err != nil {
		httputils.ReportError(w, err, "Async processing of frame failed.", http.StatusInternalServerError)
		return
	}

	if err := json.NewEncoder(w).Encode(df); err != nil {
		sklog.Errorf("Failed to encode response: %s", err)
	}
}

type CountRequest struct {
	Q     string `json:"q"`
	Begin int    `json:"begin"`
	End   int    `json:"end"`
}

type countHandlerResponse struct {
	Count    int                 `json:"count"`
	Paramset paramtools.ParamSet `json:"paramset"`
}

// countHandler takes the POST'd query and runs that against the current
// dataframe and returns how many traces match the query.
func countHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var cr CountRequest
	if err := json.NewDecoder(r.Body).Decode(&cr); err != nil {
		httputils.ReportError(w, err, "Failed to decode JSON.", http.StatusInternalServerError)
		return
	}

	u, err := url.ParseQuery(cr.Q)
	if err != nil {
		httputils.ReportError(w, err, "Invalid URL query.", http.StatusInternalServerError)
		return
	}
	q, err := query.New(u)
	if err != nil {
		httputils.ReportError(w, err, "Invalid query.", http.StatusInternalServerError)
		return
	}
	resp := countHandlerResponse{}
	if cr.Q == "" {
		ps := paramsetRefresher.Get()
		resp.Count = 0
		resp.Paramset = ps
	} else {
		count, ps, err := dfBuilder.PreflightQuery(r.Context(), time.Now(), q)
		if err != nil {
			httputils.ReportError(w, err, "Failed to Preflight the query.", http.StatusInternalServerError)
		}

		resp.Count = int(count)
		resp.Paramset = ps
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		sklog.Errorf("Failed to encode paramset: %s", err)
	}
}

// cidHandler takes the POST'd list of dataframe.ColumnHeaders,
// and returns a serialized slice of vcsinfo.ShortCommit's.
func cidHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	cids := []*cid.CommitID{}
	if err := json.NewDecoder(r.Body).Decode(&cids); err != nil {
		httputils.ReportError(w, err, "Could not decode POST body.", http.StatusInternalServerError)
		return
	}
	resp, err := cidl.Lookup(context.Background(), cids)
	if err != nil {
		httputils.ReportError(w, err, "Failed to lookup all commit ids", http.StatusInternalServerError)
		return
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		sklog.Errorf("Failed to encode paramset: %s", err)
	}
}

// ClusterStartResponse is serialized as JSON for the response in
// clusterStartHandler.
type ClusterStartResponse struct {
	ID string `json:"id"`
}

// clusterStartHandler takes a POST'd ClusterRequest and starts a long
// running Go routine to do the actual clustering. The ID of the long
// running routine is returned to be used in subsequent calls to
// clusterStatusHandler to check on the status of the work.
func clusterStartHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	req := &regression.ClusterRequest{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputils.ReportError(w, err, "Could not decode POST body.", http.StatusInternalServerError)
		return
	}
	id, err := clusterRequests.Add(context.Background(), req)
	if err != nil {
		httputils.ReportError(w, err, "Cluster request was invalid", http.StatusInternalServerError)
		return
	}
	resp := ClusterStartResponse{
		ID: id,
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		sklog.Errorf("Failed to encode paramset: %s", err)
	}
}

// ClusterStatus is used to serialize a JSON response in clusterStatusHandler.
type ClusterStatus struct {
	State   regression.ProcessState       `json:"state"`
	Message string                        `json:"message"`
	Value   *regression.ClusterResponse   `json:"value"`
	Values  []*regression.ClusterResponse `json:"values"`
}

// clusterStatusHandler is used to check on the status of a long
// running cluster request. The ID of the routine is passed in via
// the URL path. A JSON serialized ClusterStatus is returned, with
// ClusterStatus.Value being populated only when the clustering
// process has finished.
func clusterStatusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	id := mux.Vars(r)["id"]

	status := &ClusterStatus{}
	state, msg, err := clusterRequests.Status(id)
	if err != nil {
		httputils.ReportError(w, err, msg, http.StatusInternalServerError)
		return
	}
	status.State = state
	status.Message = msg
	if state == regression.PROCESS_SUCCESS {
		value, err := clusterRequests.Response(id)
		if err != nil {
			httputils.ReportError(w, err, "Failed to retrieve results.", http.StatusInternalServerError)
			return
		}
		status.Value = value
	}

	if err := json.NewEncoder(w).Encode(status); err != nil {
		sklog.Errorf("Failed to encode paramset: %s", err)
	}
}

// clusterStatusHandler2 is used to check on the status of a long
// running cluster request. The ID of the routine is passed in via
// the URL path. A JSON serialized ClusterStatus is returned, with
// ClusterStatus.Value being populated with all the clusters found so far.
func clusterStatusHandler2(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	id := mux.Vars(r)["id"]

	status := &ClusterStatus{}
	state, msg, err := clusterRequests.Status(id)
	if err != nil {
		httputils.ReportError(w, err, msg, http.StatusInternalServerError)
		return
	}
	status.State = state
	status.Message = msg
	values, err := clusterRequests.Responses(id)
	if err != nil {
		httputils.ReportError(w, err, "Failed to retrieve results.", http.StatusInternalServerError)
		return
	}
	status.Values = values

	if err := json.NewEncoder(w).Encode(status); err != nil {
		sklog.Errorf("Failed to encode paramset: %s", err)
	}
}

// keysHandler handles the POST requests of a list of keys.
//
//    {
//       "keys": [
//            ",arch=x86,...",
//            ",arch=x86,...",
//       ]
//    }
//
// And returns the ID of the new shortcut to that list of keys:
//
//   {
//     "id": 123456,
//   }
func keysHandler(w http.ResponseWriter, r *http.Request) {
	id, err := shortcut2.Insert(r.Body)
	if err != nil {
		httputils.ReportError(w, err, "Error inserting shortcut.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"id": id}); err != nil {
		sklog.Errorf("Failed to write or encode output: %s", err)
	}
}

// gotoHandler handles redirecting from a git hash to either the explore,
// clustering, or triage page.
//
// Sets begin and end to a range of commits on either side of the selected
// commit.
//
// Preserves query parameters that are passed into /g/ and passes them onto the
// target URL.
func gotoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		httputils.ReportError(w, err, "Could not parse query parameters.", http.StatusInternalServerError)
		return
	}
	ctx := context.Background()
	query := r.Form
	hash := mux.Vars(r)["hash"]
	dest := mux.Vars(r)["dest"]
	index, err := vcs.IndexOf(ctx, hash)
	if err != nil {
		httputils.ReportError(w, err, "Could not look up git hash.", http.StatusInternalServerError)
		return
	}
	last := vcs.LastNIndex(1)
	if len(last) != 1 {
		httputils.ReportError(w, fmt.Errorf("VCS.LastN(1) returned 0 hashes."), "Failed to find last hash.", http.StatusInternalServerError)
		return
	}
	lastIndex := last[0].Index

	delta := config.GOTO_RANGE
	// If redirecting to the Triage page then always show just a single commit.
	if dest == "t" {
		delta = 0
	}
	begin := index - delta
	if begin < 0 {
		begin = 0
	}
	end := index + delta
	if end > lastIndex {
		end = lastIndex
	}
	details, err := cidl.Lookup(ctx, []*cid.CommitID{
		{
			Offset: begin,
			Source: "master",
		},
		{
			Offset: end,
			Source: "master",
		},
	})
	if err != nil {
		httputils.ReportError(w, err, "Could not convert indices to hashes.", http.StatusInternalServerError)
		return
	}
	beginTime := details[0].Timestamp
	endTime := details[1].Timestamp + 1
	query.Set("begin", fmt.Sprintf("%d", beginTime))
	query.Set("end", fmt.Sprintf("%d", endTime))

	if dest == "e" {
		http.Redirect(w, r, fmt.Sprintf("/e/?%s", query.Encode()), http.StatusFound)
	} else if dest == "c" {
		query.Set("offset", fmt.Sprintf("%d", index))
		query.Set("source", "master")
		http.Redirect(w, r, fmt.Sprintf("/c/?%s", query.Encode()), http.StatusFound)
	} else if dest == "t" {
		query.Set("subset", "all")
		http.Redirect(w, r, fmt.Sprintf("/t/?%s", query.Encode()), http.StatusFound)
	}
}

// TriageRequest is used in triageHandler.
type TriageRequest struct {
	Cid         *cid.CommitID           `json:"cid"`
	Alert       alerts.Config           `json:"alert"`
	Triage      regression.TriageStatus `json:"triage"`
	ClusterType string                  `json:"cluster_type"`
}

// TriageResponse is used in triageHandler.
type TriageResponse struct {
	Bug string `json:"bug"` // URL to bug reporting page.
}

// triageHandler takes a POST'd TriageRequest serialized as JSON
// and performs the triage.
//
// If succesful it returns a 200, or an HTTP status code of 500 otherwise.
func triageHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if login.LoggedInAs(r) == "" {
		httputils.ReportError(w, fmt.Errorf("Not logged in."), "You must be logged in to triage.", http.StatusInternalServerError)
		return
	}
	tr := &TriageRequest{}
	if err := json.NewDecoder(r.Body).Decode(tr); err != nil {
		httputils.ReportError(w, err, "Failed to decode JSON.", http.StatusInternalServerError)
		return
	}
	detail, err := cidl.Lookup(context.Background(), []*cid.CommitID{tr.Cid})
	if err != nil {
		httputils.ReportError(w, err, "Failed to find CommitID.", http.StatusInternalServerError)
		return
	}

	key := tr.Alert.IdAsString()
	if tr.ClusterType == "low" {
		err = regStore.TriageLow(detail[0], key, tr.Triage)
	} else {
		err = regStore.TriageHigh(detail[0], key, tr.Triage)
	}

	if err != nil {
		httputils.ReportError(w, err, "Failed to triage.", http.StatusInternalServerError)
		return
	}

	link := fmt.Sprintf("%s/t/?begin=%d&end=%d&subset=all", r.Header.Get("Origin"), detail[0].Timestamp, detail[0].Timestamp+1)
	a := &activitylog.Activity{
		UserID: login.LoggedInAs(r),
		Action: fmt.Sprintf("Perf Triage: %q %q %q %q", tr.Alert.Query, detail[0].URL, tr.Triage.Status, tr.Triage.Message),
		URL:    link,
	}
	if err := activitylog.Write(a); err != nil {
		sklog.Errorf("Failed to log activity: %s", err)
	}

	resp := &TriageResponse{}

	if tr.Triage.Status == regression.NEGATIVE {
		cfgs, err := configProvider()
		if err != nil {
			sklog.Errorf("Failed to load configs looking for BugURITemplate: %s", err)
		}
		uritemplate := DEFAULT_BUG_URI_TEMPLATE
		for _, c := range cfgs {
			if c.ID == tr.Alert.ID {
				uritemplate = c.BugURITemplate
				break
			}
		}
		resp.Bug = bug.Expand(uritemplate, link, detail[0], tr.Triage.Message)
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		sklog.Errorf("Failed to write or encode output: %s", err)
	}
}

// regressionCount returns the number of commits that have regressions for alerts
// in the given category. The time range of commits is REGRESSION_COUNT_DURATION.
func regressionCount(category string) (int, error) {
	configs, err := configProvider()
	if err != nil {
		return 0, err
	}

	// Query for Regressions in the range.
	end := time.Now()
	begin := end.Add(REGRESSION_COUNT_DURATION)
	regMap, err := regStore.Range(begin.Unix(), end.Unix())
	if err != nil {
		return 0, err
	}
	count := 0
	for _, regs := range regMap {
		for _, cfg := range configs {
			if reg, ok := regs.ByAlertID[cfg.IdAsString()]; ok {
				if cfg.Category == category && !reg.Triaged() {
					// If any alert for the commit is in the category and is untriaged then we count that row only once.
					count += 1
					break
				}
			}
		}
	}
	return count, nil
}

// regressionCountHandler returns a JSON object with the number of untriaged
// alerts that appear in the REGRESSION_COUNT_DURATION. The category
// can be supplied by the 'cat' query parameter and defaults to "".
func regressionCountHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	category := r.FormValue("cat")

	count, err := regressionCount(category)
	if err != nil {
		httputils.ReportError(w, err, "Failed to count regressions.", http.StatusInternalServerError)
	}

	if err := json.NewEncoder(w).Encode(struct{ Count int }{Count: count}); err != nil {
		sklog.Errorf("Failed to write or encode output: %s", err)
	}
}

// RegressionRangeRequest is used in regressionRangeHandler and is used to query for a range of
// of Regressions.
//
// Begin and End are Unix timestamps in seconds.
type RegressionRangeRequest struct {
	Begin       int64             `json:"begin"`
	End         int64             `json:"end"`
	Subset      regression.Subset `json:"subset"`
	AlertFilter string            `json:"alert_filter"` // Can be an alertfilter constant, or a category prefixed with "cat:".
}

// RegressionRow are all the Regression's for a specific commit. It is used in
// RegressionRangeResponse.
//
// The Columns have the same order as RegressionRangeResponse.Header.
type RegressionRow struct {
	Id      *cid.CommitDetail        `json:"cid"`
	Columns []*regression.Regression `json:"columns"`
}

// RegressionRangeResponse is the response from regressionRangeHandler.
type RegressionRangeResponse struct {
	Header     []*alerts.Config `json:"header"`
	Table      []*RegressionRow `json:"table"`
	Categories []string         `json:"categories"`
}

// regressionRangeHandler accepts a POST'd JSON serialized RegressionRangeRequest
// and returns a serialized JSON RegressionRangeResponse:
//
//    {
//      header: [ "query1", "query2", "query3", ...],
//      table: [
//        { cid: cid1, columns: [ Regression, Regression, Regression, ...], },
//        { cid: cid2, columns: [ Regression, null,       Regression, ...], },
//        { cid: cid3, columns: [ Regression, Regression, Regression, ...], },
//      ]
//    }
//
// Note that there will be nulls in the columns slice where no Regression have been found.
func regressionRangeHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ctx := context.Background()
	rr := &RegressionRangeRequest{}
	if err := json.NewDecoder(r.Body).Decode(rr); err != nil {
		httputils.ReportError(w, err, "Failed to decode JSON.", http.StatusInternalServerError)
		return
	}

	// Query for Regressions in the range.
	regMap, err := regStore.Range(rr.Begin, rr.End)
	if err != nil {
		httputils.ReportError(w, err, "Failed to retrieve clusters.", http.StatusInternalServerError)
		return
	}

	headers, err := configProvider()
	if err != nil {
		httputils.ReportError(w, err, "Failed to retrieve alert configs.", http.StatusInternalServerError)
		return
	}

	// Build the full list of categories.
	categorySet := util.StringSet{}
	for _, header := range headers {
		categorySet[header.Category] = true
	}

	// Filter down the alerts according to rr.AlertFilter.
	if rr.AlertFilter == alertfilter.OWNER {
		user := login.LoggedInAs(r)
		filteredHeaders := []*alerts.Config{}
		for _, a := range headers {
			if a.Owner == user {
				filteredHeaders = append(filteredHeaders, a)
			}
		}
		if len(filteredHeaders) > 0 {
			headers = filteredHeaders
		} else {
			sklog.Infof("User doesn't own any alerts.")
		}
	} else if strings.HasPrefix(rr.AlertFilter, "cat:") {
		selectedCategory := rr.AlertFilter[4:]
		filteredHeaders := []*alerts.Config{}
		for _, a := range headers {
			if a.Category == selectedCategory {
				filteredHeaders = append(filteredHeaders, a)
			}
		}
		if len(filteredHeaders) > 0 {
			headers = filteredHeaders
		} else {
			sklog.Infof("No alert in that category: %q", selectedCategory)
		}
	}

	// Get a list of commits for the range.
	var ids []*cid.CommitID
	if rr.Subset == regression.ALL_SUBSET {
		indexCommits := vcs.Range(time.Unix(rr.Begin, 0), time.Unix(rr.End, 0))
		ids = make([]*cid.CommitID, 0, len(indexCommits))
		for _, indexCommit := range indexCommits {
			ids = append(ids, &cid.CommitID{
				Source: "master",
				Offset: indexCommit.Index,
			})
		}
	} else {
		// If rr.Subset == UNTRIAGED_QS or FLAGGED_QS then only get the commits that
		// exactly line up with the regressions in regMap.
		ids = make([]*cid.CommitID, 0, len(regMap))
		keys := []string{}
		for k := range regMap {
			keys = append(keys, k)
		}
		sort.Sort(sort.StringSlice(keys))
		for _, key := range keys {
			c, err := cid.FromID(key)
			if err != nil {
				httputils.ReportError(w, err, "Got an invalid commit id.", http.StatusInternalServerError)
				return
			}
			ids = append(ids, c)
		}
	}

	// Convert the CommitIDs to CommitDetails.
	cids, err := cidl.Lookup(ctx, ids)
	if err != nil {
		httputils.ReportError(w, err, "Failed to look up commit details", http.StatusInternalServerError)
		return
	}

	// Reverse the order of the cids, so the latest
	// commit shows up first in the UI display.
	revCids := make([]*cid.CommitDetail, len(cids), len(cids))
	for i, c := range cids {
		revCids[len(cids)-1-i] = c
	}

	categories := categorySet.Keys()
	sort.Strings(categories)

	// Build the RegressionRangeResponse.
	ret := RegressionRangeResponse{
		Header:     headers,
		Table:      []*RegressionRow{},
		Categories: categories,
	}

	for _, cid := range revCids {
		row := &RegressionRow{
			Id:      cid,
			Columns: make([]*regression.Regression, len(headers), len(headers)),
		}
		count := 0
		if r, ok := regMap[cid.ID()]; ok {
			for i, h := range headers {
				key := h.IdAsString()
				if reg, ok := r.ByAlertID[key]; ok {
					if rr.Subset == regression.UNTRIAGED_SUBSET && reg.Triaged() {
						continue
					}
					row.Columns[i] = reg
					count += 1
				}
			}
		}
		if count == 0 && rr.Subset != regression.ALL_SUBSET {
			continue
		}
		ret.Table = append(ret.Table, row)
	}
	if err := json.NewEncoder(w).Encode(ret); err != nil {
		sklog.Errorf("Failed to write or encode output: %s", err)
	}
}

func regressionCurrentHandler(w http.ResponseWriter, r *http.Request) {
	status := []regression.Current{}
	for _, c := range continuous {
		status = append(status, c.CurrentStatus())
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		sklog.Errorf("Failed to encode status: %s", err)
	}
}

// DetailsRequest is for deserializing incoming POST requests
// in detailsHandler.
type DetailsRequest struct {
	CID     cid.CommitID `json:"cid"`
	TraceID string       `json:"traceid"`
}

func detailsHandler(w http.ResponseWriter, r *http.Request) {
	includeResults := r.FormValue("results") != "false"
	w.Header().Set("Content-Type", "application/json")
	dr := &DetailsRequest{}
	if err := json.NewDecoder(r.Body).Decode(dr); err != nil {
		httputils.ReportError(w, err, "Failed to decode JSON.", http.StatusInternalServerError)
		return
	}

	var err error
	name := ""
	index := int32(dr.CID.Offset)
	name, err = traceStore.GetSource(r.Context(), index, dr.TraceID)
	if err != nil {
		httputils.ReportError(w, err, "Failed to load details", http.StatusInternalServerError)
		return
	}

	sklog.Infof("Full URL to source: %q", name)
	u, err := url.Parse(name)
	if err != nil {
		httputils.ReportError(w, err, "Failed to parse source file location.", http.StatusInternalServerError)
		return
	}
	if u.Host == "" || u.Path == "" {
		httputils.ReportError(w, fmt.Errorf("Invalid source location: %q", name), "Invalid source location.", http.StatusInternalServerError)
		return
	}
	sklog.Infof("Host: %q Path: %q", u.Host, u.Path)
	reader, err := storageClient.Bucket(u.Host).Object(u.Path[1:]).NewReader(context.Background())
	if err != nil {
		httputils.ReportError(w, err, "Failed to get reader for source file location", http.StatusInternalServerError)
		return
	}
	defer util.Close(reader)
	res := map[string]interface{}{}
	if err := json.NewDecoder(reader).Decode(&res); err != nil {
		httputils.ReportError(w, err, "Failed to decode JSON source file", http.StatusInternalServerError)
		return
	}
	if !includeResults {
		delete(res, "results")
	}
	if b, err := json.MarshalIndent(res, "", "  "); err != nil {
		httputils.ReportError(w, err, "Failed to re-encode JSON source file", http.StatusInternalServerError)
		return
	} else {
		if _, err := w.Write(b); err != nil {
			sklog.Errorf("Failed to write JSON source file: %s", err)
		}
	}
}

type ShiftRequest struct {
	// Begin is the timestamp of the beginning of a range of commits.
	Begin int64 `json:"begin"`
	// BeginOffset is the number of commits to move (+ or -) the Begin timestamp.
	BeginOffset int `json:"begin_offset"`

	// End is the timestamp of the end of a range of commits.
	End int64 `json:"end"`
	// EndOffset is the number of commits to move (+ or -) the End timestamp.
	EndOffset int `json:"end_offset"`

	// See dataframe.FrameRequest.
	NumCommits int `json:"num_commits"`

	// See dataframe.FrameRequest.
	RequestType dataframe.RequestType `json:"request_type"`
}

type ShiftResponse struct {
	Begin      int64 `json:"begin"`
	End        int64 `json:"end"`
	NumCommits int   `json:"num_commits"`
}

// shiftHandler computes a new begin and end timestamp for a dataframe given
// the current begin and end timestamps and offsets, given in +/- the number of
// commits to move.
func shiftHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ctx := context.Background()
	var sr ShiftRequest
	if err := json.NewDecoder(r.Body).Decode(&sr); err != nil {
		httputils.ReportError(w, err, "Failed to decode JSON.", http.StatusInternalServerError)
		return
	}
	sklog.Infof("ShiftRequest: %#v", &sr)
	commits := vcs.Range(time.Unix(sr.Begin, 0), time.Unix(sr.End, 0))
	if len(commits) == 0 {
		httputils.ReportError(w, fmt.Errorf("No commits found in range."), "No commits found in range.", http.StatusInternalServerError)
		return
	}
	numCommits := sr.NumCommits
	if sr.RequestType == dataframe.REQUEST_COMPACT {
		numCommits -= sr.BeginOffset
	}
	beginCommit, err := vcs.ByIndex(ctx, commits[0].Index+sr.BeginOffset)
	if err != nil {
		httputils.ReportError(w, err, "Scrolled too far.", http.StatusInternalServerError)
		return
	}
	var endCommitTs time.Time
	endCommit, err := vcs.ByIndex(ctx, commits[len(commits)-1].Index+sr.EndOffset)
	if err != nil {
		// We went too far, so just use the last index.
		commits := vcs.LastNIndex(1)
		if len(commits) == 0 {
			httputils.ReportError(w, err, "Scrolled too far.", http.StatusInternalServerError)
			return
		}
		endCommitTs = commits[0].Timestamp
	} else {
		endCommitTs = endCommit.Timestamp
	}
	if beginCommit.Timestamp.Unix() == endCommitTs.Unix() {
		httputils.ReportError(w, err, "No commits found in range.", http.StatusInternalServerError)
		return
	}
	resp := ShiftResponse{
		Begin:      beginCommit.Timestamp.Unix(),
		End:        endCommitTs.Unix() + 1,
		NumCommits: numCommits,
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		sklog.Errorf("Failed to write JSON response: %s", err)
	}
}

func alertListHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	show := mux.Vars(r)["show"]
	resp, err := alertStore.List(show == "true")
	if err != nil {
		httputils.ReportError(w, err, "Failed to retrieve alert configs.", http.StatusInternalServerError)
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		sklog.Errorf("Failed to write JSON response: %s", err)
	}
}

func alertNewHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(alerts.NewConfig()); err != nil {
		sklog.Errorf("Failed to write JSON response: %s", err)
	}
}

func alertUpdateHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if login.LoggedInAs(r) == "" {
		httputils.ReportError(w, fmt.Errorf("Not logged in."), "You must be logged in to edit alerts.", http.StatusInternalServerError)
		return
	}

	cfg := &alerts.Config{}
	if err := json.NewDecoder(r.Body).Decode(cfg); err != nil {
		httputils.ReportError(w, err, "Failed to decode JSON.", http.StatusInternalServerError)
		return
	}
	if err := alertStore.Save(cfg); err != nil {
		httputils.ReportError(w, err, "Failed to save alerts.Config.", http.StatusInternalServerError)
	}
	link := ""
	if cfg.ID != alerts.INVALID_ID {
		link = fmt.Sprintf("/a/?%d", cfg.ID)
	}
	a := &activitylog.Activity{
		UserID: login.LoggedInAs(r),
		Action: fmt.Sprintf("Create/Update Alert: %#v, %d", *cfg, cfg.ID),
		URL:    link,
	}
	if err := activitylog.Write(a); err != nil {
		sklog.Errorf("Failed to log activity: %s", err)
	}
}

func alertDeleteHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if login.LoggedInAs(r) == "" {
		httputils.ReportError(w, fmt.Errorf("Not logged in."), "You must be logged in to delete alerts.", http.StatusInternalServerError)
		return
	}

	sid := mux.Vars(r)["id"]
	id, err := strconv.ParseInt(sid, 10, 64)
	if err != nil {
		httputils.ReportError(w, err, "Failed to parse alert id.", http.StatusInternalServerError)
	}
	if err := alertStore.Delete(int(id)); err != nil {
		httputils.ReportError(w, err, "Failed to delete the alerts.Config.", http.StatusInternalServerError)
		return
	}
	a := &activitylog.Activity{
		UserID: login.LoggedInAs(r),
		Action: fmt.Sprintf("Delete Alert: %d", id),
		URL:    fmt.Sprintf("/a/?%d", id),
	}
	if err := activitylog.Write(a); err != nil {
		sklog.Errorf("Failed to log activity: %s", err)
	}
}

type TryBugRequest struct {
	BugURITemplate string `json:"bug_uri_template"`
}

type TryBugResponse struct {
	URL string `json:"url"`
}

func alertBugTryHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if login.LoggedInAs(r) == "" {
		httputils.ReportError(w, fmt.Errorf("Not logged in."), "You must be logged in to test alerts.", http.StatusInternalServerError)
		return
	}

	req := &TryBugRequest{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		httputils.ReportError(w, err, "Failed to decode JSON.", http.StatusInternalServerError)
		return
	}
	resp := &TryBugResponse{
		URL: bug.ExampleExpand(req.BugURITemplate),
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		sklog.Errorf("Failed to encode response: %s", err)
	}
}

func alertNotifyTryHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if login.LoggedInAs(r) == "" {
		httputils.ReportError(w, fmt.Errorf("Not logged in."), "You must be logged in to try alerts.", http.StatusInternalServerError)
		return
	}

	req := &alerts.Config{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		httputils.ReportError(w, err, "Failed to decode JSON.", http.StatusInternalServerError)
		return
	}

	if err := notifier.ExampleSend(req); err != nil {
		httputils.ReportError(w, err, fmt.Sprintf("Failed to send email: %s", err), http.StatusInternalServerError)
	}
}

func makeResourceHandler() func(http.ResponseWriter, *http.Request) {
	fileServer := http.FileServer(http.Dir(*resourcesDir))
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Cache-Control", "max-age=300")
		fileServer.ServeHTTP(w, r)
	}
}

func oldMainHandler(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/e/", http.StatusMovedPermanently)
}

func oldClustersHandler(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/c/", http.StatusMovedPermanently)
}

func oldAlertsHandler(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/t/", http.StatusMovedPermanently)
}

var internalOnlyWhitelist = []string{
	"/oauth2callback/",
	"/_/reg/count",
}

// internalOnlyHandler wraps the handler with a handler that only allows
// authenticated access, with the exception of the /oauth2callback/ handler.
func internalOnlyHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if util.In(r.URL.Path, internalOnlyWhitelist) || login.LoggedInAs(r) != "" {
			h.ServeHTTP(w, r)
		} else {
			http.Redirect(w, r, login.LoginURL(w, r), http.StatusTemporaryRedirect)
		}
	})
}

func main() {

	common.InitWithMust(
		"skiaperf",
		common.PrometheusOpt(promPort),
		common.MetricsLoggingOpt(),
	)

	Init()
	login.SimpleInitMust(*port, *local)

	// Start the internal server on the internal port if requested.
	if *internalPort != "" {
		// Add the profiling endpoints to the internal router.
		internalRouter := mux.NewRouter()

		// Register pprof handlers
		internalRouter.HandleFunc("/debug/pprof/", pprof.Index)
		internalRouter.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		internalRouter.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		internalRouter.HandleFunc("/debug/pprof/profile", pprof.Profile)
		internalRouter.HandleFunc("/debug/pprof/trace", pprof.Trace)
		internalRouter.HandleFunc("/debug/pprof/{profile}", pprof.Index)

		go func() {
			sklog.Infof("Internal server on %q", *internalPort)
			sklog.Info(http.ListenAndServe(*internalPort, internalRouter))
		}()
	}

	// Resources are served directly.
	router := mux.NewRouter()

	router.PathPrefix("/res/").HandlerFunc(makeResourceHandler())
	router.PathPrefix("/dist/").HandlerFunc(makeResourceHandler())

	// Redirects for the old Perf URLs.
	router.HandleFunc("/", oldMainHandler)
	router.HandleFunc("/clusters/", oldClustersHandler)
	router.HandleFunc("/alerts/", oldAlertsHandler)

	// New endpoints that use ptracestore will go here.
	router.HandleFunc("/e/", templateHandler("newindex.html"))
	router.HandleFunc("/c/", templateHandler("clusters2.html"))
	router.HandleFunc("/t/", templateHandler("triage.html"))
	router.HandleFunc("/a/", templateHandler("alerts.html"))
	router.HandleFunc("/d/", templateHandler("dryRunAlert.html"))
	router.HandleFunc("/g/{dest:[ect]}/{hash:[a-zA-Z0-9]+}", gotoHandler)
	router.HandleFunc("/help/", helpHandler)
	router.PathPrefix("/activitylog/").HandlerFunc(activityHandler)
	router.HandleFunc("/logout/", login.LogoutHandler)
	router.HandleFunc("/loginstatus/", login.StatusHandler)
	router.HandleFunc("/oauth2callback/", login.OAuth2CallbackHandler)
	router.HandleFunc("/offline", offlineHandler)
	router.HandleFunc("/service-worker.js", scriptHandler("service-worker-bundle.js"))

	// JSON handlers.
	router.HandleFunc("/_/initpage/", initpageHandler)
	router.HandleFunc("/_/cidRange/", cidRangeHandler).Methods("POST")
	router.HandleFunc("/_/count/", countHandler).Methods("POST")
	router.HandleFunc("/_/cid/", cidHandler).Methods("POST")
	router.HandleFunc("/_/keys/", keysHandler).Methods("POST")
	router.HandleFunc("/_/frame/start", frameStartHandler).Methods("POST")
	router.HandleFunc("/_/frame/status/{id:[a-zA-Z0-9]+}", frameStatusHandler).Methods("GET")
	router.HandleFunc("/_/frame/results/{id:[a-zA-Z0-9]+}", frameResultsHandler).Methods("GET")
	router.HandleFunc("/_/cluster/start", clusterStartHandler).Methods("POST")
	router.HandleFunc("/_/cluster/status/{id:[a-zA-Z0-9]+}", clusterStatusHandler).Methods("GET")

	router.HandleFunc("/_/dryrun/start", dryrunRequests.StartHandler).Methods("POST")
	router.HandleFunc("/_/dryrun/status/{id:[a-zA-Z0-9]+}", dryrunRequests.StatusHandler).Methods("GET")

	router.HandleFunc("/_/reg/", regressionRangeHandler).Methods("POST")
	router.HandleFunc("/_/reg/count", regressionCountHandler).Methods("GET")
	router.HandleFunc("/_/reg/current", regressionCurrentHandler).Methods("GET")
	router.HandleFunc("/_/triage/", triageHandler).Methods("POST")
	router.HandleFunc("/_/alerts/", alertsHandler)
	router.HandleFunc("/_/details/", detailsHandler).Methods("POST")
	router.HandleFunc("/_/shift/", shiftHandler).Methods("POST")
	router.HandleFunc("/_/alert/list/{show}", alertListHandler).Methods("GET")
	router.HandleFunc("/_/alert/new", alertNewHandler).Methods("GET")
	router.HandleFunc("/_/alert/update", alertUpdateHandler).Methods("POST")
	router.HandleFunc("/_/alert/delete/{id:[0-9]+}", alertDeleteHandler).Methods("POST")
	router.HandleFunc("/_/alert/bug/try", alertBugTryHandler).Methods("POST")
	router.HandleFunc("/_/alert/notify/try", alertNotifyTryHandler).Methods("POST")

	var h http.Handler = router
	if *internalOnly {
		h = internalOnlyHandler(h)
	}
	h = httputils.LoggingGzipRequestResponse(h)
	if !*local {
		h = httputils.HealthzAndHTTPS(h)
	}
	http.Handle("/", h)

	sklog.Info("Ready to serve.")
	sklog.Fatal(http.ListenAndServe(*port, nil))
}
