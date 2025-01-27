package repo_manager

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.skia.org/infra/go/gerrit"
	"go.skia.org/infra/go/git"
	git_testutils "go.skia.org/infra/go/git/testutils"
	gitiles_testutils "go.skia.org/infra/go/gitiles/testutils"
	"go.skia.org/infra/go/mockhttpclient"
	"go.skia.org/infra/go/recipe_cfg"
	"go.skia.org/infra/go/testutils"
	"go.skia.org/infra/go/testutils/unittest"
)

func setupNoCheckout(t *testing.T, cfg *NoCheckoutDEPSRepoManagerConfig, gerritCfg *gerrit.Config) (context.Context, string, *noCheckoutDEPSRepoManager, *git_testutils.GitBuilder, *git_testutils.GitBuilder, *gitiles_testutils.MockRepo, *gitiles_testutils.MockRepo, []string, *mockhttpclient.URLMock, func()) {
	unittest.LargeTest(t)

	wd, err := ioutil.TempDir("", "")
	require.NoError(t, err)

	// Create child and parent repos.
	child := git_testutils.GitInit(t, context.Background())
	child.Add(context.Background(), "DEPS", `deps = {
  "child/dep": "grandchild@def4560000def4560000def4560000def4560000",
}`)
	child.Commit(context.Background())
	f := "somefile.txt"
	childCommits := make([]string, 0, 10)
	for i := 0; i < numChildCommits; i++ {
		childCommits = append(childCommits, child.CommitGen(context.Background(), f))
	}

	urlmock := mockhttpclient.NewURLMock()

	mockChild := gitiles_testutils.NewMockRepo(t, child.RepoUrl(), git.GitDir(child.Dir()), urlmock)

	parent := git_testutils.GitInit(t, context.Background())
	parent.Add(context.Background(), "DEPS", fmt.Sprintf(`deps = {
  "%s": "%s@%s",
  "parent/dep": "grandchild@abc1230000abc1230000abc1230000abc1230000",
}`, childPath, child.RepoUrl(), childCommits[0]))
	parent.Commit(context.Background())

	mockParent := gitiles_testutils.NewMockRepo(t, parent.RepoUrl(), git.GitDir(parent.Dir()), urlmock)

	ctx := context.Background()

	gUrl := "https://fake-skia-review.googlesource.com"
	gitcookies := path.Join(wd, "gitcookies_fake")
	require.NoError(t, ioutil.WriteFile(gitcookies, []byte(".googlesource.com\tTRUE\t/\tTRUE\t123\to\tgit-user.google.com=abc123"), os.ModePerm))
	serialized, err := json.Marshal(&gerrit.AccountDetails{
		AccountId: 101,
		Name:      mockUser,
		Email:     mockUser,
		UserName:  mockUser,
	})
	require.NoError(t, err)
	serialized = append([]byte("abcd\n"), serialized...)
	urlmock.MockOnce(gUrl+"/a/accounts/self/detail", mockhttpclient.MockGetDialogue(serialized))
	g, err := gerrit.NewGerritWithConfig(gerritCfg, gUrl, gitcookies, urlmock.Client())
	require.NoError(t, err)

	cfg.ChildRepo = child.RepoUrl()
	cfg.ParentRepo = parent.RepoUrl()
	recipesCfg := filepath.Join(testutils.GetRepoRoot(t), recipe_cfg.RECIPE_CFG_PATH)

	mockParent.MockGetCommit(ctx, "master")
	parentMaster, err := git.GitDir(parent.Dir()).RevParse(ctx, "HEAD")
	require.NoError(t, err)
	mockParent.MockReadFile(ctx, "DEPS", parentMaster)
	mockChild.MockGetCommit(ctx, childCommits[0])
	mockChild.MockGetCommit(ctx, "master")
	mockChild.MockLog(ctx, git.LogFromTo(childCommits[0], childCommits[len(childCommits)-1]))

	rm, err := NewNoCheckoutDEPSRepoManager(ctx, cfg, wd, g, recipesCfg, "fake.server.com", urlmock.Client(), gerritCR(t, g), false)
	require.NoError(t, err)

	if len(cfg.TransitiveDeps) > 0 {
		mockChild.MockReadFile(ctx, "DEPS", childCommits[len(childCommits)-1])
		for _, hash := range childCommits {
			mockChild.MockReadFile(ctx, "DEPS", hash)
		}
	}

	_, _, _, err = rm.Update(ctx)
	require.NoError(t, err)

	cleanup := func() {
		testutils.RemoveAll(t, wd)
		child.Cleanup()
		parent.Cleanup()
		require.True(t, urlmock.Empty(), strings.Join(urlmock.List(), "\n"))
	}
	return ctx, wd, rm.(*noCheckoutDEPSRepoManager), child, parent, mockChild, mockParent, childCommits, urlmock, cleanup
}

func noCheckoutDEPSCfg() *NoCheckoutDEPSRepoManagerConfig {
	return &NoCheckoutDEPSRepoManagerConfig{
		NoCheckoutRepoManagerConfig: NoCheckoutRepoManagerConfig{
			CommonRepoManagerConfig: CommonRepoManagerConfig{
				ChildBranch:  "master",
				ChildPath:    childPath,
				IncludeLog:   true,
				ParentBranch: "master",
			},
		},
	}
}

func TestNoCheckoutDEPSRepoManagerUpdate(t *testing.T) {
	cfg := noCheckoutDEPSCfg()
	ctx, _, rm, _, parentRepo, mockChild, mockParent, childCommits, _, cleanup := setupNoCheckout(t, cfg, gerrit.CONFIG_CHROMIUM)
	defer cleanup()

	mockParent.MockGetCommit(ctx, "master")
	parentMaster, err := git.GitDir(parentRepo.Dir()).RevParse(ctx, "HEAD")
	require.NoError(t, err)
	mockParent.MockReadFile(ctx, "DEPS", parentMaster)
	mockChild.MockGetCommit(ctx, childCommits[0])
	mockChild.MockGetCommit(ctx, "master")
	mockChild.MockLog(ctx, git.LogFromTo(childCommits[0], childCommits[len(childCommits)-1]))
	lastRollRev, tipRev, notRolledRevs, err := rm.Update(ctx)
	require.NoError(t, err)
	require.Equal(t, childCommits[0], lastRollRev.Id)
	require.Equal(t, childCommits[len(childCommits)-1], tipRev.Id)
	require.Equal(t, len(notRolledRevs), len(childCommits)-1)
}

func testNoCheckoutDEPSRepoManagerCreateNewRoll(t *testing.T, gerritCfg *gerrit.Config) {
	cfg := noCheckoutDEPSCfg()
	ctx, _, rm, childRepo, parentRepo, mockChild, mockParent, childCommits, urlmock, cleanup := setupNoCheckout(t, cfg, gerritCfg)
	defer cleanup()

	mockParent.MockGetCommit(ctx, "master")
	parentMaster, err := git.GitDir(parentRepo.Dir()).RevParse(ctx, "HEAD")
	require.NoError(t, err)
	mockParent.MockReadFile(ctx, "DEPS", parentMaster)
	mockChild.MockGetCommit(ctx, childCommits[0])
	mockChild.MockGetCommit(ctx, "master")
	mockChild.MockLog(ctx, git.LogFromTo(childCommits[0], childCommits[len(childCommits)-1]))
	lastRollRev, tipRev, notRolledRevs, err := rm.Update(ctx)
	require.NoError(t, err)
	require.Equal(t, childCommits[0], lastRollRev.Id)
	require.Equal(t, childCommits[len(childCommits)-1], tipRev.Id)

	// Mock the request to retrieve the DEPS file.
	mockParent.MockReadFile(ctx, "DEPS", parentMaster)

	// Mock the initial change creation.
	logStr := ""
	childGitRepo := git.GitDir(childRepo.Dir())
	for _, c := range notRolledRevs {
		details, err := childGitRepo.Details(ctx, c.Id)
		require.NoError(t, err)
		ts := details.Timestamp.Format("2006-01-02")
		author := details.Author
		authorSplit := strings.Split(details.Author, "(")
		if len(authorSplit) > 1 {
			author = strings.TrimRight(strings.TrimSpace(authorSplit[1]), ")")
		}
		logStr += fmt.Sprintf("%s %s %s\n", ts, author, details.Subject)
	}
	commitMsg := fmt.Sprintf(`Roll %s %s..%s (%d commits)

%s/+log/%s..%s

git log %s..%s --date=short --first-parent --format='%%ad %%ae %%s'
%s
Created with:
  gclient setdep -r %s@%s

If this roll has caused a breakage, revert this CL and stop the roller
using the controls here:
fake.server.com
Please CC me@google.com on the revert to ensure that a human
is aware of the problem.

To report a problem with the AutoRoller itself, please file a bug:
https://bugs.chromium.org/p/skia/issues/entry?template=Autoroller+Bug

Documentation for the AutoRoller is here:
https://skia.googlesource.com/buildbot/+/master/autoroll/README.md

Bug: None
Tbr: me@google.com`, childPath, lastRollRev.Id[:12], tipRev.Id[:12], len(notRolledRevs), childRepo.RepoUrl(), lastRollRev.Id[:12], tipRev.Id[:12], lastRollRev.Id[:12], tipRev.Id[:12], logStr, childPath, tipRev.Id[:12])
	subject := strings.Split(commitMsg, "\n")[0]
	reqBody := []byte(fmt.Sprintf(`{"project":"%s","subject":"%s","branch":"%s","topic":"","status":"NEW","base_commit":"%s"}`, rm.gerritConfig.Project, subject, cfg.ParentBranch, parentMaster))
	ci := gerrit.ChangeInfo{
		ChangeId: "123",
		Id:       "123",
		Issue:    123,
		Revisions: map[string]*gerrit.Revision{
			"ps1": {
				ID:     "ps1",
				Number: 1,
			},
		},
		WorkInProgress: true,
	}
	respBody, err := json.Marshal(ci)
	require.NoError(t, err)
	respBody = append([]byte(")]}'\n"), respBody...)
	urlmock.MockOnce("https://fake-skia-review.googlesource.com/a/changes/", mockhttpclient.MockPostDialogueWithResponseCode("application/json", reqBody, respBody, 201))

	// Mock the edit of the change to update the commit message.
	reqBody = []byte(fmt.Sprintf(`{"message":"%s"}`, strings.Replace(commitMsg, "\n", "\\n", -1)))
	urlmock.MockOnce("https://fake-skia-review.googlesource.com/a/changes/123/edit:message", mockhttpclient.MockPutDialogue("application/json", reqBody, []byte("")))

	// Mock the request to modify the DEPS file.
	reqBody = []byte(fmt.Sprintf(`deps = {
  "%s": "%s@%s",
  "parent/dep": "grandchild@abc1230000abc1230000abc1230000abc1230000",
}`, childPath, childRepo.RepoUrl(), tipRev.Id))
	urlmock.MockOnce("https://fake-skia-review.googlesource.com/a/changes/123/edit/DEPS", mockhttpclient.MockPutDialogue("", reqBody, []byte("")))

	// Mock the request to publish the change edit.
	reqBody = []byte(`{"notify":"ALL"}`)
	urlmock.MockOnce("https://fake-skia-review.googlesource.com/a/changes/123/edit:publish", mockhttpclient.MockPostDialogue("application/json", reqBody, []byte("")))

	// Mock the request to load the updated change.
	respBody, err = json.Marshal(ci)
	require.NoError(t, err)
	respBody = append([]byte(")]}'\n"), respBody...)
	urlmock.MockOnce("https://fake-skia-review.googlesource.com/a/changes/123/detail?o=ALL_REVISIONS", mockhttpclient.MockGetDialogue(respBody))

	// Mock the request to set the change as read for review. This is only
	// done if ChangeInfo.WorkInProgress is true.
	reqBody = []byte(`{}`)
	urlmock.MockOnce("https://fake-skia-review.googlesource.com/a/changes/123/ready", mockhttpclient.MockPostDialogue("application/json", reqBody, []byte("")))

	// Mock the request to set the CQ.
	if gerritCfg.HasCq {
		reqBody = []byte(`{"labels":{"Code-Review":1,"Commit-Queue":2},"message":"","reviewers":[{"reviewer":"me@google.com"}]}`)
	} else {
		reqBody = []byte(`{"labels":{"Code-Review":1},"message":"","reviewers":[{"reviewer":"me@google.com"}]}`)
	}
	urlmock.MockOnce("https://fake-skia-review.googlesource.com/a/changes/123/revisions/ps1/review", mockhttpclient.MockPostDialogue("application/json", reqBody, []byte("")))
	if !gerritCfg.HasCq {
		urlmock.MockOnce("https://fake-skia-review.googlesource.com/a/changes/123/submit", mockhttpclient.MockPostDialogue("application/json", []byte("{}"), []byte("")))
	}

	issue, err := rm.CreateNewRoll(ctx, lastRollRev, tipRev, notRolledRevs, []string{"me@google.com"}, "", false)
	require.NoError(t, err)
	require.NotEqual(t, 0, issue)
}

func TestNoCheckoutDEPSRepoManagerCreateNewRoll(t *testing.T) {
	testNoCheckoutDEPSRepoManagerCreateNewRoll(t, gerrit.CONFIG_CHROMIUM)
}

func TestNoCheckoutDEPSRepoManagerCreateNewRollNoCQ(t *testing.T) {
	testNoCheckoutDEPSRepoManagerCreateNewRoll(t, gerrit.CONFIG_CHROMIUM_NO_CQ)
}

func TestNoCheckoutDEPSRepoManagerCreateNewRollTransitive(t *testing.T) {
	cfg := noCheckoutDEPSCfg()
	cfg.TransitiveDeps = map[string]string{
		"child/dep": "parent/dep",
	}
	ctx, _, rm, childRepo, parentRepo, mockChild, mockParent, childCommits, urlmock, cleanup := setupNoCheckout(t, cfg, gerrit.CONFIG_CHROMIUM)
	defer cleanup()

	mockParent.MockGetCommit(ctx, "master")
	parentMaster, err := git.GitDir(parentRepo.Dir()).RevParse(ctx, "HEAD")
	require.NoError(t, err)
	mockParent.MockReadFile(ctx, "DEPS", parentMaster)
	mockChild.MockGetCommit(ctx, childCommits[0])
	mockChild.MockGetCommit(ctx, "master")
	mockChild.MockLog(ctx, git.LogFromTo(childCommits[0], childCommits[len(childCommits)-1]))
	mockChild.MockReadFile(ctx, "DEPS", childCommits[len(childCommits)-1])
	for _, hash := range childCommits {
		mockChild.MockReadFile(ctx, "DEPS", hash)
	}
	lastRollRev, tipRev, notRolledRevs, err := rm.Update(ctx)
	require.NoError(t, err)
	require.Equal(t, childCommits[0], lastRollRev.Id)
	require.Equal(t, childCommits[len(childCommits)-1], tipRev.Id)

	// Mock the request to retrieve the DEPS file.
	mockParent.MockReadFile(ctx, "DEPS", parentMaster)

	// Mock the initial change creation.
	logStr := ""
	childGitRepo := git.GitDir(childRepo.Dir())
	for _, c := range notRolledRevs {
		details, err := childGitRepo.Details(ctx, c.Id)
		require.NoError(t, err)
		ts := details.Timestamp.Format("2006-01-02")
		author := details.Author
		authorSplit := strings.Split(details.Author, "(")
		if len(authorSplit) > 1 {
			author = strings.TrimRight(strings.TrimSpace(authorSplit[1]), ")")
		}
		logStr += fmt.Sprintf("%s %s %s\n", ts, author, details.Subject)
	}
	commitMsg := fmt.Sprintf(`Roll %s %s..%s (%d commits)

%s/+log/%s..%s

git log %s..%s --date=short --first-parent --format='%%ad %%ae %%s'
%s
Also rolling transitive DEPS:
  parent/dep abc1230000ab..def4560000de

Created with:
  gclient setdep -r %s@%s

If this roll has caused a breakage, revert this CL and stop the roller
using the controls here:
fake.server.com
Please CC me@google.com on the revert to ensure that a human
is aware of the problem.

To report a problem with the AutoRoller itself, please file a bug:
https://bugs.chromium.org/p/skia/issues/entry?template=Autoroller+Bug

Documentation for the AutoRoller is here:
https://skia.googlesource.com/buildbot/+/master/autoroll/README.md

Bug: None
Tbr: me@google.com`, childPath, lastRollRev.Id[:12], tipRev.Id[:12], len(notRolledRevs), childRepo.RepoUrl(), lastRollRev.Id[:12], tipRev.Id[:12], lastRollRev.Id[:12], tipRev.Id[:12], logStr, childPath, tipRev.Id[:12])
	subject := strings.Split(commitMsg, "\n")[0]
	reqBody := []byte(fmt.Sprintf(`{"project":"%s","subject":"%s","branch":"%s","topic":"","status":"NEW","base_commit":"%s"}`, rm.gerritConfig.Project, subject, cfg.ParentBranch, parentMaster))
	ci := gerrit.ChangeInfo{
		ChangeId: "123",
		Id:       "123",
		Issue:    123,
		Revisions: map[string]*gerrit.Revision{
			"ps1": {
				ID:     "ps1",
				Number: 1,
			},
		},
		WorkInProgress: true,
	}
	respBody, err := json.Marshal(ci)
	require.NoError(t, err)
	respBody = append([]byte(")]}'\n"), respBody...)
	urlmock.MockOnce("https://fake-skia-review.googlesource.com/a/changes/", mockhttpclient.MockPostDialogueWithResponseCode("application/json", reqBody, respBody, 201))

	// Mock the edit of the change to update the commit message.
	reqBody = []byte(fmt.Sprintf(`{"message":"%s"}`, strings.Replace(commitMsg, "\n", "\\n", -1)))
	urlmock.MockOnce("https://fake-skia-review.googlesource.com/a/changes/123/edit:message", mockhttpclient.MockPutDialogue("application/json", reqBody, []byte("")))

	// Mock the request to modify the DEPS file.
	reqBody = []byte(fmt.Sprintf(`deps = {
  "%s": "%s@%s",
  "parent/dep": "grandchild@def4560000def4560000def4560000def4560000",
}`, childPath, childRepo.RepoUrl(), tipRev.Id))
	urlmock.MockOnce("https://fake-skia-review.googlesource.com/a/changes/123/edit/DEPS", mockhttpclient.MockPutDialogue("", reqBody, []byte("")))

	// Mock the request to publish the change edit.
	reqBody = []byte(`{"notify":"ALL"}`)
	urlmock.MockOnce("https://fake-skia-review.googlesource.com/a/changes/123/edit:publish", mockhttpclient.MockPostDialogue("application/json", reqBody, []byte("")))

	// Mock the request to load the updated change.
	respBody, err = json.Marshal(ci)
	require.NoError(t, err)
	respBody = append([]byte(")]}'\n"), respBody...)
	urlmock.MockOnce("https://fake-skia-review.googlesource.com/a/changes/123/detail?o=ALL_REVISIONS", mockhttpclient.MockGetDialogue(respBody))

	// Mock the request to set the change as read for review. This is only
	// done if ChangeInfo.WorkInProgress is true.
	reqBody = []byte(`{}`)
	urlmock.MockOnce("https://fake-skia-review.googlesource.com/a/changes/123/ready", mockhttpclient.MockPostDialogue("application/json", reqBody, []byte("")))

	// Mock the request to set the CQ.
	reqBody = []byte(`{"labels":{"Code-Review":1,"Commit-Queue":2},"message":"","reviewers":[{"reviewer":"me@google.com"}]}`)
	urlmock.MockOnce("https://fake-skia-review.googlesource.com/a/changes/123/revisions/ps1/review", mockhttpclient.MockPostDialogue("application/json", reqBody, []byte("")))

	issue, err := rm.CreateNewRoll(ctx, lastRollRev, tipRev, notRolledRevs, []string{"me@google.com"}, "", false)
	require.NoError(t, err)
	require.NotEqual(t, 0, issue)
}
