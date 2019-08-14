package repograph_test

import (
	"context"
	"io/ioutil"
	"testing"

	assert "github.com/stretchr/testify/require"
	"go.skia.org/infra/go/git/repograph"
	"go.skia.org/infra/go/git/repograph/shared_tests"
	git_testutils "go.skia.org/infra/go/git/testutils"
	"go.skia.org/infra/go/testutils"
	"go.skia.org/infra/go/testutils/unittest"
	"go.skia.org/infra/go/vcsinfo"
)

// localRepoRefresher is a RepoImplRefresher backed by a local git repo.
type localRepoRefresher struct{}

// No-op, since the repo held by Graph is updated by the Graph during Update.
func (u *localRepoRefresher) Refresh(...*vcsinfo.LongCommit) {}

// setupRepo performs common setup for git.Repo based Graphs.
func setupRepo(t *testing.T) (context.Context, *git_testutils.GitBuilder, *repograph.Graph, shared_tests.RepoImplRefresher, func()) {
	ctx, g, cleanup := shared_tests.CommonSetup(t)

	tmp, err := ioutil.TempDir("", "")
	assert.NoError(t, err)

	repo, err := repograph.NewLocalGraph(ctx, g.Dir(), tmp)
	assert.NoError(t, err)

	return ctx, g, repo, &localRepoRefresher{}, func() {
		testutils.RemoveAll(t, tmp)
		cleanup()
	}
}

func TestGraphWellFormedRepo(t *testing.T) {
	unittest.MediumTest(t)
	ctx, g, repo, ud, cleanup := setupRepo(t)
	defer cleanup()
	shared_tests.TestGraphWellFormed(t, ctx, g, repo, ud)
}

func TestRecurseRepo(t *testing.T) {
	unittest.MediumTest(t)
	ctx, g, repo, ud, cleanup := setupRepo(t)
	defer cleanup()
	shared_tests.TestRecurse(t, ctx, g, repo, ud)
}

func TestRecurseAllBranchesRepo(t *testing.T) {
	unittest.MediumTest(t)
	ctx, g, repo, ud, cleanup := setupRepo(t)
	defer cleanup()
	shared_tests.TestRecurseAllBranches(t, ctx, g, repo, ud)
}

func TestUpdateHistoryChangedRepo(t *testing.T) {
	unittest.MediumTest(t)
	ctx, g, repo, ud, cleanup := setupRepo(t)
	defer cleanup()
	shared_tests.TestUpdateHistoryChanged(t, ctx, g, repo, ud)
}

func TestUpdateAndReturnCommitDiffsRepo(t *testing.T) {
	unittest.MediumTest(t)
	ctx, g, repo, ud, cleanup := setupRepo(t)
	defer cleanup()
	shared_tests.TestUpdateAndReturnCommitDiffs(t, ctx, g, repo, ud)
}

func TestRevListRepo(t *testing.T) {
	unittest.MediumTest(t)
	ctx, g, repo, ud, cleanup := setupRepo(t)
	defer cleanup()
	shared_tests.TestRevList(t, ctx, g, repo, ud)
}