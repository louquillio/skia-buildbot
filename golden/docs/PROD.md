Gold Production Manual
======================

First make sure you are familiar with the design of gold by reading the
[architectural overview](https://goto.google.com/life-of-a-gold-image) doc.

General Metrics
===============
The following dashboard is for the skia-public instances:
<https://grafana2.skia.org/d/m8kl1amWk/gold-panel-public>.

The following dashboard is for the skia-corp instances:
<https://skia-mon.corp.goog/d/m8kl1amWk/gold-panel-corp>

Some things to look for:

 - Do goroutines or memory increase continuously (e.g leaks)?
 - How fresh is the tile data? (this could indicate something is stuck).
 - How is ingestion liveness?  Anything stuck?

QPS
---
To determine load of various services, we can use the fact that there are
`defer metrics2.FuncTimer().Stop()` all around the code base to get a rough idea of QPS/load.

On <https://prom2.skia.org> or <https://skia-prom.corp.goog>, try doing the search:

    rate(timer_func_timer_ns_count{appgroup=~"gold.+"}[1m])

You can even search by package, e.g. all Firestore related functions:

    rate(timer_func_timer_ns_count{package=~".+fs_.+"}[1m])

If you find something problematic, then `timer_func_timer_ns{appgroup=~"gold.+"}/1000000000` is
how to see how many milliseconds a given timer actually took.

General Logs
============
Logs for Gold instances in skia-public/skia-corp are in the usual
GKE container grouping, for example:
<https://console.cloud.google.com/logs/viewer?project=skia-public&resource=container&logName=projects%2Fskia-public%2Flogs%2Fgold-flutter-skiacorrectness>

Alerts
======

Items below here should include target links from alerts.

GoldStreamingIngestionStalled
--------------------
Gold has a pubsub subscription for events created in its bucket.
This alert means we haven't successfully ingested a file in over 24 hours.
This could mean that ingestion is throwing errors on every file or
the repo isn't very busy.

This has happened before because gitsync stopped, so check that out too.

Key metrics: liveness_gold_bt_s{metric="last-successful-process"}, liveness_last_successful_git_sync_s


GoldPollingIngestionStalled
--------------------
Gold regularly polls its GCS buckets for any files that were not
successfully ingested via PubSub event when the file was created (aka "streaming").
This alert means it has been at least 10 minutes since this happened;
this should happen every 5 minutes or so, even in not-busy repos.

This has happened before because gitsync stopped, so check that out too.

Key metrics: liveness_gold_bt_s{metric="since-last-run"}, liveness_last_successful_git_sync_s


GoldIgnoreMonitoring
--------------------
This alert means gold was unable to calculate which ignore rules were expired.
Search the logs for "ignorestore.go" to get a hint as to why.

This has happened before because of manually-edited (and incorrect) Datastore data
so maybe check out the raw data
<https://console.cloud.google.com/datastore/entities;kind=IgnoreRule;ns=gold-flutter/query/kind?project=skia-public>
and
<https://console.cloud.google.com/datastore/entities;kind=HelperRecentKeys;ns=gold-flutter/query/kind?project=skia-public>

Key metrics: gold_expired_ignore_rules_monitoring

GoldCommitTooOldWallTime
----------------------
Too much time has elapsed since Gold noticed a commit. This occasionally is a false positive
if a commit simply hasn't landed in the repo we are tracking.

In the past, this has indicated git-sync might have had problems, so check out
the logs of the relevant git-sync instance.

Key metrics: gold_last_commit_age_s

GoldCommitTooOldNewerCommit
----------------------
Gold has noticed there is a newer commit available for processing, but hasn't
succeeded on moving forward.

This would usually indicate an issue with Gold itself, so check
the logs of the Gold instance.

Key metrics: gold_last_commit_age_s

GoldStatusStalled
----------------------
Gold has been unable to re-process some of the data used to keep the
frontend up to date.

This hasn't happened yet, but likely would indicate a problem with
something in golden/go/status.go

Key metrics: liveness_gold_status_monitoring_s

GoldIngestionErrorRate
----------------------
The recent rate of errors for ingestion is high, it is typically well below 0.1.
See the error logs for the given instance for more.

GoldDiffServerErrorRate
----------------------
The recent rate of errors for the diff server is high, it is typically well
below 0.1.
See the error logs for the given instance for more.

GoldErrorRate
----------------------
The recent rate of errors for the main gold instance is high, it is
typically well below 0.1.
See the error logs for the given instance for more.

GoldExpectationsStale
----------------------
Currently, our baseline servers use QuerySnapshotIterators when fetching expectations out of
Firestore. Those run on goroutines. This alert will be active if any of those sharded
iterators are down, thus yielding stale results.

To fix, delete one baseliner pod of the affected instance at a time until all of them
have restarted and are healthy.

If this alert fires, it probably means the related logic in fs_expstore needs to be rethought.

GoldCorruptTryJobData
---------------------
This section covers both GoldCorruptTryJobParamMaps and GoldTryJobResultsIncompleteData which are
probably both active or both inactive. TryJobResults are stored in firestore in a separate
document from the Param maps that store the keys so as to lower request data. However, if somehow
data was only partially uploaded or corrupted, there might be TryJobResults that reference
Params that don't exist.

If this happens, we might need to re-ingest the TryJob data to re-construct the missing data.

GoldNoDataAtHead
----------------
The last 20 commits (100 for Chrome, since their tests are slower) have seen 0 data. This probably
means something is wrong with goldctl or whatever means is getting data into gold.

Check out the bucket for the instance to confirm nothing is being uploaded and the logs
of the ingester if newer stuff is in the bucket, but hasn't been processed already. (If it's
an issue with ingestion, expect other alerts to be firing)

Key metrics: gold_empty_commits_at_head
