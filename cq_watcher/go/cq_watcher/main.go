// The CQ Watcher monitors the Skia CQ for Gerrit CLs. It pumps the results of
// the monitoring into InfluxDB.
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"go.skia.org/infra/go/common"
	"go.skia.org/infra/go/cq"
	"go.skia.org/infra/go/gerrit"
	"go.skia.org/infra/go/httputils"
	"go.skia.org/infra/go/metrics2"
	"go.skia.org/infra/go/sklog"
)

var (
	testing  = flag.Bool("testing", false, "Set to true for local testing.")
	promPort = flag.String("prom_port", ":20000", "Metrics service address (e.g., ':20000')")
)

const (
	// How often the two pollers should poll Gerrit.
	IN_FLIGHT_POLL_TIME    = 30 * time.Second
	AFTER_COMMIT_POLL_TIME = 5 * time.Minute

	// How often to refresh the trybots received from cq.cfg.
	REFRESH_CQ_TRYBOTS_TIME = time.Hour

	MAX_CLS_PER_POLL = 100
	METRIC_NAME      = "cq_watcher"
)

// monitorStatsForInFlightCLs queries Gerrit for all CLs currently in dry run
// or waiting for commit. The following metrics are then reported for the
// matching CLs:
// * Number of CLs waiting for the CQ (both for dry runs and commits).
// * How long CQ trybots have been running for.
// * How many CQ trybots have been triggered.
func monitorStatsForInFlightCLs(ctx context.Context, cqClient *cq.Client, gerritClient *gerrit.Gerrit) {
	liveness := metrics2.NewLiveness(fmt.Sprintf("%s_%s", METRIC_NAME, cq.INFLIGHT_METRIC_NAME))
	cqMetric := metrics2.GetInt64Metric(fmt.Sprintf("%s_%s_%s", METRIC_NAME, cq.INFLIGHT_METRIC_NAME, cq.INFLIGHT_WAITING_IN_CQ))
	for range time.Tick(time.Duration(IN_FLIGHT_POLL_TIME)) {
		dryRunChanges, err := gerritClient.Search(ctx, MAX_CLS_PER_POLL, true, gerrit.SearchStatus("open"), gerrit.SearchProject("skia"), gerrit.SearchLabel(gerrit.COMMITQUEUE_LABEL, "1"))
		if err != nil {
			sklog.Errorf("Error searching for open changes with dry run in Gerrit: %s", err)
			continue
		}
		cqChanges, err := gerritClient.Search(ctx, MAX_CLS_PER_POLL, true, gerrit.SearchStatus("open"), gerrit.SearchProject("skia"), gerrit.SearchLabel(gerrit.COMMITQUEUE_LABEL, "2"))
		if err != nil {
			sklog.Errorf("Error searching for open changes waiting for CQ in Gerrit: %s", err)
			continue
		}

		// Combine dryrun and CQ changes into one slice.
		changes := dryRunChanges
		changes = append(changes, cqChanges...)
		cqMetric.Update(int64(len(changes)))

		for _, change := range changes {
			if err := cqClient.ReportCQStats(ctx, change.Issue); err != nil {
				sklog.Errorf("Could not get CQ stats for %d: %s", change.Issue, err)
				continue
			}
		}
		// If no CLs are currently in the CQ then report dummy data to prevent
		// "Failed to execute query for rule..." failures in the alertserver.
		if len(changes) == 0 {
			durationTags := map[string]string{
				"trybot": "DummyTrybot",
			}
			inflightTrybotDurationMetric := metrics2.GetInt64Metric(fmt.Sprintf("%s_%s_%s", METRIC_NAME, cq.INFLIGHT_METRIC_NAME, cq.INFLIGHT_TRYBOT_DURATION), durationTags)
			inflightTrybotDurationMetric.Update(0)
			trybotNumDurationMetric := metrics2.GetInt64Metric(fmt.Sprintf("%s_%s_%s", METRIC_NAME, cq.INFLIGHT_METRIC_NAME, cq.INFLIGHT_TRYBOT_NUM), map[string]string{})
			trybotNumDurationMetric.Update(0)
		}
		liveness.Reset()
	}
}

// monitorStatsForLandedCLs queries Gerrit for all CLs that have been merged in
// the last AFTER_COMMIT_POLL_TIME seconds. The following metrics are then
// reported for the matching CLs:
// * The total time the CL spent waiting for CQ trybots to complete.
// * The time each CQ trybot took to complete.
func monitorStatsForLandedCLs(ctx context.Context, cqClient *cq.Client, gerritClient *gerrit.Gerrit) {
	liveness := metrics2.NewLiveness(fmt.Sprintf("%s_%s", METRIC_NAME, cq.LANDED_METRIC_NAME))
	previousPollChanges := []*gerrit.ChangeInfo{}
	for range time.Tick(time.Duration(AFTER_COMMIT_POLL_TIME)) {
		// Add a short (2 min) buffer to overlap with the last poll to make sure
		// we do not lose any edge cases.
		t_delta := time.Now().Add(-AFTER_COMMIT_POLL_TIME).Add(-2 * time.Minute)
		changes, err := gerritClient.Search(ctx, MAX_CLS_PER_POLL, true, gerrit.SearchModifiedAfter(t_delta), gerrit.SearchStatus("merged"), gerrit.SearchProject("skia"))
		if err != nil {
			sklog.Errorf("Error searching for merged changes in Gerrit: %s", err)
			continue
		}
		for _, change := range changes {
			if gerrit.ContainsAny(change.Issue, previousPollChanges) {
				continue
			}
			if err := cqClient.ReportCQStats(ctx, change.Issue); err != nil {
				sklog.Errorf("Could not get CQ stats for %d: %s", change.Issue, err)
				continue
			}
		}
		previousPollChanges = changes
		liveness.Reset()
	}
}

func refreshCQTryBots(cqClient *cq.Client) {
	for range time.Tick(time.Duration(REFRESH_CQ_TRYBOTS_TIME)) {
		if err := cqClient.RefreshCQTryBots(); err != nil {
			sklog.Errorf("Error refreshing CQ trybots: %s", err)
		}
	}
}

func main() {
	common.InitWithMust(METRIC_NAME, common.PrometheusOpt(promPort))

	ctx := context.Background()
	httpClient := httputils.NewTimeoutClient()
	gerritClient, err := gerrit.NewGerrit(gerrit.GERRIT_SKIA_URL, "", httpClient)
	if err != nil {
		sklog.Fatalf("Failed to create Gerrit client: %s", err)
	}
	cqClient, err := cq.NewClient(gerritClient, cq.GetSkiaCQTryBots, METRIC_NAME)
	if err != nil {
		sklog.Fatalf("Failed to create CQ client: %s", err)
	}

	// Periodically refresh slice of trybots to make sure any changes to cq.cfg
	// are picked up without needing to restart the app.
	go refreshCQTryBots(cqClient)
	// Monitor in-flight CLs.
	go monitorStatsForInFlightCLs(ctx, cqClient, gerritClient)
	// Monitor landed CLs.
	monitorStatsForLandedCLs(ctx, cqClient, gerritClient)
}
