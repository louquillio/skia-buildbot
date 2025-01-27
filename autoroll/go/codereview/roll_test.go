package codereview

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"testing"
	"time"

	"github.com/golang/protobuf/ptypes"
	github_api "github.com/google/go-github/github"
	"github.com/stretchr/testify/require"
	buildbucketpb "go.chromium.org/luci/buildbucket/proto"
	"go.skia.org/infra/autoroll/go/recent_rolls"
	"go.skia.org/infra/autoroll/go/revision"
	"go.skia.org/infra/go/autoroll"
	"go.skia.org/infra/go/deepequal"
	"go.skia.org/infra/go/ds"
	"go.skia.org/infra/go/ds/testutil"
	"go.skia.org/infra/go/gerrit"
	gerrit_testutils "go.skia.org/infra/go/gerrit/testutils"
	"go.skia.org/infra/go/github"
	"go.skia.org/infra/go/mockhttpclient"
	"go.skia.org/infra/go/testutils"
	"go.skia.org/infra/go/testutils/unittest"
)

func makeFakeRoll(t *testing.T, cfg *GerritConfig, issueNum int64, from, to string, dryRun bool) (*gerrit.ChangeInfo, *autoroll.AutoRollIssue) {
	// Gerrit API only has millisecond precision.
	now := time.Now().UTC().Round(time.Millisecond)
	description := fmt.Sprintf(`Roll src/third_party/skia/ %s..%s (42 commits).

blah blah
Tbr: some-sheriff
`, from[:12], to[:12])
	rev := &gerrit.Revision{
		ID:            "1",
		Number:        1,
		CreatedString: now.Format(gerrit.TIME_FORMAT),
		Created:       now,
	}
	roll := &gerrit.ChangeInfo{
		Created:       now,
		CreatedString: now.Format(gerrit.TIME_FORMAT),
		Subject:       description,
		ChangeId:      fmt.Sprintf("%d", issueNum),
		Issue:         issueNum,
		Labels:        map[string]*gerrit.LabelEntry{},
		Owner: &gerrit.Owner{
			Email: "fake-deps-roller@chromium.org",
		},
		Project: "skia",
		Revisions: map[string]*gerrit.Revision{
			"1": rev,
		},
		Patchsets:     []*gerrit.Revision{rev},
		Updated:       now,
		UpdatedString: now.Format(gerrit.TIME_FORMAT),
	}
	gc, err := cfg.GetConfig()
	require.NoError(t, err)
	cqLabels := gc.SetCqLabels
	if dryRun {
		cqLabels = gc.SetDryRunLabels
	}
	for k, v := range cqLabels {
		roll.Labels[k] = &gerrit.LabelEntry{
			All: []*gerrit.LabelDetail{{Value: v}},
		}
	}
	for k, v := range gc.SelfApproveLabels {
		roll.Labels[k] = &gerrit.LabelEntry{
			All: []*gerrit.LabelDetail{{Value: v}},
		}
	}
	return roll, &autoroll.AutoRollIssue{
		IsDryRun:    dryRun,
		Issue:       issueNum,
		RollingFrom: from,
		RollingTo:   to,
	}
}

func testGerritRoll(t *testing.T, cfg *GerritConfig) {
	unittest.LargeTest(t)

	tmp, err := ioutil.TempDir("", "")
	require.NoError(t, err)
	defer testutils.RemoveAll(t, tmp)

	testutil.InitDatastore(t, ds.KIND_AUTOROLL_ROLL)

	gc, err := cfg.GetConfig()
	require.NoError(t, err)
	g := gerrit_testutils.NewGerritWithConfig(t, gc, tmp)
	ctx := context.Background()
	recent, err := recent_rolls.NewRecentRolls(ctx, "test-roller")
	require.NoError(t, err)

	// Upload and retrieve the roll.
	from := "abcde12345abcde12345abcde12345abcde12345"
	to := "fghij67890fghij67890fghij67890fghij67890"
	toRev := &revision.Revision{
		Id:          to,
		Description: "rolling to fghi",
	}
	ci, issue := makeFakeRoll(t, cfg, 123, from, to, false)
	g.MockGetIssueProperties(ci)
	if cfg.CanQueryTrybots() {
		g.MockGetTrybotResults(ci, 1, nil)
	}
	gr, err := newGerritRoll(ctx, cfg, issue, g.Gerrit, recent, "http://issue/", toRev, nil)
	require.NoError(t, err)
	require.False(t, issue.IsDryRun)
	require.False(t, gr.IsFinished())
	require.False(t, gr.IsSuccess())
	require.False(t, gr.IsDryRunFinished())
	require.False(t, gr.IsDryRunSuccess())
	g.AssertEmpty()
	require.Equal(t, toRev, gr.RollingTo())

	// Insert into DB.
	current := recent.CurrentRoll()
	require.Nil(t, current)
	require.NoError(t, gr.InsertIntoDB(ctx))
	current = recent.CurrentRoll()
	require.NotNil(t, current)
	require.Equal(t, current.Issue, ci.Issue)
	g.AssertEmpty()

	// Add a comment.
	msg := "Here's a comment"
	g.MockAddComment(ci, msg)
	require.NoError(t, gr.AddComment(ctx, msg))
	g.AssertEmpty()
	require.False(t, issue.IsDryRun)
	require.False(t, gr.IsFinished())
	require.False(t, gr.IsSuccess())
	require.False(t, gr.IsDryRunFinished())
	require.False(t, gr.IsDryRunSuccess())

	// Set dry run.
	g.MockPost(ci, "Mode was changed to dry run", gc.SetDryRunLabels)
	gerrit.SetLabels(ci, gc.SetDryRunLabels)
	g.MockGetIssueProperties(ci)
	if cfg.CanQueryTrybots() {
		g.MockGetTrybotResults(ci, 1, nil)
	}
	require.NoError(t, gr.SwitchToDryRun(ctx))
	g.AssertEmpty()
	require.True(t, issue.IsDryRun)
	require.False(t, gr.IsFinished())
	require.False(t, gr.IsSuccess())
	require.False(t, gr.IsDryRunFinished())
	require.False(t, gr.IsDryRunSuccess())

	// Set normal.
	g.MockPost(ci, "Mode was changed to normal", gc.SetCqLabels)
	gerrit.SetLabels(ci, gc.SetCqLabels)
	g.MockGetIssueProperties(ci)
	if cfg.CanQueryTrybots() {
		g.MockGetTrybotResults(ci, 1, nil)
	}
	require.NoError(t, gr.SwitchToNormal(ctx))
	g.AssertEmpty()
	require.False(t, issue.IsDryRun)
	require.False(t, gr.IsFinished())
	require.False(t, gr.IsSuccess())
	require.False(t, gr.IsDryRunFinished())
	require.False(t, gr.IsDryRunSuccess())

	// Update.
	ci.Status = gerrit.CHANGE_STATUS_MERGED
	// Landing a change adds an empty patchset.
	rev := &gerrit.Revision{
		Number:  int64(len(ci.Revisions) + 1),
		Created: time.Now(),
		Kind:    "",
	}
	ci.Revisions[fmt.Sprintf("%d", rev.Number)] = rev
	ci.Patchsets = append(ci.Patchsets, rev)
	g.MockGetIssueProperties(ci)
	if cfg.CanQueryTrybots() {
		g.MockGetTrybotResults(ci, 1, nil)
	}
	require.NoError(t, gr.Update(ctx))
	require.False(t, issue.IsDryRun)
	require.True(t, gr.IsFinished())
	require.True(t, gr.IsSuccess())
	require.False(t, gr.IsDryRunFinished())
	require.False(t, gr.IsDryRunSuccess())
	require.Nil(t, recent.CurrentRoll())

	// Upload and retrieve another roll, dry run this time.
	ci, issue = makeFakeRoll(t, cfg, 124, from, to, true)
	g.MockGetIssueProperties(ci)
	var tryjob *buildbucketpb.Build
	if cfg.CanQueryTrybots() {
		ts, err := ptypes.TimestampProto(time.Now().UTC().Round(time.Millisecond))
		require.NoError(t, err)
		tryjob = &buildbucketpb.Build{
			Builder: &buildbucketpb.BuilderID{
				Project: "skia",
				Bucket:  "fake",
				Builder: "fake-builder",
			},
			Id:         99999,
			CreateTime: ts,
			Status:     buildbucketpb.Status_STARTED,
			Tags: []*buildbucketpb.StringPair{
				{
					Key:   "user_agent",
					Value: "cq",
				},
				{
					Key:   "cq_experimental",
					Value: "false",
				},
			},
		}
		g.MockGetTrybotResults(ci, 1, []*buildbucketpb.Build{tryjob})
	}
	gr, err = newGerritRoll(ctx, cfg, issue, g.Gerrit, recent, "http://issue/", toRev, nil)
	require.NoError(t, err)
	require.True(t, issue.IsDryRun)
	require.False(t, gr.IsFinished())
	require.False(t, gr.IsSuccess())
	require.False(t, gr.IsDryRunFinished())
	require.False(t, gr.IsDryRunSuccess())
	g.AssertEmpty()
	require.Equal(t, toRev, gr.RollingTo())

	// Insert into DB.
	current = recent.CurrentRoll()
	require.Nil(t, current)
	require.NoError(t, gr.InsertIntoDB(ctx))
	current = recent.CurrentRoll()
	require.NotNil(t, current)
	require.Equal(t, current.Issue, ci.Issue)
	g.AssertEmpty()

	// Success.
	if len(gc.DryRunSuccessLabels) > 0 {
		gerrit.SetLabels(ci, gc.DryRunSuccessLabels)
	}
	gerrit.UnsetLabels(ci, gc.DryRunActiveLabels)
	g.MockGetIssueProperties(ci)
	if cfg.CanQueryTrybots() {
		tryjob.Status = buildbucketpb.Status_SUCCESS
		g.MockGetTrybotResults(ci, 1, []*buildbucketpb.Build{tryjob})
	}
	require.NoError(t, gr.Update(ctx))
	require.True(t, issue.IsDryRun)
	require.False(t, gr.IsFinished())
	require.False(t, gr.IsSuccess())
	require.True(t, gr.IsDryRunFinished())
	require.True(t, gr.IsDryRunSuccess())
	g.AssertEmpty()

	// Close for cleanup.
	ci.Status = gerrit.CHANGE_STATUS_ABANDONED
	g.MockGetIssueProperties(ci)
	if cfg.CanQueryTrybots() {
		g.MockGetTrybotResults(ci, 1, []*buildbucketpb.Build{tryjob})
	}
	require.NoError(t, gr.Update(ctx))

	// Verify that all of the mutation functions handle a conflict (eg.
	// someone closed the CL) gracefully.

	// 1. SwitchToDryRun.
	ci, issue = makeFakeRoll(t, cfg, 125, from, to, false)
	g.MockGetIssueProperties(ci)
	if cfg.CanQueryTrybots() {
		g.MockGetTrybotResults(ci, 1, nil)
	}
	gr, err = newGerritRoll(ctx, cfg, issue, g.Gerrit, recent, "http://issue/", toRev, nil)
	require.NoError(t, err)
	require.NoError(t, gr.InsertIntoDB(ctx))
	url, reqBytes := g.MakePostRequest(ci, "Mode was changed to dry run", gc.SetDryRunLabels)
	g.Mock.MockOnce(url, mockhttpclient.MockPostError("application/json", reqBytes, "CONFLICT", http.StatusConflict))
	ci.Status = gerrit.CHANGE_STATUS_ABANDONED
	g.MockGetIssueProperties(ci)
	if cfg.CanQueryTrybots() {
		g.MockGetTrybotResults(ci, 1, nil)
	}
	require.NoError(t, gr.SwitchToDryRun(ctx))
	g.AssertEmpty()

	// 2. SwitchToNormal
	ci, issue = makeFakeRoll(t, cfg, 126, from, to, false)
	g.MockGetIssueProperties(ci)
	if cfg.CanQueryTrybots() {
		g.MockGetTrybotResults(ci, 1, nil)
	}
	gr, err = newGerritRoll(ctx, cfg, issue, g.Gerrit, recent, "http://issue/", toRev, nil)
	require.NoError(t, err)
	require.NoError(t, gr.InsertIntoDB(ctx))
	url, reqBytes = g.MakePostRequest(ci, "Mode was changed to normal", gc.SetCqLabels)
	g.Mock.MockOnce(url, mockhttpclient.MockPostError("application/json", reqBytes, "CONFLICT", http.StatusConflict))
	ci.Status = gerrit.CHANGE_STATUS_ABANDONED
	g.MockGetIssueProperties(ci)
	if cfg.CanQueryTrybots() {
		g.MockGetTrybotResults(ci, 1, nil)
	}
	require.NoError(t, gr.SwitchToNormal(ctx))
	g.AssertEmpty()

	// 3. Close.
	ci, issue = makeFakeRoll(t, cfg, 127, from, to, false)
	g.MockGetIssueProperties(ci)
	if cfg.CanQueryTrybots() {
		g.MockGetTrybotResults(ci, 1, nil)
	}
	gr, err = newGerritRoll(ctx, cfg, issue, g.Gerrit, recent, "http://issue/", toRev, nil)
	require.NoError(t, err)
	require.NoError(t, gr.InsertIntoDB(ctx))
	url = fmt.Sprintf("%s/a/changes/%d/abandon", gerrit_testutils.FAKE_GERRIT_URL, ci.Issue)
	req := testutils.MarshalJSON(t, &struct {
		Message string `json:"message"`
	}{
		Message: "close it!",
	})
	g.Mock.MockOnce(url, mockhttpclient.MockPostError("application/json", []byte(req), "CONFLICT", http.StatusConflict))
	ci.Status = gerrit.CHANGE_STATUS_ABANDONED
	g.MockGetIssueProperties(ci)
	if cfg.CanQueryTrybots() {
		g.MockGetTrybotResults(ci, 1, nil)
	}
	require.NoError(t, gr.Close(ctx, autoroll.ROLL_RESULT_FAILURE, "close it!"))
	g.AssertEmpty()

	// Verify that we set the correct status when abandoning a CL.
	ci, issue = makeFakeRoll(t, cfg, 128, from, to, false)
	g.MockGetIssueProperties(ci)
	if cfg.CanQueryTrybots() {
		g.MockGetTrybotResults(ci, 1, nil)
	}
	gr, err = newGerritRoll(ctx, cfg, issue, g.Gerrit, recent, "http://issue/", toRev, nil)
	require.NoError(t, err)
	require.NoError(t, gr.InsertIntoDB(ctx))
	url = fmt.Sprintf("%s/a/changes/%d/abandon", gerrit_testutils.FAKE_GERRIT_URL, ci.Issue)
	req = testutils.MarshalJSON(t, &struct {
		Message string `json:"message"`
	}{
		Message: "close it!",
	})
	g.Mock.MockOnce(url, mockhttpclient.MockPostDialogue("application/json", []byte(req), nil))
	ci.Status = gerrit.CHANGE_STATUS_ABANDONED
	g.MockGetIssueProperties(ci)
	if cfg.CanQueryTrybots() {
		g.MockGetTrybotResults(ci, 1, nil)
	}
	require.NoError(t, gr.Close(ctx, autoroll.ROLL_RESULT_DRY_RUN_SUCCESS, "close it!"))
	g.AssertEmpty()
	issue, err = recent.Get(ctx, 128)
	require.NoError(t, err)
	require.Equal(t, issue.Result, autoroll.ROLL_RESULT_DRY_RUN_SUCCESS)
}

func TestGerritRoll(t *testing.T) {
	testGerritRoll(t, &GerritConfig{
		URL:     "???",
		Project: "???",
		Config:  GERRIT_CONFIG_CHROMIUM,
	})
}

func TestGerritAndroidRoll(t *testing.T) {
	testGerritRoll(t, &GerritConfig{
		URL:     "???",
		Project: "???",
		Config:  GERRIT_CONFIG_ANDROID,
	})
}

func testUpdateFromGerritChangeInfo(t *testing.T, cfg *gerrit.Config) {
	unittest.SmallTest(t)

	now := time.Now()

	a := &autoroll.AutoRollIssue{
		Issue:       123,
		RollingFrom: "abc123",
		RollingTo:   "def456",
	}

	// Ensure that we don't overwrite the issue number.
	require.EqualError(t, updateIssueFromGerritChangeInfo(a, &gerrit.ChangeInfo{}, cfg), "CL ID 0 differs from existing issue number 123!")

	// Normal, in-progress CL.
	rev := &gerrit.Revision{
		ID:            "1",
		Number:        1,
		Created:       now,
		CreatedString: now.Format(gerrit.TIME_FORMAT),
	}
	ci := &gerrit.ChangeInfo{
		Created:       now,
		CreatedString: now.Format(gerrit.TIME_FORMAT),
		Subject:       "roll the deps",
		ChangeId:      fmt.Sprintf("%d", a.Issue),
		Issue:         a.Issue,
		Labels:        map[string]*gerrit.LabelEntry{},
		Owner: &gerrit.Owner{
			Email: "fake@chromium.org",
		},
		Project: "skia",
		Revisions: map[string]*gerrit.Revision{
			rev.ID: rev,
		},
		Patchsets:     []*gerrit.Revision{rev},
		Status:        gerrit.CHANGE_STATUS_NEW,
		Updated:       now,
		UpdatedString: now.Format(gerrit.TIME_FORMAT),
	}
	gerrit.SetLabels(ci, cfg.SelfApproveLabels)
	gerrit.SetLabels(ci, cfg.SetCqLabels)
	require.NoError(t, updateIssueFromGerritChangeInfo(a, ci, cfg))
	expect := &autoroll.AutoRollIssue{
		Created:     now,
		Issue:       123,
		Modified:    now,
		Patchsets:   []int64{1},
		Result:      autoroll.ROLL_RESULT_IN_PROGRESS,
		RollingFrom: "abc123",
		RollingTo:   "def456",
		Subject:     "roll the deps",
	}
	if !cfg.HasCq {
		expect.CqFinished = true
		expect.Result = autoroll.ROLL_RESULT_FAILURE
	}
	deepequal.AssertDeepEqual(t, expect, a)

	// CQ failed.
	if len(cfg.CqFailureLabels) > 0 {
		gerrit.SetLabels(ci, cfg.CqFailureLabels)
	}
	gerrit.UnsetLabels(ci, cfg.CqActiveLabels)
	expect.CqFinished = true
	expect.Result = autoroll.ROLL_RESULT_FAILURE
	require.NoError(t, updateIssueFromGerritChangeInfo(a, ci, cfg))
	deepequal.AssertDeepEqual(t, expect, a)

	// CQ succeeded.
	if len(cfg.CqFailureLabels) > 0 {
		gerrit.UnsetLabels(ci, cfg.CqFailureLabels)
	}
	if len(cfg.CqSuccessLabels) > 0 {
		gerrit.SetLabels(ci, cfg.CqSuccessLabels)
	} else {
		gerrit.UnsetLabels(ci, cfg.CqActiveLabels)
	}
	ci.Committed = true
	ci.Status = gerrit.CHANGE_STATUS_MERGED
	expect.Closed = true
	expect.Committed = true
	expect.CqSuccess = true
	expect.Result = autoroll.ROLL_RESULT_SUCCESS
	require.NoError(t, updateIssueFromGerritChangeInfo(a, ci, cfg))
	deepequal.AssertDeepEqual(t, expect, a)

	// CL was abandoned while CQ was running.
	if len(cfg.CqSuccessLabels) > 0 {
		gerrit.UnsetLabels(ci, cfg.CqSuccessLabels)
	} else {
		gerrit.SetLabels(ci, cfg.CqActiveLabels)
	}
	ci.Committed = false
	ci.Status = gerrit.CHANGE_STATUS_ABANDONED
	expect.Committed = false
	expect.CqFinished = true // Not really, but the CL is finished.
	expect.CqSuccess = false
	expect.Result = autoroll.ROLL_RESULT_FAILURE
	require.NoError(t, updateIssueFromGerritChangeInfo(a, ci, cfg))
	deepequal.AssertDeepEqual(t, expect, a)

	// Dry run active.
	ci.Status = gerrit.CHANGE_STATUS_NEW
	gerrit.UnsetLabels(ci, cfg.SetCqLabels)
	gerrit.SetLabels(ci, cfg.SetDryRunLabels)
	expect.Closed = false
	expect.CqFinished = false
	expect.IsDryRun = true
	expect.Result = autoroll.ROLL_RESULT_DRY_RUN_IN_PROGRESS
	if !cfg.HasCq {
		expect.DryRunFinished = true
		expect.DryRunSuccess = true
		expect.Result = autoroll.ROLL_RESULT_DRY_RUN_SUCCESS
	}
	a.IsDryRun = true
	require.NoError(t, updateIssueFromGerritChangeInfo(a, ci, cfg))
	deepequal.AssertDeepEqual(t, expect, a)

	// Dry run failed.
	if len(cfg.DryRunFailureLabels) > 0 {
		gerrit.SetLabels(ci, cfg.DryRunFailureLabels)
	}
	gerrit.UnsetLabels(ci, cfg.DryRunActiveLabels)
	expect.DryRunFinished = true
	expect.Result = autoroll.ROLL_RESULT_DRY_RUN_FAILURE
	expect.TryResults = []*autoroll.TryResult{
		{
			Builder:  "fake",
			Category: autoroll.TRYBOT_CATEGORY_CQ,
			Result:   autoroll.TRYBOT_RESULT_FAILURE,
			Status:   autoroll.TRYBOT_STATUS_COMPLETED,
		},
	}
	if !cfg.HasCq {
		expect.DryRunSuccess = true
		expect.Result = autoroll.ROLL_RESULT_DRY_RUN_SUCCESS
	}
	a.TryResults = expect.TryResults
	require.NoError(t, updateIssueFromGerritChangeInfo(a, ci, cfg))
	deepequal.AssertDeepEqual(t, expect, a)

	// The CL was abandoned while the dry run was running.
	expect.TryResults[0].Result = ""
	expect.TryResults[0].Status = autoroll.TRYBOT_STATUS_SCHEDULED
	ci.Status = gerrit.CHANGE_STATUS_ABANDONED
	expect.Closed = true
	expect.DryRunFinished = true
	expect.DryRunSuccess = false
	expect.Result = autoroll.ROLL_RESULT_DRY_RUN_FAILURE
	require.NoError(t, updateIssueFromGerritChangeInfo(a, ci, cfg))
	deepequal.AssertDeepEqual(t, expect, a)

	// The CL was landed while the dry run was running.
	ci.Committed = true
	ci.Status = gerrit.CHANGE_STATUS_MERGED
	expect.Committed = true
	expect.DryRunSuccess = true
	expect.Result = autoroll.ROLL_RESULT_DRY_RUN_SUCCESS
	require.NoError(t, updateIssueFromGerritChangeInfo(a, ci, cfg))
	deepequal.AssertDeepEqual(t, expect, a)

	// Dry run success.
	if len(cfg.DryRunSuccessLabels) > 0 {
		gerrit.SetLabels(ci, cfg.DryRunSuccessLabels)
	}
	ci.Committed = false
	ci.Status = gerrit.CHANGE_STATUS_NEW
	expect.Closed = false
	expect.Committed = false
	expect.CqFinished = false
	expect.CqSuccess = false
	expect.DryRunSuccess = true
	expect.Result = autoroll.ROLL_RESULT_DRY_RUN_SUCCESS
	expect.TryResults[0].Result = autoroll.TRYBOT_RESULT_SUCCESS
	expect.TryResults[0].Status = autoroll.TRYBOT_STATUS_COMPLETED
	require.NoError(t, updateIssueFromGerritChangeInfo(a, ci, cfg))
	deepequal.AssertDeepEqual(t, expect, a)
}

func TestUpdateFromGerritChangeInfoAndroid(t *testing.T) {
	testUpdateFromGerritChangeInfo(t, gerrit.CONFIG_ANDROID)
}

func TestUpdateFromGerritChangeInfoANGLE(t *testing.T) {
	testUpdateFromGerritChangeInfo(t, gerrit.CONFIG_ANGLE)
}

func TestUpdateFromGerritChangeInfoChromium(t *testing.T) {
	testUpdateFromGerritChangeInfo(t, gerrit.CONFIG_CHROMIUM)
}

func TestUpdateFromGerritChangeInfoChromiumNoCQ(t *testing.T) {
	testUpdateFromGerritChangeInfo(t, gerrit.CONFIG_CHROMIUM_NO_CQ)
}

func TestUpdateFromGitHubPullRequest(t *testing.T) {
	unittest.SmallTest(t)

	now := time.Now()

	intPtr := func(v int) *int {
		return &v
	}
	stringPtr := func(v string) *string {
		return &v
	}
	boolPtr := func(v bool) *bool {
		return &v
	}

	a := &autoroll.AutoRollIssue{
		Issue:       123,
		RollingFrom: "abc123",
		RollingTo:   "def456",
	}

	// Ensure that we don't overwrite the issue number.
	require.EqualError(t, updateIssueFromGitHubPullRequest(a, &github_api.PullRequest{}), "Pull request number 0 differs from existing issue number 123!")

	// Normal, in-progress CL.
	pr := &github_api.PullRequest{
		Number:    intPtr(int(a.Issue)),
		State:     stringPtr(""),
		Commits:   intPtr(1),
		Title:     stringPtr("roll the deps"),
		CreatedAt: &now,
		UpdatedAt: &now,
	}
	require.NoError(t, updateIssueFromGitHubPullRequest(a, pr))
	expect := &autoroll.AutoRollIssue{
		Created:     now,
		Issue:       123,
		Modified:    now,
		Patchsets:   []int64{1},
		Result:      autoroll.ROLL_RESULT_IN_PROGRESS,
		RollingFrom: "abc123",
		RollingTo:   "def456",
		Subject:     "roll the deps",
	}
	deepequal.AssertDeepEqual(t, expect, a)

	// CQ failed.
	pr.State = &github.CLOSED_STATE
	expect.Closed = true // if the CQ fails, we close the PR.
	expect.CqFinished = true
	expect.Result = autoroll.ROLL_RESULT_FAILURE
	require.NoError(t, updateIssueFromGitHubPullRequest(a, pr))
	deepequal.AssertDeepEqual(t, expect, a)

	// CQ succeeded.
	pr.Merged = boolPtr(true)
	expect.Closed = true
	expect.Committed = true
	expect.CqSuccess = true
	expect.Result = autoroll.ROLL_RESULT_SUCCESS
	require.NoError(t, updateIssueFromGitHubPullRequest(a, pr))
	deepequal.AssertDeepEqual(t, expect, a)

	// CL was abandoned while CQ was running.
	// (the above includes this case)

	// Dry run active.
	pr.Merged = boolPtr(false)
	pr.State = stringPtr("")
	expect.TryResults = []*autoroll.TryResult{
		{
			Builder:  "fake",
			Category: autoroll.TRYBOT_CATEGORY_CQ,
			Status:   autoroll.TRYBOT_STATUS_SCHEDULED,
		},
	}
	expect.Closed = false
	expect.Committed = false
	expect.CqFinished = false
	expect.CqSuccess = false
	expect.IsDryRun = true
	expect.Result = autoroll.ROLL_RESULT_DRY_RUN_IN_PROGRESS
	a.IsDryRun = true
	a.TryResults = expect.TryResults
	require.NoError(t, updateIssueFromGitHubPullRequest(a, pr))
	deepequal.AssertDeepEqual(t, expect, a)

	// Dry run failed.
	expect.DryRunFinished = true
	expect.Result = autoroll.ROLL_RESULT_DRY_RUN_FAILURE
	expect.TryResults[0].Result = autoroll.TRYBOT_RESULT_FAILURE
	expect.TryResults[0].Status = autoroll.TRYBOT_STATUS_COMPLETED
	a.TryResults = expect.TryResults
	require.NoError(t, updateIssueFromGitHubPullRequest(a, pr))
	deepequal.AssertDeepEqual(t, expect, a)

	// CL was abandoned while dry run was still running.
	expect.TryResults[0].Result = ""
	expect.TryResults[0].Status = autoroll.TRYBOT_STATUS_SCHEDULED
	pr.State = &github.CLOSED_STATE
	expect.Closed = true
	expect.CqFinished = true
	require.NoError(t, updateIssueFromGitHubPullRequest(a, pr))
	deepequal.AssertDeepEqual(t, expect, a)

	// CL was landed while dry run was still running.
	pr.Merged = boolPtr(true)
	expect.Committed = true
	expect.CqSuccess = true
	expect.DryRunSuccess = true
	expect.Result = autoroll.ROLL_RESULT_DRY_RUN_SUCCESS
	require.NoError(t, updateIssueFromGitHubPullRequest(a, pr))
	deepequal.AssertDeepEqual(t, expect, a)

	// Dry run success.
	pr.Merged = boolPtr(false)
	pr.State = stringPtr("")
	expect.Closed = false
	expect.Committed = false
	expect.CqFinished = false
	expect.CqSuccess = false
	expect.DryRunSuccess = true
	expect.Result = autoroll.ROLL_RESULT_DRY_RUN_SUCCESS
	expect.TryResults[0].Result = autoroll.TRYBOT_RESULT_SUCCESS
	expect.TryResults[0].Status = autoroll.TRYBOT_STATUS_COMPLETED
	require.NoError(t, updateIssueFromGitHubPullRequest(a, pr))
	deepequal.AssertDeepEqual(t, expect, a)
}
