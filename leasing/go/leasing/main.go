/*
	Leasing Server for Swarming Bots.
*/

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"net/http"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"go.skia.org/infra/go/allowed"
	"go.skia.org/infra/go/common"
	"go.skia.org/infra/go/httputils"
	"go.skia.org/infra/go/login"
	"go.skia.org/infra/go/metrics2"
	"go.skia.org/infra/go/skiaversion"
	"go.skia.org/infra/go/sklog"
	"go.skia.org/infra/go/swarming"
	"go.skia.org/infra/go/util"
	"google.golang.org/api/iterator"
)

const (
	// OAUTH2_CALLBACK_PATH is callback endpoint used for the Oauth2 flow.
	OAUTH2_CALLBACK_PATH = "/oauth2callback/"

	MAX_LEASE_DURATION_HRS = 23

	SWARMING_HARD_TIMEOUT = 24 * time.Hour

	LEASE_TASK_PRIORITY = 50

	MY_LEASES_URI         = "/my_leases"
	ALL_LEASES_URI        = "/all_leases"
	GET_TASK_STATUS_URI   = "/_/get_task_status"
	POOL_DETAILS_POST_URI = "/_/pooldetails"
	ADD_TASK_POST_URI     = "/_/add_leasing_task"
	EXTEND_TASK_POST_URI  = "/_/extend_leasing_task"
	EXPIRE_TASK_POST_URI  = "/_/expire_leasing_task"
	PROD_URI              = "https://leasing.skia.org"
)

var (
	// Flags
	host                       = flag.String("host", "localhost", "HTTP service host")
	promPort                   = flag.String("prom_port", ":20000", "Metrics service address (e.g., ':20000')")
	port                       = flag.String("port", ":8002", "HTTP service port (e.g., ':8002')")
	local                      = flag.Bool("local", false, "Running locally if true. As opposed to in production.")
	workdir                    = flag.String("workdir", ".", "Directory to use for scratch work.")
	resourcesDir               = flag.String("resources_dir", "", "The directory to find templates, JS, and CSS files.  If blank then the directory two directories up from this source file will be used.")
	pollInterval               = flag.Duration("poll_interval", 1*time.Minute, "How often the leasing server will check if tasks have expired.")
	emailClientSecretFile      = flag.String("email_client_secret_file", "/etc/leasing-email-secrets/client_secret.json", "OAuth client secret JSON file for sending email.")
	emailTokenCacheFile        = flag.String("email_token_cache_file", "/etc/leasing-email-secrets/client_token.json", "OAuth token cache file for sending email.")
	serviceAccountFile         = flag.String("service_account_file", "/var/secrets/google/key.json", "Service account JSON file.")
	poolDetailsUpdateFrequency = flag.Duration("pool_details_update_freq", 5*time.Minute, "How often to call swarming API to refresh the details of supported pools.")

	// Datastore params
	namespace   = flag.String("namespace", "leasing-server", "The Cloud Datastore namespace, such as 'leasing-server'.")
	projectName = flag.String("project_name", "google.com:skia-buildbots", "The Google Cloud project name.")

	// OAUTH params
	authWhiteList = flag.String("auth_whitelist", "google.com", "White space separated list of domains and email addresses that are allowed to login.")

	// indexTemplate is the main index.html page we serve.
	indexTemplate *template.Template = nil

	// leasesListTemplate is the page we serve on the my-leases and all-leases pages.
	leasesListTemplate *template.Template = nil

	serverURL string

	poolToDetails      map[string]*PoolDetails
	poolToDetailsMutex sync.Mutex
)

func reloadTemplates() {
	if *resourcesDir == "" {
		// If resourcesDir is not specified then consider the directory two directories up from this
		// source file as the resourcesDir.
		_, filename, _, _ := runtime.Caller(0)
		*resourcesDir = filepath.Join(filepath.Dir(filename), "../..")
	}
	indexTemplate = template.Must(template.ParseFiles(
		filepath.Join(*resourcesDir, "templates/index.html"),
		filepath.Join(*resourcesDir, "templates/header.html"),
	))
	leasesListTemplate = template.Must(template.ParseFiles(
		filepath.Join(*resourcesDir, "templates/leases_list.html"),
		filepath.Join(*resourcesDir, "templates/header.html"),
	))
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, login.LoginURL(w, r), http.StatusFound)
	return
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	if *local {
		reloadTemplates()
	}
	w.Header().Set("Content-Type", "text/html")

	if err := indexTemplate.Execute(w, nil); err != nil {
		httputils.ReportError(w, err, "Failed to expand template", http.StatusInternalServerError)
		return
	}
	return
}

type Status struct {
	TaskId  int64
	Expired bool
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	taskParam := r.FormValue("task")
	if taskParam == "" {
		httputils.ReportError(w, nil, "Missing task parameter", http.StatusInternalServerError)
		return
	}
	taskID, err := strconv.ParseInt(taskParam, 10, 64)
	if err != nil {
		httputils.ReportError(w, err, "Invalid task parameter", http.StatusInternalServerError)
		return
	}

	k, t, err := GetDSTask(taskID)
	if err != nil {
		httputils.ReportError(w, err, "Could not find task", http.StatusInternalServerError)
		return
	}

	status := Status{
		TaskId:  k.ID,
		Expired: t.Done,
	}
	if err := json.NewEncoder(w).Encode(status); err != nil {
		httputils.ReportError(w, err, "Failed to encode JSON", http.StatusInternalServerError)
		return

	}

	return
}

func poolDetailsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	poolParam := r.FormValue("pool")
	if poolParam == "" {
		httputils.ReportError(w, nil, "Missing pool parameter", http.StatusInternalServerError)
		return
	}
	poolToDetailsMutex.Lock()
	defer poolToDetailsMutex.Unlock()
	poolDetails, ok := poolToDetails[poolParam]
	if !ok {
		httputils.ReportError(w, nil, "No such pool", http.StatusInternalServerError)
		return
	}
	if err := json.NewEncoder(w).Encode(poolDetails); err != nil {
		httputils.ReportError(w, err, fmt.Sprintf("Failed to encode JSON: %v", err), http.StatusInternalServerError)
		return
	}
}

type Task struct {
	Requester          string    `json:"requester"`
	OsType             string    `json:"osType"`
	DeviceType         string    `json:"deviceType"`
	InitialDurationHrs string    `json:"duration"`
	Created            time.Time `json:"created"`
	LeaseStartTime     time.Time `json:"leaseStartTime"`
	LeaseEndTime       time.Time `json:"leaseEndTime"`
	Description        string    `json:"description"`
	Done               bool      `json:"done"`
	WarningSent        bool      `json:"warningSent"`

	TaskIdForIsolates string `json:"taskIdForIsolates"`
	SwarmingPool      string `json:"pool"`
	SwarmingBotId     string `json:"botId"`
	SwarmingServer    string `json:"swarmingServer"`
	SwarmingTaskId    string `json:"swarmingTaskId"`
	SwarmingTaskState string `json:"swarmingTaskState"`

	DatastoreId int64 `json:"datastoreId"`

	// Left for backwards compatibility but no longer used.
	Architecture  string `json:"architecture"`
	SetupDebugger bool   `json:"setupDebugger"`
}

type sortTasks []*Task

func (a sortTasks) Len() int      { return len(a) }
func (a sortTasks) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a sortTasks) Less(i, j int) bool {
	return a[i].Created.After(a[j].Created)
}

func getLeasingTasks(filterUser string) ([]*Task, error) {
	tasks := []*Task{}
	it := GetAllDSTasks(filterUser)
	for {
		t := &Task{}
		k, err := it.Next(t)
		if err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("Failed to retrieve list of tasks: %s", err)
		}
		t.DatastoreId = k.ID
		tasks = append(tasks, t)
	}
	sort.Sort(sortTasks(tasks))

	return tasks, nil
}

func leasesHandlerHelper(w http.ResponseWriter, r *http.Request, filterUser string) {
	if *local {
		reloadTemplates()
	}
	w.Header().Set("Content-Type", "text/html")

	tasks, err := getLeasingTasks(filterUser)
	if err != nil {
		httputils.ReportError(w, err, "Failed to expand template", http.StatusInternalServerError)
		return
	}

	var templateTasks = struct {
		Tasks []*Task
	}{
		Tasks: tasks,
	}
	if err := leasesListTemplate.Execute(w, templateTasks); err != nil {
		httputils.ReportError(w, err, "Failed to expand template", http.StatusInternalServerError)
		return
	}
	return
}

func myLeasesHandler(w http.ResponseWriter, r *http.Request) {
	leasesHandlerHelper(w, r, login.LoggedInAs(r))
}

func allLeasesHandler(w http.ResponseWriter, r *http.Request) {
	leasesHandlerHelper(w, r, "" /* filterUser */)
}

func extendTaskHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	taskParam := r.FormValue("task")
	if taskParam == "" {
		httputils.ReportError(w, nil, "Missing task parameter", http.StatusInternalServerError)
		return
	}
	taskID, err := strconv.ParseInt(taskParam, 10, 64)
	if err != nil {
		httputils.ReportError(w, err, "Invalid task parameter", http.StatusInternalServerError)
		return
	}

	durationParam := r.FormValue("duration")
	if durationParam == "" {
		httputils.ReportError(w, nil, "Missing duration parameter", http.StatusInternalServerError)
		return
	}
	durationHrs, err := strconv.Atoi(durationParam)
	if err != nil {
		httputils.ReportError(w, err, fmt.Sprintf("Failed to parse %s", durationParam), http.StatusInternalServerError)
		return
	}

	k, t, err := GetDSTask(taskID)
	if err != nil {
		httputils.ReportError(w, err, "Could not find task", http.StatusInternalServerError)
		return
	}

	// Add duration hours to the task's lease end time only if ends up being
	// less than 23 hours after the task's creation time.
	newLeaseEndTime := t.LeaseEndTime.Add(time.Hour * time.Duration(durationHrs))
	maxPossibleLeaseEndTime := t.Created.Add(time.Hour * time.Duration(MAX_LEASE_DURATION_HRS))
	if newLeaseEndTime.After(maxPossibleLeaseEndTime) {
		httputils.ReportError(w, nil, fmt.Sprintf("Can not extend lease beyond %d hours of the task creation time", MAX_LEASE_DURATION_HRS), http.StatusInternalServerError)
		return
	}

	// Change the lease end time.
	t.LeaseEndTime = newLeaseEndTime
	// Reset the warning sent flag since the lease has been extended.
	t.WarningSent = false
	if _, err := UpdateDSTask(k, t); err != nil {
		httputils.ReportError(w, err, "Error updating task in datastore", http.StatusInternalServerError)
		return
	}
	// Inform the requester that the task has been extended by durationHrs.
	if err := SendExtensionEmail(t.Requester, t.SwarmingServer, t.SwarmingTaskId, t.SwarmingBotId, durationHrs); err != nil {
		httputils.ReportError(w, err, "Error sending extension email", http.StatusInternalServerError)
		return
	}
}

func expireTaskHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	taskParam := r.FormValue("task")
	if taskParam == "" {
		httputils.ReportError(w, nil, "Missing task parameter", http.StatusInternalServerError)
		return
	}
	taskID, err := strconv.ParseInt(taskParam, 10, 64)
	if err != nil {
		httputils.ReportError(w, err, "Invalid task parameter", http.StatusInternalServerError)
		return
	}

	k, t, err := GetDSTask(taskID)
	if err != nil {
		httputils.ReportError(w, err, "Could not find task", http.StatusInternalServerError)
		return
	}

	// Change the task to Done, change the lease end time to now, and mark the
	// state as successfully completed.
	t.Done = true
	t.LeaseEndTime = time.Now()
	t.SwarmingTaskState = getCompletedStateStr(false)
	if _, err := UpdateDSTask(k, t); err != nil {
		httputils.ReportError(w, err, "Error updating task in datastore", http.StatusInternalServerError)
		return
	}
	// Inform the requester that the task has completed.
	if err := SendCompletionEmail(t.Requester, t.SwarmingServer, t.SwarmingTaskId, t.SwarmingBotId); err != nil {
		httputils.ReportError(w, err, "Error sending completion email", http.StatusInternalServerError)
		return
	}
}

func addTaskHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ctx := context.Background()

	task := &Task{}
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
		httputils.ReportError(w, err, fmt.Sprintf("Failed to add %T task", task), http.StatusInternalServerError)
		return
	}
	defer util.Close(r.Body)

	key := GetNewDSKey()
	if task.SwarmingBotId != "" {
		// If BotId is specified then validate it so that we can fail fast if
		// necessary.
		validBotId, err := IsBotIdValid(task.SwarmingPool, task.SwarmingBotId)
		if err != nil {
			httputils.ReportError(w, err, fmt.Sprintf("Error querying swarming for botId %s in pool %s", task.SwarmingBotId, task.SwarmingPool), http.StatusInternalServerError)
			return
		}
		if !validBotId {
			httputils.ReportError(w, err, fmt.Sprintf("Could not find botId %s in pool %s", task.SwarmingBotId, task.SwarmingPool), http.StatusInternalServerError)
			return
		}
	}
	// Populate deviceType only if Android is the osType.
	if task.OsType != "Android" {
		task.DeviceType = ""
	}
	// Add the username of the requester.
	task.Requester = login.LoggedInAs(r)
	// Add the created time.
	task.Created = time.Now()
	// Set to pending.
	task.SwarmingTaskState = swarming.TASK_STATE_PENDING

	// Isolate artifacts.
	var isolateDetails *IsolateDetails
	if task.TaskIdForIsolates != "" {
		t, err := GetSwarmingTaskMetadata(task.SwarmingPool, task.TaskIdForIsolates)
		if err != nil {
			httputils.ReportError(w, err, fmt.Sprintf("Could not find taskId %s in pool %s", task.TaskIdForIsolates, task.SwarmingPool), http.StatusInternalServerError)
			return
		}
		isolateDetails, err = GetIsolateDetails(ctx, *serviceAccountFile, t.Request.Properties)
		if err != nil {
			httputils.ReportError(w, err, fmt.Sprintf("Could not get isolate details of task %s in pool %s", task.TaskIdForIsolates, task.SwarmingPool), http.StatusInternalServerError)
			return
		}
	} else {
		isolateDetails = &IsolateDetails{}
	}

	datastoreKey, err := PutDSTask(key, task)
	if err != nil {
		httputils.ReportError(w, err, fmt.Sprintf("Error putting task in datastore: %v", err), http.StatusInternalServerError)
		return
	}
	isolateHash, err := GetIsolateHash(ctx, task.SwarmingPool, isolateDetails.IsolateDep)
	if err != nil {
		httputils.ReportError(w, err, fmt.Sprintf("Error when getting isolate hash: %v", err), http.StatusInternalServerError)
		return
	}
	// Trigger the swarming task.
	swarmingTaskId, err := TriggerSwarmingTask(task.SwarmingPool, task.Requester, strconv.Itoa(int(datastoreKey.ID)), task.OsType, task.DeviceType, task.SwarmingBotId, serverURL, isolateHash, isolateDetails)
	if err != nil {
		httputils.ReportError(w, err, fmt.Sprintf("Error when triggering swarming task: %v", err), http.StatusInternalServerError)
		return
	}

	// Update the task with swarming fields.
	swarmingInstance := GetSwarmingInstance(task.SwarmingPool)
	task.SwarmingServer = swarmingInstance.SwarmingServer
	task.SwarmingTaskId = swarmingTaskId
	if _, err = UpdateDSTask(datastoreKey, task); err != nil {
		httputils.ReportError(w, err, fmt.Sprintf("Error updating task with swarming fields in datastore: %v", err), http.StatusInternalServerError)
		return
	}

	sklog.Infof("Added %v task into the datastore with key %s", task, datastoreKey)
}

func runServer() {
	r := mux.NewRouter()
	r.PathPrefix("/res/").HandlerFunc(httputils.MakeResourceHandler(*resourcesDir))

	r.HandleFunc("/", indexHandler)
	r.HandleFunc(MY_LEASES_URI, myLeasesHandler)
	r.HandleFunc(ALL_LEASES_URI, allLeasesHandler)
	r.HandleFunc(POOL_DETAILS_POST_URI, poolDetailsHandler).Methods("POST")
	r.HandleFunc(ADD_TASK_POST_URI, addTaskHandler).Methods("POST")
	r.HandleFunc(EXTEND_TASK_POST_URI, extendTaskHandler).Methods("POST")
	r.HandleFunc(EXPIRE_TASK_POST_URI, expireTaskHandler).Methods("POST")
	r.HandleFunc("/json/version", skiaversion.JsonHandler)
	r.HandleFunc("/loginstatus/", login.StatusHandler)

	h := httputils.LoggingGzipRequestResponse(r)
	h = login.RestrictViewer(h)
	h = login.ForceAuth(h, login.DEFAULT_REDIRECT_URL)
	h = httputils.HealthzAndHTTPS(h)

	http.Handle("/", h)
	http.HandleFunc(GET_TASK_STATUS_URI, statusHandler)

	sklog.Infof("Ready to serve on %s", serverURL)
	sklog.Fatal(http.ListenAndServe(*port, nil))
}

type ClientConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type Installed struct {
	Installed ClientConfig `json:"installed"`
}

func main() {
	flag.Parse()

	common.InitWithMust(
		"leasing",
		common.PrometheusOpt(promPort),
		common.MetricsLoggingOpt(),
	)

	skiaversion.MustLogVersion()

	reloadTemplates()
	serverURL = "https://" + *host
	if *local {
		serverURL = "http://" + *host + *port
	}

	// Initialize mailing library.
	var cfg Installed
	err := util.WithReadFile(*emailClientSecretFile, func(f io.Reader) error {
		return json.NewDecoder(f).Decode(&cfg)
	})
	if err != nil {
		sklog.Fatalf("Failed to read client secrets from %q: %s", *emailClientSecretFile, err)
	}
	// Create a copy of the token cache file since mounted secrets are read-only
	// and the access token will need to be updated for the oauth2 flow.
	if !*local {
		fout, err := ioutil.TempFile("", "")
		if err != nil {
			sklog.Fatalf("Unable to create temp file %q: %s", fout.Name(), err)
		}
		err = util.WithReadFile(*emailTokenCacheFile, func(fin io.Reader) error {
			_, err := io.Copy(fout, fin)
			if err != nil {
				err = fout.Close()
			}
			return err
		})
		if err != nil {
			sklog.Fatalf("Failed to write token cache file from %q to %q: %s", *emailTokenCacheFile, fout.Name(), err)
		}
		*emailTokenCacheFile = fout.Name()
	}
	if err := MailInit(cfg.Installed.ClientID, cfg.Installed.ClientSecret, *emailTokenCacheFile); err != nil {
		sklog.Fatalf("Failed to init mail library: %s", err)
	}

	var allow allowed.Allow
	if !*local {
		allow = allowed.NewAllowedFromList([]string{*authWhiteList})
	} else {
		allow = allowed.NewAllowedFromList([]string{"fred@example.org", "barney@example.org", "wilma@example.org"})
	}
	login.SimpleInitWithAllow(*port, *local, nil, nil, allow)

	// Initialize isolate and swarming.
	if err := SwarmingInit(*serviceAccountFile); err != nil {
		sklog.Fatalf("Failed to init isolate and swarming: %s", err)
	}

	// Initialize cloud datastore.
	if err := DatastoreInit(*projectName, *namespace); err != nil {
		sklog.Fatalf("Failed to init cloud datastore: %s", err)
	}

	poolToDetails, err = GetDetailsOfAllPools()
	if err != nil {
		sklog.Fatalf("Could not get details of all pools: %s", err)
	}
	go func() {
		for range time.Tick(*poolDetailsUpdateFrequency) {
			poolToDetailsMutex.Lock()
			poolToDetails, err = GetDetailsOfAllPools()
			poolToDetailsMutex.Unlock()
			if err != nil {
				sklog.Errorf("Could not get details of all pools: %s", err)
			}
		}
	}()

	healthyGauge := metrics2.GetInt64Metric("healthy")
	go func() {
		for range time.Tick(*pollInterval) {
			healthyGauge.Update(1)
			if err := pollSwarmingTasks(); err != nil {
				sklog.Errorf("Error when checking for expired tasks: %v", err)
			}
		}
	}()

	runServer()
}
