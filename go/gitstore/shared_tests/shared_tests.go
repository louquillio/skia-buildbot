package shared_tests

import (
	"context"
	"math"
	"time"

	"github.com/stretchr/testify/require"
	"go.skia.org/infra/go/deepequal"
	"go.skia.org/infra/go/git/testutils/mem_git"
	"go.skia.org/infra/go/gitstore"
	"go.skia.org/infra/go/sktest"
	"go.skia.org/infra/go/vcsinfo"
)

func TestGitStore(t sktest.TestingT, gs gitstore.GitStore) {
	ctx := context.Background()

	// Empty to start.
	branches, err := gs.GetBranches(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, len(branches))
	lcs, err := gs.Get(ctx, []string{"a", "b", "c"})
	require.NoError(t, err)
	require.Equal(t, 3, len(lcs))
	for _, lc := range lcs {
		require.Nil(t, lc)
	}
	ics, err := gs.RangeN(ctx, math.MinInt32, math.MaxInt32, gitstore.ALL_BRANCHES)
	require.NoError(t, err)
	require.Equal(t, 0, len(ics))
	ics, err = gs.RangeByTime(ctx, time.Time{}, vcsinfo.MaxTime, gitstore.ALL_BRANCHES)
	require.NoError(t, err)
	require.Equal(t, 0, len(ics))

	// Put a commit, but don't update the branch head. It should show up in
	// results of Get() and Range, but the master branch should not be
	// updated.
	master := "master"
	c0 := mem_git.FakeCommit(t, "c0", master)
	require.NoError(t, gs.Put(ctx, []*vcsinfo.LongCommit{c0}))
	branches, err = gs.GetBranches(ctx)
	require.NoError(t, err)
	require.Nil(t, branches[master])
	lcs, err = gs.Get(ctx, []string{"a", "b", c0.Hash})
	require.NoError(t, err)
	require.Equal(t, 3, len(lcs))
	require.Nil(t, lcs[0])
	require.Nil(t, lcs[1])
	deepequal.AssertDeepEqual(t, c0, lcs[2])
	ics, err = gs.RangeN(ctx, math.MinInt32, math.MaxInt32, gitstore.ALL_BRANCHES)
	require.NoError(t, err)
	require.Equal(t, 1, len(ics))
	deepequal.AssertDeepEqual(t, c0.IndexCommit(), ics[0])
	ics, err = gs.RangeByTime(ctx, time.Time{}, vcsinfo.MaxTime, gitstore.ALL_BRANCHES)
	require.NoError(t, err)
	require.Equal(t, 1, len(ics))
	deepequal.AssertDeepEqual(t, c0.IndexCommit(), ics[0])
	ics, err = gs.RangeN(ctx, math.MinInt32, math.MaxInt32, master)
	require.NoError(t, err)
	require.Equal(t, 0, len(ics))
	ics, err = gs.RangeByTime(ctx, vcsinfo.MinTime, vcsinfo.MaxTime, master)
	require.NoError(t, err)
	require.Equal(t, 0, len(ics))

	// Put the master branch.
	require.NoError(t, gs.PutBranches(ctx, map[string]string{
		master: c0.Hash,
	}))
	branches, err = gs.GetBranches(ctx)
	require.NoError(t, err)
	require.NotNil(t, branches[master])
	require.Equal(t, c0.Hash, branches[master].Head)
	require.Equal(t, 0, branches[master].Index)
	ics, err = gs.RangeN(ctx, math.MinInt32, math.MaxInt32, master)
	require.NoError(t, err)
	require.Equal(t, 1, len(ics))
	deepequal.AssertDeepEqual(t, c0.IndexCommit(), ics[0])
	ics, err = gs.RangeByTime(ctx, vcsinfo.MinTime, vcsinfo.MaxTime, master)
	require.NoError(t, err)
	require.Equal(t, 1, len(ics))
	deepequal.AssertDeepEqual(t, c0.IndexCommit(), ics[0])

	// Add a second commit.
	c1 := mem_git.FakeCommit(t, "c1", master, c0)
	require.NoError(t, gs.Put(ctx, []*vcsinfo.LongCommit{c1}))
	branches, err = gs.GetBranches(ctx)
	require.NoError(t, err)
	require.NotNil(t, branches[master])
	require.Equal(t, c0.Hash, branches[master].Head)
	lcs, err = gs.Get(ctx, []string{c1.Hash})
	require.NoError(t, err)
	require.Equal(t, 1, len(lcs))
	deepequal.AssertDeepEqual(t, c1, lcs[0])
	ics, err = gs.RangeN(ctx, math.MinInt32, math.MaxInt32, gitstore.ALL_BRANCHES)
	require.NoError(t, err)
	require.Equal(t, 2, len(ics))
	deepequal.AssertDeepEqual(t, c0.IndexCommit(), ics[0])
	ics, err = gs.RangeByTime(ctx, time.Time{}, vcsinfo.MaxTime, gitstore.ALL_BRANCHES)
	require.NoError(t, err)
	require.Equal(t, 2, len(ics))
	deepequal.AssertDeepEqual(t, c0.IndexCommit(), ics[0])
	ics, err = gs.RangeN(ctx, math.MinInt32, math.MaxInt32, master)
	require.NoError(t, err)
	require.Equal(t, 1, len(ics))
	ics, err = gs.RangeByTime(ctx, vcsinfo.MinTime, vcsinfo.MaxTime, master)
	require.NoError(t, err)
	require.Equal(t, 1, len(ics))
	require.NoError(t, gs.PutBranches(ctx, map[string]string{
		master: c1.Hash,
	}))
	branches, err = gs.GetBranches(ctx)
	require.NoError(t, err)
	require.NotNil(t, branches[master])
	require.Equal(t, c1.Hash, branches[master].Head)
	require.Equal(t, 1, branches[master].Index)
	ics, err = gs.RangeN(ctx, math.MinInt32, math.MaxInt32, master)
	require.NoError(t, err)
	require.Equal(t, 2, len(ics))
	deepequal.AssertDeepEqual(t, c0.IndexCommit(), ics[0])
	deepequal.AssertDeepEqual(t, c1.IndexCommit(), ics[1])
	ics, err = gs.RangeByTime(ctx, vcsinfo.MinTime, vcsinfo.MaxTime, master)
	require.NoError(t, err)
	require.Equal(t, 2, len(ics))
	deepequal.AssertDeepEqual(t, c0.IndexCommit(), ics[0])
	deepequal.AssertDeepEqual(t, c1.IndexCommit(), ics[1])

	// Add a new branch.
	otherbranch := "otherbranch"
	c2 := mem_git.FakeCommit(t, "c2", otherbranch, c0)
	c0.Branches[otherbranch] = true // Re-insert c0 so that it shows up as part of otherbranch.
	require.NoError(t, gs.Put(ctx, []*vcsinfo.LongCommit{c0, c2}))
	branches, err = gs.GetBranches(ctx)
	require.NoError(t, err)
	require.Nil(t, branches[otherbranch])
	lcs, err = gs.Get(ctx, []string{c2.Hash})
	require.NoError(t, err)
	require.Equal(t, 1, len(lcs))
	deepequal.AssertDeepEqual(t, c2, lcs[0])
	// Note: Behavior for ALL_BRANCHES is undefined for RangeN when there
	// are multiple branches with overlapping indexes, so we don't check
	// that here.
	ics, err = gs.RangeByTime(ctx, time.Time{}, vcsinfo.MaxTime, gitstore.ALL_BRANCHES)
	require.NoError(t, err)
	require.Equal(t, 3, len(ics))
	deepequal.AssertDeepEqual(t, c0.IndexCommit(), ics[0])
	// RangeByTime sorts by index, and c1 and c2 both have index 1.
	if ics[1].Hash == c1.Hash {
		deepequal.AssertDeepEqual(t, c1.IndexCommit(), ics[1])
		deepequal.AssertDeepEqual(t, c2.IndexCommit(), ics[2])
	} else {
		deepequal.AssertDeepEqual(t, c1.IndexCommit(), ics[2])
		deepequal.AssertDeepEqual(t, c2.IndexCommit(), ics[1])
	}
	ics, err = gs.RangeN(ctx, math.MinInt32, math.MaxInt32, otherbranch)
	require.NoError(t, err)
	require.Equal(t, 0, len(ics))
	ics, err = gs.RangeByTime(ctx, vcsinfo.MinTime, vcsinfo.MaxTime, otherbranch)
	require.NoError(t, err)
	require.Equal(t, 0, len(ics))
	require.NoError(t, gs.PutBranches(ctx, map[string]string{
		otherbranch: c2.Hash,
	}))
	branches, err = gs.GetBranches(ctx)
	require.NoError(t, err)
	require.NotNil(t, branches[otherbranch])
	require.Equal(t, c2.Hash, branches[otherbranch].Head)
	require.Equal(t, 1, branches[otherbranch].Index)
	ics, err = gs.RangeN(ctx, math.MinInt32, math.MaxInt32, otherbranch)
	require.NoError(t, err)
	require.Equal(t, 2, len(ics))
	deepequal.AssertDeepEqual(t, c0.IndexCommit(), ics[0])
	deepequal.AssertDeepEqual(t, c2.IndexCommit(), ics[1])
	ics, err = gs.RangeByTime(ctx, vcsinfo.MinTime, vcsinfo.MaxTime, otherbranch)
	require.NoError(t, err)
	require.Equal(t, 2, len(ics))
	deepequal.AssertDeepEqual(t, c0.IndexCommit(), ics[0])
	deepequal.AssertDeepEqual(t, c2.IndexCommit(), ics[1])
	ics, err = gs.RangeN(ctx, math.MinInt32, math.MaxInt32, master)
	require.NoError(t, err)
	require.Equal(t, 2, len(ics))
	deepequal.AssertDeepEqual(t, c0.IndexCommit(), ics[0])
	deepequal.AssertDeepEqual(t, c1.IndexCommit(), ics[1])
	ics, err = gs.RangeByTime(ctx, vcsinfo.MinTime, vcsinfo.MaxTime, master)
	require.NoError(t, err)
	require.Equal(t, 2, len(ics))
	deepequal.AssertDeepEqual(t, c0.IndexCommit(), ics[0])
	deepequal.AssertDeepEqual(t, c1.IndexCommit(), ics[1])
}
