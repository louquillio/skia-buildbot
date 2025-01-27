// perf-ingest listens to a PubSub Topic for new files that appear
// in a storage bucket and then ingests those files into BigTable.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/bigtable"
	"cloud.google.com/go/pubsub"
	"cloud.google.com/go/storage"
	"go.skia.org/infra/go/auth"
	"go.skia.org/infra/go/common"
	"go.skia.org/infra/go/git/gitinfo"
	"go.skia.org/infra/go/httputils"
	"go.skia.org/infra/go/metrics2"
	"go.skia.org/infra/go/paramtools"
	"go.skia.org/infra/go/query"
	"go.skia.org/infra/go/skerr"
	"go.skia.org/infra/go/sklog"
	"go.skia.org/infra/go/util"
	"go.skia.org/infra/go/vcsinfo"
	"go.skia.org/infra/perf/go/btts"
	"go.skia.org/infra/perf/go/config"
	"go.skia.org/infra/perf/go/ingestcommon"
	"go.skia.org/infra/perf/go/ingestevents"
	"google.golang.org/api/option"
)

// flags
var (
	configName = flag.String("config_name", "nano", "Name of the perf ingester config to use.")
	local      = flag.Bool("local", false, "Running locally if true. As opposed to in production.")
	port       = flag.String("port", ":8000", "HTTP service address (e.g., ':8000')")
	promPort   = flag.String("prom_port", ":20000", "Metrics service address (e.g., ':10110')")
)

const (
	// MAX_PARALLEL_RECEIVES is the number of Go routines we want to run. Determined experimentally.
	MAX_PARALLEL_RECEIVES = 1
)

var (
	// mutex protects hashCache.
	mutex = sync.Mutex{}

	// hashCache is a cache of results from calling vcs.IndexOf().
	hashCache = map[string]int{}

	// pubSubClient is a client used for both receiving PubSub messages from GCS
	// and for sending ingestion notifications if the config specifies such a
	// Topic.
	pubSubClient *pubsub.Client

	// The configuration data for the selected Perf instance.
	cfg *config.PerfBigTableConfig
)

var (
	// NonRecoverableError is returned if the error is non-recoverable and we
	// should Ack the PubSub message. This might happen if, for example, a
	// non-JSON file gets dropped in the bucket.
	NonRecoverableError = errors.New("Non-recoverable ingestion error.")
)

// getParamsAndValues returns two parallel slices, each slice contains the
// params and then the float for a single value of a trace. It also returns the
// consolidated ParamSet built from all the Params.
func getParamsAndValues(b *ingestcommon.BenchData) ([]paramtools.Params, []float32, paramtools.ParamSet) {
	params := []paramtools.Params{}
	values := []float32{}
	ps := paramtools.ParamSet{}
	for testName, allConfigs := range b.Results {
		for configName, result := range allConfigs {
			key := paramtools.Params(b.Key).Copy()
			key["test"] = testName
			key["config"] = configName
			key.Add(paramtools.Params(b.Options))

			// If there is an options map inside the result add it to the params.
			if resultOptions, ok := result["options"]; ok {
				if opts, ok := resultOptions.(map[string]interface{}); ok {
					for k, vi := range opts {
						// Ignore the very long and not useful GL_ values, we can retrieve
						// them later via ptracestore.Details.
						if strings.HasPrefix(k, "GL_") {
							continue
						}
						if s, ok := vi.(string); ok {
							key[k] = s
						}
					}
				}
			}

			for k, vi := range result {
				if k == "options" || k == "samples" {
					continue
				}
				key["sub_result"] = k
				floatVal, ok := vi.(float64)
				if !ok {
					sklog.Errorf("Found a non-float64 in %v", result)
					continue
				}

				key = query.ForceValid(key)
				params = append(params, key.Copy())
				values = append(values, float32(floatVal))
				ps.AddParams(key)
			}
		}
	}
	ps.Normalize()
	return params, values, ps
}

func indexFromCache(hash string) (int, bool) {
	mutex.Lock()
	defer mutex.Unlock()

	index, ok := hashCache[hash]
	return index, ok
}

func indexToCache(hash string, index int) {
	mutex.Lock()
	defer mutex.Unlock()

	hashCache[hash] = index
}

// processSingleFile parses the contents of a single JSON file and writes the values into BigTable.
//
// If 'branches' is not empty then restrict to ingesting just the branches in the slice.
func processSingleFile(ctx context.Context, store *btts.BigTableTraceStore, vcs vcsinfo.VCS, filename string, r io.Reader, timestamp time.Time, branches []string) error {
	benchData, err := ingestcommon.ParseBenchDataFromReader(r)
	if err != nil {
		sklog.Errorf("Failed to read or parse data: %s", err)
		return NonRecoverableError
	}

	branch, ok := benchData.Key["branch"]
	if ok {
		if len(branches) > 0 {
			if !util.In(branch, branches) {
				return nil
			}
		}
	} else {
		sklog.Infof("No branch name.")
	}

	params, values, paramset := getParamsAndValues(benchData)
	// Don't do any more work if there's no data to ingest.
	if len(params) == 0 {
		metrics2.GetCounter("perf_ingest_no_data_in_file", map[string]string{"branch": branch}).Inc(1)
		sklog.Infof("No data in: %q", filename)
		return nil
	}
	sklog.Infof("Processing %q", filename)
	index, ok := indexFromCache(benchData.Hash)
	if !ok {
		var err error
		index, err = vcs.IndexOf(ctx, benchData.Hash)
		if err != nil {
			if err := vcs.Update(context.Background(), true, false); err != nil {
				return fmt.Errorf("Could not ingest, failed to pull: %s", err)
			}
			index, err = vcs.IndexOf(ctx, benchData.Hash)
			if err != nil {
				sklog.Errorf("Could not ingest, hash not found even after pulling %q: %s", benchData.Hash, err)
				return NonRecoverableError
			}
		}
		indexToCache(benchData.Hash, index)
	}
	err = store.WriteTraces(int32(index), params, values, paramset, filename, timestamp)
	if err != nil {
		return err
	}
	return sendPubSubEvent(params, paramset, filename)
}

// sendPubSubEvent sends the unencoded params and paramset found in a single
// ingested file to the PubSub topic specified in the selected Perf instances
// configuration data.
func sendPubSubEvent(params []paramtools.Params, paramset paramtools.ParamSet, filename string) error {
	if cfg.FileIngestionTopicName == "" {
		return nil
	}
	traceIDs := make([]string, 0, len(params))
	for _, p := range params {
		key, err := query.MakeKeyFast(p)
		if err != nil {
			continue
		}
		traceIDs = append(traceIDs, key)
	}
	ie := &ingestevents.IngestEvent{
		TraceIDs: traceIDs,
		ParamSet: paramset,
		Filename: filename,
	}
	body, err := ingestevents.CreatePubSubBody(ie)
	if err != nil {
		return skerr.Wrapf(err, "Failed to encode PubSub body for topic: %q", cfg.FileIngestionTopicName)
	}
	msg := &pubsub.Message{
		Data: body,
	}
	ctx := context.Background()
	_, err = pubSubClient.Topic(cfg.FileIngestionTopicName).Publish(ctx, msg).Get(ctx)

	return err
}

// Event is used to deserialize the PubSub data.
//
// The PubSub event data is a JSON serialized storage.ObjectAttrs object.
// See https://cloud.google.com/storage/docs/pubsub-notifications#payload
type Event struct {
	Bucket string `json:"bucket"`
	Name   string `json:"name"`
}

func main() {
	common.InitWithMust(
		"perf-ingest",
		common.PrometheusOpt(promPort),
		common.MetricsLoggingOpt(),
	)

	// nackCounter is the number files we weren't able to ingest.
	nackCounter := metrics2.GetCounter("nack", nil)
	// ackCounter is the number files we were able to ingest.
	ackCounter := metrics2.GetCounter("ack", nil)

	ctx := context.Background()
	var ok bool
	cfg, ok = config.PERF_BIGTABLE_CONFIGS[*configName]
	if !ok {
		sklog.Fatalf("Invalid --config value: %q", *configName)
	}
	hostname, err := os.Hostname()
	if err != nil {
		sklog.Fatalf("Failed to get hostname: %s", err)
	}
	ts, err := auth.NewDefaultTokenSource(*local, bigtable.Scope, storage.ScopeReadOnly, pubsub.ScopePubSub)
	if err != nil {
		sklog.Fatalf("Failed to create TokenSource: %s", err)
	}

	client := httputils.DefaultClientConfig().WithTokenSource(ts).WithoutRetries().Client()
	gcsClient, err := storage.NewClient(ctx, option.WithHTTPClient(client))
	if err != nil {
		sklog.Fatalf("Failed to create GCS client: %s", err)
	}
	pubSubClient, err = pubsub.NewClient(ctx, cfg.Project, option.WithTokenSource(ts))
	if err != nil {
		sklog.Fatal(err)
	}

	// When running in production we have every instance use the same topic name so that
	// they load-balance pulling items from the topic.
	subName := fmt.Sprintf("%s-%s", cfg.Topic, "prod")
	if *local {
		// When running locally create a new topic for every host.
		subName = fmt.Sprintf("%s-%s", cfg.Topic, hostname)
	}
	sub := pubSubClient.Subscription(subName)
	ok, err = sub.Exists(ctx)
	if err != nil {
		sklog.Fatalf("Failed checking subscription existence: %s", err)
	}
	if !ok {
		sub, err = pubSubClient.CreateSubscription(ctx, subName, pubsub.SubscriptionConfig{
			Topic: pubSubClient.Topic(cfg.Topic),
		})
		if err != nil {
			sklog.Fatalf("Failed creating subscription: %s", err)
		}
	}

	// How many Go routines should be processing messages?
	sub.ReceiveSettings.MaxOutstandingMessages = MAX_PARALLEL_RECEIVES
	sub.ReceiveSettings.NumGoroutines = MAX_PARALLEL_RECEIVES

	vcs, err := gitinfo.CloneOrUpdate(ctx, cfg.GitUrl, "/tmp/skia_ingest_checkout", true)
	if err != nil {
		sklog.Fatal(err)
	}

	store, err := btts.NewBigTableTraceStoreFromConfig(ctx, cfg, ts, true)
	if err != nil {
		sklog.Fatal(err)
	}

	// Process all incoming PubSub requests.
	go func() {
		for {
			// Wait for PubSub events.
			err := sub.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
				// Set success to true if we should Ack the PubSub message, otherwise
				// the message will be Nack'd, and PubSub will try to send the message
				// again.
				success := false
				defer func() {
					if success {
						ackCounter.Inc(1)
						msg.Ack()
					} else {
						nackCounter.Inc(1)
						msg.Nack()
					}
				}()
				// Decode the event, which is a GCS event that a file was written.
				var event Event
				if err := json.Unmarshal(msg.Data, &event); err != nil {
					sklog.Error(err)
					return
				}
				// Transaction logs for android_ingest are written to the same bucket,
				// which we should ignore.
				if strings.Contains(event.Name, "/tx_log/") {
					// Ack the file so we don't process it again.
					success = true
					return
				}
				// Load the file.
				obj := gcsClient.Bucket(event.Bucket).Object(event.Name)
				attrs, err := obj.Attrs(ctx)
				if err != nil {
					sklog.Error(err)
					return
				}
				reader, err := obj.NewReader(ctx)
				if err != nil {
					sklog.Error(err)
					return
				}
				defer util.Close(reader)
				sklog.Infof("Filename: %q", attrs.Name)
				// Pull data out of file and write it into BigTable.
				fullName := fmt.Sprintf("gs://%s/%s", event.Bucket, event.Name)
				err = processSingleFile(ctx, store, vcs, fullName, reader, attrs.Created, cfg.Branches)
				if err := reader.Close(); err != nil {
					sklog.Errorf("Failed to close: %s", err)
				}
				if err == NonRecoverableError {
					success = true
				} else if err != nil {
					sklog.Errorf("Failed to write results: %s", err)
					return
				}
				success = true
			})
			if err != nil {
				sklog.Errorf("Failed receiving pubsub message: %s", err)
			}
		}
	}()

	// Set up the http handler to indicate ready-ness and start serving.
	http.HandleFunc("/ready", httputils.ReadyHandleFunc)
	log.Fatal(http.ListenAndServe(*port, nil))
}
