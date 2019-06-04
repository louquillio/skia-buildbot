package repo_manager

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	assert "github.com/stretchr/testify/require"
	"go.chromium.org/luci/cipd/client/cipd"
	"go.chromium.org/luci/cipd/common"
	"go.skia.org/infra/autoroll/go/strategy"
	"go.skia.org/infra/go/cipd/mocks"
	"go.skia.org/infra/go/exec"
	git_testutils "go.skia.org/infra/go/git/testutils"
	"go.skia.org/infra/go/recipe_cfg"
	"go.skia.org/infra/go/testutils"
	"go.skia.org/infra/go/testutils/unittest"
)

const (
	GITHUB_CIPD_DEPS_CHILD_PATH = "path/to/child"
	GITHUB_CIPD_ASSET_NAME      = "test/cipd/name"
	GITHUB_CIPD_ASSET_TAG       = "latest"

	LAST_ROLLED  = "xyz12345"
	NOT_ROLLED_1 = "abc12345"
	NOT_ROLLED_2 = "def12345"
)

func githubCipdDEPSRmCfg() *GithubCipdDEPSRepoManagerConfig {
	return &GithubCipdDEPSRepoManagerConfig{
		GithubDEPSRepoManagerConfig: GithubDEPSRepoManagerConfig{
			DepotToolsRepoManagerConfig: DepotToolsRepoManagerConfig{
				CommonRepoManagerConfig: CommonRepoManagerConfig{
					ChildBranch:  "master",
					ChildPath:    GITHUB_CIPD_DEPS_CHILD_PATH,
					ParentBranch: "master",
				},
			},
		},
		CipdAssetName: GITHUB_CIPD_ASSET_NAME,
		CipdAssetTag:  "latest",
	}
}

func setupGithubCipdDEPS(t *testing.T) (context.Context, string, *git_testutils.GitBuilder, *exec.CommandCollector, func()) {
	wd, err := ioutil.TempDir("", "")
	assert.NoError(t, err)

	// Create child and parent repos.
	childPath := filepath.Join(wd, "github_repos", "earth")
	assert.NoError(t, os.MkdirAll(childPath, 0755))

	parent := git_testutils.GitInit(t, context.Background())
	parent.Add(context.Background(), "DEPS", fmt.Sprintf(`
deps = {
  "%s": {
    "packages": [
	  {
	    "package": "%s",
	    "version": "%s"
	  }
	],
  },
}`, GITHUB_CIPD_DEPS_CHILD_PATH, GITHUB_CIPD_ASSET_NAME, LAST_ROLLED))
	parent.Commit(context.Background())

	mockRun := &exec.CommandCollector{}
	mockRun.SetDelegateRun(func(cmd *exec.Command) error {
		if cmd.Name == "git" {
			if cmd.Args[0] == "clone" || cmd.Args[0] == "fetch" {
				return nil
			}
			if cmd.Args[0] == "checkout" && cmd.Args[1] == "remote/master" {
				// Pretend origin is the remote branch for testing ease.
				cmd.Args[1] = "origin/master"
			}
		}
		return exec.DefaultRun(cmd)
	})
	ctx := exec.NewContext(context.Background(), mockRun.Run)

	cleanup := func() {
		testutils.RemoveAll(t, wd)
		parent.Cleanup()
	}

	return ctx, wd, parent, mockRun, cleanup
}

type instanceEnumeratorImpl struct {
	done bool
}

func (e *instanceEnumeratorImpl) Next(ctx context.Context, limit int) ([]cipd.InstanceInfo, error) {
	if e.done {
		return nil, nil
	}
	instances := []cipd.InstanceInfo{}
	instance0 := cipd.InstanceInfo{
		Pin: common.Pin{
			PackageName: GITHUB_CIPD_ASSET_NAME,
			InstanceID:  LAST_ROLLED,
		},
		RegisteredBy: "aquaman@ocean.com",
	}
	instance1 := cipd.InstanceInfo{
		Pin: common.Pin{
			PackageName: GITHUB_CIPD_ASSET_NAME,
			InstanceID:  NOT_ROLLED_1,
		},
		RegisteredBy: "superman@krypton.com",
	}
	instance2 := cipd.InstanceInfo{
		Pin: common.Pin{
			PackageName: GITHUB_CIPD_ASSET_NAME,
			InstanceID:  NOT_ROLLED_2,
		},
		RegisteredBy: "batman@gotham.com",
	}
	instances = append(instances, instance0, instance1, instance2)
	e.done = true
	return instances, nil
}

func getCipdMock(ctx context.Context) *mocks.CIPDClient {
	cipdClient := &mocks.CIPDClient{}
	head := common.Pin{
		PackageName: "test/cipd/name",
		InstanceID:  NOT_ROLLED_1,
	}
	cipdClient.On("ResolveVersion", ctx, GITHUB_CIPD_ASSET_NAME, GITHUB_CIPD_ASSET_TAG).Return(head, nil).Once()
	cipdClient.On("ListInstances", ctx, GITHUB_CIPD_ASSET_NAME).Return(&instanceEnumeratorImpl{}, nil).Once()
	return cipdClient
}

// TestGithubRepoManager tests all aspects of the GithubRepoManager except for CreateNewRoll.
func TestGithubCipdDEPSRepoManager(t *testing.T) {
	unittest.LargeTest(t)

	ctx, wd, parent, _, cleanup := setupGithubCipdDEPS(t)
	defer cleanup()
	recipesCfg := filepath.Join(testutils.GetRepoRoot(t), recipe_cfg.RECIPE_CFG_PATH)

	g, _ := setupFakeGithub(t, nil)
	cfg := githubCipdDEPSRmCfg()
	cfg.ParentRepo = parent.RepoUrl()
	rm, err := NewGithubCipdDEPSRepoManager(ctx, cfg, wd, "test_roller_name", g, recipesCfg, "fake.server.com", nil, githubCR(t, g), false)
	assert.NoError(t, err)
	rm.(*githubCipdDEPSRepoManager).CipdClient = getCipdMock(ctx)
	assert.NoError(t, SetStrategy(ctx, rm, strategy.ROLL_STRATEGY_BATCH))
	assert.NoError(t, rm.Update(ctx))

	// Assert last roll, next roll and not rolled yet.
	assert.Equal(t, LAST_ROLLED, rm.LastRollRev())
	assert.Equal(t, NOT_ROLLED_1, rm.NextRollRev())
	assert.Equal(t, 2, len(rm.NotRolledRevisions()))
	assert.Equal(t, NOT_ROLLED_1, rm.NotRolledRevisions()[0].Id)
	assert.Equal(t, fmt.Sprintf("%s:%s", GITHUB_CIPD_ASSET_NAME, NOT_ROLLED_1), rm.NotRolledRevisions()[0].Display)
	assert.Equal(t, NOT_ROLLED_2, rm.NotRolledRevisions()[1].Id)
	assert.Equal(t, fmt.Sprintf("%s:%s", GITHUB_CIPD_ASSET_NAME, NOT_ROLLED_2), rm.NotRolledRevisions()[1].Display)
}

func TestCreateNewGithubCipdDEPSRoll(t *testing.T) {
	unittest.LargeTest(t)

	ctx, wd, parent, _, cleanup := setupGithubCipdDEPS(t)
	defer cleanup()
	recipesCfg := filepath.Join(testutils.GetRepoRoot(t), recipe_cfg.RECIPE_CFG_PATH)

	g, urlMock := setupFakeGithub(t, nil)
	cfg := githubCipdDEPSRmCfg()
	cfg.ParentRepo = parent.RepoUrl()
	rm, err := NewGithubCipdDEPSRepoManager(ctx, cfg, wd, "test_roller_name", g, recipesCfg, "fake.server.com", nil, githubCR(t, g), false)
	assert.NoError(t, err)
	rm.(*githubCipdDEPSRepoManager).CipdClient = getCipdMock(ctx)
	assert.NoError(t, SetStrategy(ctx, rm, strategy.ROLL_STRATEGY_BATCH))
	assert.NoError(t, rm.Update(ctx))

	// Create a roll.
	mockGithubRequests(t, urlMock, rm.LastRollRev(), rm.NextRollRev(), len(rm.NotRolledRevisions()))
	issue, err := rm.CreateNewRoll(ctx, rm.LastRollRev(), rm.NextRollRev(), githubEmails, cqExtraTrybots, false)
	assert.NoError(t, err)
	assert.Equal(t, issueNum, issue)
}

// Verify that we ran the PreUploadSteps.
func TestRanPreUploadStepsGithubCipdDEPS(t *testing.T) {
	unittest.LargeTest(t)

	ctx, wd, parent, _, cleanup := setupGithubCipdDEPS(t)
	defer cleanup()
	recipesCfg := filepath.Join(testutils.GetRepoRoot(t), recipe_cfg.RECIPE_CFG_PATH)

	g, urlMock := setupFakeGithub(t, nil)
	cfg := githubCipdDEPSRmCfg()
	cfg.ParentRepo = parent.RepoUrl()
	rm, err := NewGithubCipdDEPSRepoManager(ctx, cfg, wd, "test_roller_name", g, recipesCfg, "fake.server.com", nil, githubCR(t, g), false)
	assert.NoError(t, err)
	rm.(*githubCipdDEPSRepoManager).CipdClient = getCipdMock(ctx)
	assert.NoError(t, SetStrategy(ctx, rm, strategy.ROLL_STRATEGY_BATCH))
	assert.NoError(t, rm.Update(ctx))

	ran := false
	rm.(*githubCipdDEPSRepoManager).preUploadSteps = []PreUploadStep{
		func(context.Context, []string, *http.Client, string) error {
			ran = true
			return nil
		},
	}

	// Create a roll, assert that we ran the PreUploadSteps.
	mockGithubRequests(t, urlMock, rm.LastRollRev(), rm.NextRollRev(), len(rm.NotRolledRevisions()))
	_, createErr := rm.CreateNewRoll(ctx, rm.LastRollRev(), rm.NextRollRev(), githubEmails, cqExtraTrybots, false)
	assert.NoError(t, createErr)
	assert.True(t, ran)
}

// Verify that we fail when a PreUploadStep fails.
func TestErrorPreUploadStepsGithubCipdDEPS(t *testing.T) {
	unittest.LargeTest(t)

	ctx, wd, parent, _, cleanup := setupGithubCipdDEPS(t)
	defer cleanup()
	recipesCfg := filepath.Join(testutils.GetRepoRoot(t), recipe_cfg.RECIPE_CFG_PATH)

	g, urlMock := setupFakeGithub(t, nil)
	cfg := githubCipdDEPSRmCfg()
	cfg.ParentRepo = parent.RepoUrl()
	rm, err := NewGithubCipdDEPSRepoManager(ctx, cfg, wd, "test_roller_name", g, recipesCfg, "fake.server.com", nil, githubCR(t, g), false)
	assert.NoError(t, err)
	rm.(*githubCipdDEPSRepoManager).CipdClient = getCipdMock(ctx)
	assert.NoError(t, SetStrategy(ctx, rm, strategy.ROLL_STRATEGY_BATCH))
	assert.NoError(t, rm.Update(ctx))

	ran := false
	expectedErr := errors.New("Expected error")
	rm.(*githubCipdDEPSRepoManager).preUploadSteps = []PreUploadStep{
		func(context.Context, []string, *http.Client, string) error {
			ran = true
			return expectedErr
		},
	}

	// Create a roll, assert that we ran the PreUploadSteps.
	mockGithubRequests(t, urlMock, rm.LastRollRev(), rm.NextRollRev(), len(rm.NotRolledRevisions()))
	_, createErr := rm.CreateNewRoll(ctx, rm.LastRollRev(), rm.NextRollRev(), githubEmails, cqExtraTrybots, false)
	assert.Error(t, expectedErr, createErr)
	assert.True(t, ran)
}