package codereview

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"strings"
	"time"

	github_api "github.com/google/go-github/github"
	"go.skia.org/infra/autoroll/go/recent_rolls"
	"go.skia.org/infra/autoroll/go/revision"
	"go.skia.org/infra/autoroll/go/state_machine"
	"go.skia.org/infra/go/autoroll"
	"go.skia.org/infra/go/gerrit"
	"go.skia.org/infra/go/github"
	"go.skia.org/infra/go/httputils"
	"go.skia.org/infra/go/sklog"
	"go.skia.org/infra/go/travisci"
	"go.skia.org/infra/go/util"
)

type RollImpl interface {
	state_machine.RollCLImpl

	// Insert the roll into the DB.
	InsertIntoDB(ctx context.Context) error
}

// updateIssueFromGerrit loads details about the issue from the Gerrit API and
// updates the AutoRollIssue accordingly.
func updateIssueFromGerrit(ctx context.Context, cfg *GerritConfig, a *autoroll.AutoRollIssue, g gerrit.GerritInterface) (*gerrit.ChangeInfo, error) {
	info, err := g.GetIssueProperties(ctx, a.Issue)
	if err != nil {
		return nil, fmt.Errorf("Failed to get issue properties: %s", err)
	}
	if cfg.CanQueryTrybots() {
		// Use try results from the most recent non-trivial patchset.
		if len(info.Patchsets) == 0 {
			return nil, fmt.Errorf("Issue %d has no patchsets!", a.Issue)
		}
		nontrivial := info.GetNonTrivialPatchSets()
		if len(nontrivial) == 0 {
			msg := fmt.Sprintf("No non-trivial patchsets for %d; trivial patchsets:\n", a.Issue)
			for _, ps := range info.Patchsets {
				msg += fmt.Sprintf("  %+v\n", ps)
			}
			return nil, errors.New(msg)
		}
		tries, err := g.GetTrybotResults(ctx, a.Issue, nontrivial[len(nontrivial)-1].Number)
		if err != nil {
			return nil, fmt.Errorf("Failed to retrieve try results: %s", err)
		}
		tryResults, err := autoroll.TryResultsFromBuildbucket(tries)
		if err != nil {
			return nil, fmt.Errorf("Failed to process try results: %s", err)
		}
		a.TryResults = tryResults
	}
	if err := updateIssueFromGerritChangeInfo(a, info, g.Config()); err != nil {
		return nil, fmt.Errorf("Failed to convert issue format: %s", err)
	}
	return info, nil
}

// updateIssueFromGerritChangeInfo updates the AutoRollIssue instance based on
// the given gerrit.ChangeInfo.
func updateIssueFromGerritChangeInfo(i *autoroll.AutoRollIssue, ci *gerrit.ChangeInfo, gc *gerrit.Config) error {
	if i.Issue != ci.Issue {
		return fmt.Errorf("CL ID %d differs from existing issue number %d!", ci.Issue, i.Issue)
	}
	i.CqFinished = !i.IsDryRun && !gc.CqRunning(ci)
	i.CqSuccess = !i.IsDryRun && gc.CqSuccess(ci)
	i.DryRunFinished = i.IsDryRun && !gc.DryRunRunning(ci)
	i.DryRunSuccess = i.IsDryRun && gc.DryRunSuccess(ci, gc.DryRunUsesTryjobResults && i.AllTrybotsSucceeded())

	ps := make([]int64, 0, len(ci.Patchsets))
	for _, p := range ci.Patchsets {
		ps = append(ps, p.Number)
	}
	i.Closed = ci.IsClosed()
	i.Committed = ci.Committed
	i.Created = ci.Created
	i.Modified = ci.Updated
	i.Patchsets = ps
	i.Subject = ci.Subject
	i.Result = autoroll.RollResult(i)
	// TODO(borenet): If this validation fails, it's likely that it will
	// continue to fail indefinitely, resulting in a stuck roller.
	// Additionally, this AutoRollIssue instance persists in the AutoRoller
	// for its entire lifetime; it's possible to partially fail to update
	// it and end up in an inconsistent state.
	return i.Validate()
}

// gerritRoll is an implementation of RollImpl.
type gerritRoll struct {
	ci               *gerrit.ChangeInfo
	issue            *autoroll.AutoRollIssue
	issueUrl         string
	finishedCallback func(context.Context, RollImpl) error
	g                gerrit.GerritInterface
	recent           *recent_rolls.RecentRolls
	retrieveRoll     func(context.Context) (*gerrit.ChangeInfo, error)
	result           string
	rollingTo        *revision.Revision
}

// newGerritRoll obtains a gerritRoll instance from the given Gerrit issue
// number.
func newGerritRoll(ctx context.Context, cfg *GerritConfig, issue *autoroll.AutoRollIssue, g gerrit.GerritInterface, recent *recent_rolls.RecentRolls, issueUrlBase string, rollingTo *revision.Revision, cb func(context.Context, RollImpl) error) (RollImpl, error) {
	ci, err := updateIssueFromGerrit(ctx, cfg, issue, g)
	if err != nil {
		return nil, err
	}
	return &gerritRoll{
		ci:               ci,
		issue:            issue,
		issueUrl:         fmt.Sprintf("%s%d", issueUrlBase, issue.Issue),
		finishedCallback: cb,
		g:                g,
		recent:           recent,
		retrieveRoll: func(ctx context.Context) (*gerrit.ChangeInfo, error) {
			return updateIssueFromGerrit(ctx, cfg, issue, g)
		},
		rollingTo: rollingTo,
	}, nil
}

// See documentation for RollImpl interface.
func (r *gerritRoll) InsertIntoDB(ctx context.Context) error {
	return r.recent.Add(ctx, r.issue)
}

// See documentation for state_machine.RollCLImpl interface.
func (r *gerritRoll) AddComment(ctx context.Context, msg string) error {
	return r.g.AddComment(ctx, r.ci, msg)
}

// Helper function for modifying a roll CL which might fail due to the CL being
// closed by a human or some other process, in which case we don't want to error
// out.
func (r *gerritRoll) withModify(ctx context.Context, action string, fn func() error) error {
	if err := fn(); err != nil {
		// It's possible that somebody abandoned the CL (or the CL
		// landed) while we were working. If that's the case, log an
		// error and move on.
		if err2 := r.Update(ctx); err2 != nil {
			return fmt.Errorf("Failed to %s with error:\n%s\nAnd failed to update it with error:\n%s", action, err, err2)
		}
		if r.ci.IsClosed() {
			sklog.Errorf("Attempted to %s but it is already closed! Error: %s", action, err)
			return nil
		}
		return err
	}
	return r.Update(ctx)
}

// See documentation for state_machine.RollCLImpl interface.
func (r *gerritRoll) Close(ctx context.Context, result, msg string) error {
	sklog.Infof("Closing issue %d (result %q) with message: %s", r.ci.Issue, result, msg)
	r.result = result
	return r.withModify(ctx, "close the CL", func() error {
		return r.g.Abandon(ctx, r.ci, msg)
	})
}

// See documentation for state_machine.RollCLImpl interface.
func (r *gerritRoll) IsClosed() bool {
	return r.issue.Closed
}

// See documentation for state_machine.RollCLImpl interface.
func (r *gerritRoll) IsFinished() bool {
	return r.issue.CqFinished
}

// See documentation for state_machine.RollCLImpl interface.
func (r *gerritRoll) IsSuccess() bool {
	return r.issue.CqSuccess
}

// See documentation for state_machine.RollCLImpl interface.
func (r *gerritRoll) IsDryRunFinished() bool {
	return r.issue.DryRunFinished
}

// See documentation for state_machine.RollCLImpl interface.
func (r *gerritRoll) IsDryRunSuccess() bool {
	return r.issue.DryRunSuccess
}

// See documentation for state_machine.RollCLImpl interface.
func (r *gerritRoll) RollingTo() *revision.Revision {
	return r.rollingTo
}

// See documentation for state_machine.RollCLImpl interface.
func (r *gerritRoll) SwitchToDryRun(ctx context.Context) error {
	return r.withModify(ctx, "switch the CL to dry run", func() error {
		if err := r.g.SendToDryRun(ctx, r.ci, "Mode was changed to dry run"); err != nil {
			return err
		}
		r.issue.IsDryRun = true
		return nil
	})
}

// See documentation for state_machine.RollCLImpl interface.
func (r *gerritRoll) SwitchToNormal(ctx context.Context) error {
	return r.withModify(ctx, "switch the CL out of dry run", func() error {
		if err := r.g.SendToCQ(ctx, r.ci, "Mode was changed to normal"); err != nil {
			return err
		}
		r.issue.IsDryRun = false
		return nil
	})
}

// See documentation for state_machine.RollCLImpl interface.
func (r *gerritRoll) RetryCQ(ctx context.Context) error {
	return r.withModify(ctx, "retry the CQ", func() error {
		if err := r.g.SendToCQ(ctx, r.ci, "CQ failed but there are no new commits. Retrying..."); err != nil {
			return err
		}
		r.issue.IsDryRun = false
		return nil
	})
}

// See documentation for state_machine.RollCLImpl interface.
func (r *gerritRoll) RetryDryRun(ctx context.Context) error {
	return r.withModify(ctx, "retry the CQ (dry run)", func() error {
		if err := r.g.SendToDryRun(ctx, r.ci, "Dry run failed but there are no new commits. Retrying..."); err != nil {
			return err
		}
		r.issue.IsDryRun = true
		return nil
	})
}

// See documentation for state_machine.RollCLImpl interface.
func (r *gerritRoll) Update(ctx context.Context) error {
	alreadyFinished := r.IsFinished()
	ci, err := r.retrieveRoll(ctx)
	if err != nil {
		return err
	}
	r.ci = ci
	if r.result != "" {
		r.issue.Result = r.result
	}
	if err := r.recent.Update(ctx, r.issue); err != nil {
		return err
	}
	if r.IsFinished() && !alreadyFinished && r.finishedCallback != nil {
		return r.finishedCallback(ctx, r)
	}
	return nil
}

// See documentation for state_machine.RollCLImpl interface.
func (r *gerritRoll) IssueID() string {
	return fmt.Sprintf("%d", r.issue.Issue)
}

// See documentation for state_machine.RollCLImpl interface.
func (r *gerritRoll) IssueURL() string {
	return r.issueUrl
}

// githubRoll is an implementation of RollImpl.
// TODO(rmistry): Add tests after a code-review abstraction later exists.
type githubRoll struct {
	finishedCallback func(context.Context, RollImpl) error
	checksNum        int
	checksWaitFor    []string
	g                *github.GitHub
	issue            *autoroll.AutoRollIssue
	issueUrl         string
	mergeMethodURL   string
	pullRequest      *github_api.PullRequest
	recent           *recent_rolls.RecentRolls
	result           string
	retrieveRoll     func(context.Context) (*github_api.PullRequest, error)
	rollingTo        *revision.Revision
	t                *travisci.TravisCI
}

// updateIssueFromGitHub loads details about the pull request from the GitHub
// API and updates the AutoRollIssue accordingly.
func updateIssueFromGitHub(ctx context.Context, a *autoroll.AutoRollIssue, g *github.GitHub, checksNum int, checksWaitFor []string, mergeMethodURL string) (*github_api.PullRequest, error) {

	// Retrieve the pull request from github.
	pullRequest, err := g.GetPullRequest(int(a.Issue))
	if err != nil {
		return nil, fmt.Errorf("Failed to get pull request for %d: %s", a.Issue, err)
	}

	checks, err := g.GetChecks(pullRequest.Head.GetSHA())
	if err != nil {
		return nil, err
	}
	tryResults := []*autoroll.TryResult{}
	for _, check := range checks {
		sklog.Infof("Looking at check %+v", check)
		if check.ID != 0 {
			testStatus := autoroll.TRYBOT_STATUS_STARTED
			testResult := ""
			switch check.State {
			case github.CHECK_STATE_PENDING:
				// Still pending.
			case github.CHECK_STATE_ERROR:
				// Fallthrough to the failure state below.
				fallthrough
			case github.CHECK_STATE_FAILURE:
				if util.In(check.Name, checksWaitFor) {
					sklog.Infof("%s has state %s. Waiting for it to succeed.", check.Name, github.CHECK_STATE_FAILURE)
				} else {
					testStatus = autoroll.TRYBOT_STATUS_COMPLETED
					testResult = autoroll.TRYBOT_RESULT_FAILURE
				}
			case github.CHECK_STATE_CANCELLED:
				testStatus = autoroll.TRYBOT_STATUS_COMPLETED
				testResult = autoroll.TRYBOT_RESULT_FAILURE
			case github.CHECK_STATE_TIMED_OUT:
				testStatus = autoroll.TRYBOT_STATUS_COMPLETED
				testResult = autoroll.TRYBOT_RESULT_FAILURE
			case github.CHECK_STATE_ACTION_REQUIRED:
				testStatus = autoroll.TRYBOT_STATUS_COMPLETED
				testResult = autoroll.TRYBOT_RESULT_FAILURE
			case github.CHECK_STATE_SUCCESS:
				testStatus = autoroll.TRYBOT_STATUS_COMPLETED
				testResult = autoroll.TRYBOT_RESULT_SUCCESS
			case github.CHECK_STATE_NEUTRAL:
				// Skipped tests show up as neutral so we can consider them successful.
				testStatus = autoroll.TRYBOT_STATUS_COMPLETED
				testResult = autoroll.TRYBOT_RESULT_SUCCESS
			}
			tryResult := &autoroll.TryResult{
				Builder:  fmt.Sprintf("%s #%d", check.Name, check.ID),
				Category: autoroll.TRYBOT_CATEGORY_CQ,
				Created:  check.StartedAt,
				Result:   testResult,
				Status:   testStatus,
			}
			if check.HTMLURL != "" {
				tryResult.Url = check.HTMLURL
			}
			tryResults = append(tryResults, tryResult)
		}
	}
	if len(tryResults) != checksNum {
		sklog.Warningf("len(tryResults) != checksNum: %d != %d", len(tryResults), checksNum)
		// Add fake try results so that we don't incorrectly mark the
		// roll as having succeeded all of the tryjobs.
		for i := len(tryResults); i < checksNum; i++ {
			tryResults = append(tryResults, &autoroll.TryResult{
				Builder:  fmt.Sprintf("Missing check #%d", i+1),
				Category: autoroll.TRYBOT_CATEGORY_CQ,
				Created:  time.Now(),
				Status:   autoroll.TRYBOT_STATUS_STARTED,
			})
		}
	}
	a.TryResults = tryResults

	if err := updateIssueFromGitHubPullRequest(a, pullRequest); err != nil {
		return nil, fmt.Errorf("Failed to convert issue format: %s", err)
	}

	// Entering any one of the below blocks modifies the PR. Keep track of if this happens.
	pullRequestModified := false
	if pullRequest.GetMergeableState() == github.MERGEABLE_STATE_DIRTY {
		// Add a comment and close the roll.
		if err := g.AddComment(int(a.Issue), "PullRequest is not longer mergeable. Closing it."); err != nil {
			return nil, fmt.Errorf("Could not add comment to %d: %s", a.Issue, err)
		}
		if _, err := g.ClosePullRequest(int(a.Issue)); err != nil {
			return nil, fmt.Errorf("Could not close %d: %s", a.Issue, err)
		}
		a.Result = autoroll.ROLL_RESULT_FAILURE
		pullRequestModified = true
	} else if len(a.TryResults) >= checksNum && a.AtleastOneTrybotFailure() && pullRequest.GetState() != github.CLOSED_STATE {
		// Atleast one trybot failed. Close the roll.
		linkToFailedJobs := []string{}
		for _, tryJob := range a.TryResults {
			if tryJob.Finished() && !tryJob.Succeeded() {
				linkToFailedJobs = append(linkToFailedJobs, tryJob.Url)
			}
		}
		failureComment := fmt.Sprintf("Trybots failed. These were the failed builds: %s", strings.Join(linkToFailedJobs, " , "))
		if err := g.AddComment(int(a.Issue), failureComment); err != nil {
			return nil, fmt.Errorf("Could not add comment to %d: %s", a.Issue, err)
		}
		if _, err := g.ClosePullRequest(int(a.Issue)); err != nil {
			return nil, fmt.Errorf("Could not close %d: %s", a.Issue, err)
		}
		pullRequestModified = true
	} else if !a.IsDryRun && len(a.TryResults) >= checksNum && a.AllTrybotsSucceeded() && pullRequest.GetState() != github.CLOSED_STATE && shouldStateBeMerged(pullRequest.GetMergeableState()) {
		// Github and travisci do not have a "commit queue". So changes must be
		// merged via the API after travisci successfully completes.
		if err := g.AddComment(int(a.Issue), "Auto-roller completed checks. About to merge."); err != nil {
			return nil, fmt.Errorf("Could not add comment to %d: %s", a.Issue, err)
		}
		// Get the PR's description and use as the commit message.
		desc, err := g.GetDescription(int(a.Issue))
		if err != nil {
			return nil, fmt.Errorf("Could not get description of %d: %s", a.Issue, err)
		}
		mergeMethod := github.MERGE_METHOD_SQUASH
		if mergeMethodURL != "" {
			client := httputils.NewTimeoutClient()
			resp, err := client.Get(mergeMethodURL)
			if err != nil {
				return nil, fmt.Errorf("Could not GET from %s: %s", mergeMethodURL, err)
			}
			defer util.Close(resp.Body)
			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				return nil, fmt.Errorf("Could not read response body: %s", err)
			}
			mergeMethod = strings.TrimRight(string(body), "\n")
		}
		if mergeMethod != github.MERGE_METHOD_SQUASH && mergeMethod != github.MERGE_METHOD_REBASE {
			return nil, fmt.Errorf("Unrecognized merge method: %s", mergeMethod)
		}
		if err := g.MergePullRequest(int(a.Issue), desc, mergeMethod); err != nil {
			return nil, fmt.Errorf("Could not merge pull request %d: %s", a.Issue, err)
		}
		pullRequestModified = true

		if len(tryResults) != checksNum {
			sklog.Errorf("The PR %d landed with different number of checks (%d) than what was expected (%d). Check to see if this number should be updated in the roller config.", a.Issue, len(tryResults), checksNum)
		}
	}

	if pullRequestModified {
		// Update Autoroll issue to show the current state of the PR.
		if err := updateIssueFromGitHubPullRequest(a, pullRequest); err != nil {
			return nil, fmt.Errorf("Failed to convert issue format: %s", err)
		}
	}

	return pullRequest, nil
}

// updateIssueFromGitHubPullRequest updates the AutoRollIssue instance based on the
// given PullRequest.
func updateIssueFromGitHubPullRequest(i *autoroll.AutoRollIssue, pullRequest *github_api.PullRequest) error {
	prNum := int64(pullRequest.GetNumber())
	if i.Issue != prNum {
		return fmt.Errorf("Pull request number %d differs from existing issue number %d!", prNum, i.Issue)
	}
	i.CqFinished = pullRequest.GetState() == github.CLOSED_STATE || pullRequest.GetMerged()
	i.CqSuccess = pullRequest.GetMerged()
	if i.IsDryRun {
		i.DryRunFinished = i.AllTrybotsFinished() || pullRequest.GetState() == github.CLOSED_STATE || pullRequest.GetMerged()
		i.DryRunSuccess = (i.DryRunFinished && i.AllTrybotsSucceeded()) || pullRequest.GetMerged()
	} else {
		i.DryRunFinished = false
		i.DryRunSuccess = false
	}

	ps := make([]int64, 0, *pullRequest.Commits)
	for i := 1; i <= *pullRequest.Commits; i++ {
		ps = append(ps, int64(i))
	}
	i.Closed = pullRequest.GetState() == github.CLOSED_STATE
	i.Committed = pullRequest.GetMerged()
	i.Created = pullRequest.GetCreatedAt()
	i.Modified = pullRequest.GetUpdatedAt()
	i.Patchsets = ps
	i.Subject = pullRequest.GetTitle()
	i.Result = autoroll.RollResult(i)
	// TODO(borenet): If this validation fails, it's likely that it will
	// continue to fail indefinitely, resulting in a stuck roller.
	// Additionally, this AutoRollIssue instance persists in the AutoRoller
	// for its entire lifetime; it's possible to partially fail to update
	// it and end up in an inconsistent state.
	return i.Validate()
}

func shouldStateBeMerged(mergeableState string) bool {
	// Allow "clean" and "unstable" mergeable state.
	// "unstable" is allowed (for now) because of a bug in Github where a race condition makes
	// Github believe that a completed Cirrus check is still pending. We verify that all checks
	// pass before we try to merge, so allowing "unstable" should not break anything. More
	// details are in http://skbug.com/8598
	return mergeableState == github.MERGEABLE_STATE_CLEAN || mergeableState == github.MERGEABLE_STATE_UNSTABLE
}

// newGithubRoll obtains a githubRoll instance from the given Gerrit issue number.
func newGithubRoll(ctx context.Context, issue *autoroll.AutoRollIssue, g *github.GitHub, recent *recent_rolls.RecentRolls, issueUrlBase string, config *GithubConfig, rollingTo *revision.Revision, cb func(context.Context, RollImpl) error) (RollImpl, error) {
	pullRequest, err := updateIssueFromGitHub(ctx, issue, g, config.ChecksNum, config.ChecksWaitFor, config.MergeMethodURL)
	if err != nil {
		return nil, err
	}
	return &githubRoll{
		checksNum:        config.ChecksNum,
		finishedCallback: cb,
		g:                g,
		issue:            issue,
		issueUrl:         fmt.Sprintf("%s%d", issueUrlBase, issue.Issue),
		mergeMethodURL:   config.MergeMethodURL,
		pullRequest:      pullRequest,
		recent:           recent,
		retrieveRoll: func(ctx context.Context) (*github_api.PullRequest, error) {
			return updateIssueFromGitHub(ctx, issue, g, config.ChecksNum, config.ChecksWaitFor, config.MergeMethodURL)
		},
		rollingTo: rollingTo,
	}, nil
}

// See documentation for state_machine.RollImpl interface.
func (r *githubRoll) InsertIntoDB(ctx context.Context) error {
	return r.recent.Add(ctx, r.issue)
}

// See documentation for state_machine.RollCLImpl interface.
func (r *githubRoll) AddComment(ctx context.Context, msg string) error {
	return r.g.AddComment(r.pullRequest.GetNumber(), msg)
}

// See documentation for state_machine.RollCLImpl interface.
func (r *githubRoll) Close(ctx context.Context, result, msg string) error {
	sklog.Infof("Closing pull request %d (result %q) with message: %s", r.pullRequest.GetNumber(), result, msg)
	r.result = result
	return r.withModify(ctx, "close the pull request", func() error {
		if err := r.g.AddComment(r.pullRequest.GetNumber(), msg); err != nil {
			return err
		}
		_, err := r.g.ClosePullRequest(r.pullRequest.GetNumber())
		return err
	})
}

// Helper function for modifying a roll CL which might fail due to the CL being
// closed by a human or some other process, in which case we don't want to error
// out.
func (r *githubRoll) withModify(ctx context.Context, action string, fn func() error) error {
	if err := fn(); err != nil {
		// It's possible that somebody abandoned the CL (or the CL
		// landed) while we were working. If that's the case, log an
		// error and move on.
		if err2 := r.Update(ctx); err2 != nil {
			return fmt.Errorf("Failed to %s with error:\n%s\nAnd failed to update it with error:\n%s", action, err, err2)
		}
		if r.pullRequest.GetState() == github.CLOSED_STATE {
			sklog.Errorf("Attempted to %s but it is already closed! Error: %s", action, err)
			return nil
		}
		return err
	}
	return r.Update(ctx)
}

// See documentation for state_machine.RollCLImpl interface.
func (r *githubRoll) Update(ctx context.Context) error {
	alreadyFinished := r.IsFinished()
	pullRequest, err := r.retrieveRoll(ctx)
	if err != nil {
		return err
	}
	r.pullRequest = pullRequest
	if r.result != "" {
		r.issue.Result = r.result
	}
	if err := r.recent.Update(ctx, r.issue); err != nil {
		return err
	}
	if r.IsFinished() && !alreadyFinished && r.finishedCallback != nil {
		return r.finishedCallback(ctx, r)
	}
	return nil
}

// See documentation for state_machine.RollCLImpl interface.
func (r *githubRoll) IsClosed() bool {
	return r.issue.Closed
}

// See documentation for state_machine.RollCLImpl interface.
func (r *githubRoll) IsFinished() bool {
	return r.issue.CqFinished
}

// See documentation for state_machine.RollCLImpl interface.
func (r *githubRoll) IsSuccess() bool {
	return r.issue.CqSuccess
}

// See documentation for state_machine.RollCLImpl interface.
func (r *githubRoll) IsDryRunFinished() bool {
	return r.issue.DryRunFinished
}

// See documentation for state_machine.RollCLImpl interface.
func (r *githubRoll) IsDryRunSuccess() bool {
	return r.issue.DryRunSuccess
}

// See documentation for state_machine.RollCLImpl interface.
func (r *githubRoll) RollingTo() *revision.Revision {
	return r.rollingTo
}

// See documentation for state_machine.RollCLImpl interface.
func (r *githubRoll) SwitchToDryRun(ctx context.Context) error {
	return r.withModify(ctx, "switch the CL to dry run", func() error {
		if err := r.g.ReplaceLabel(r.pullRequest.GetNumber(), github.COMMIT_LABEL, github.DRYRUN_LABEL); err != nil {
			return err
		}
		r.issue.IsDryRun = true
		return nil
	})
}

// See documentation for state_machine.RollCLImpl interface.
func (r *githubRoll) SwitchToNormal(ctx context.Context) error {
	return r.withModify(ctx, "switch the CL out of dry run", func() error {
		if err := r.g.ReplaceLabel(r.pullRequest.GetNumber(), github.DRYRUN_LABEL, github.COMMIT_LABEL); err != nil {
			return err
		}
		r.issue.IsDryRun = false
		return nil
	})
}

// See documentation for state_machine.RollCLImpl interface.
func (r *githubRoll) RetryCQ(ctx context.Context) error {
	// TODO(rmistry): Is there a way to retrigger travisci? if there is then
	// do we want to?
	return nil
}

// See documentation for state_machine.RollCLImpl interface.
func (r *githubRoll) RetryDryRun(ctx context.Context) error {
	// TODO(rmistry): Is there a way to retrigger travisci? if there is then
	// do we want to?
	return nil
}

// See documentation for state_machine.RollCLImpl interface.
func (r *githubRoll) IssueID() string {
	return fmt.Sprintf("%d", r.issue.Issue)
}

// See documentation for state_machine.RollCLImpl interface.
func (r *githubRoll) IssueURL() string {
	return r.issueUrl
}
