package fs_tjstore

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.skia.org/infra/go/firestore"
	ifirestore "go.skia.org/infra/go/firestore"
	"go.skia.org/infra/go/paramtools"
	"go.skia.org/infra/go/testutils/unittest"
	ci "go.skia.org/infra/golden/go/continuous_integration"
	"go.skia.org/infra/golden/go/tjstore"
	"go.skia.org/infra/golden/go/types"
)

// TestPutGetTryJob makes sure we can store and retrieve a single TryJob.
func TestPutGetTryJob(t *testing.T) {
	unittest.LargeTest(t)
	c, cleanup := firestore.NewClientForTesting(t)
	defer cleanup()

	f := New(c, "buildbucket")
	ctx := context.Background()

	expectedID := "987654"
	psID := tjstore.CombinedPSID{
		CL:  "1234",
		CRS: "github",
		PS:  "abcd",
	}

	// Should not exist initially
	_, err := f.GetTryJob(ctx, expectedID)
	assert.Error(t, err)
	assert.Equal(t, tjstore.ErrNotFound, err)

	tj := ci.TryJob{
		SystemID:    expectedID,
		DisplayName: "My-Test",
		Updated:     time.Date(2019, time.August, 13, 12, 11, 10, 0, time.UTC),
	}

	err = f.PutTryJob(ctx, psID, tj)
	assert.NoError(t, err)

	actual, err := f.GetTryJob(ctx, expectedID)
	assert.NoError(t, err)
	assert.Equal(t, tj, actual)
}

// TestGetTryJobs stores several TryJobs belonging to two different PatchSets and makes sure
// we can retrieve them with GetTryJobs.
func TestGetTryJobs(t *testing.T) {
	unittest.LargeTest(t)
	c, cleanup := firestore.NewClientForTesting(t)
	defer cleanup()

	f := New(c, "buildbucket")
	ctx := context.Background()

	psID := tjstore.CombinedPSID{
		CL:  "1234",
		CRS: "github",
		PS:  "abcd",
	}

	// Should not exist initially
	xtj, err := f.GetTryJobs(ctx, psID)
	assert.NoError(t, err)
	assert.Empty(t, xtj)

	// Put them in backwards to check the order
	for i := 4; i > 0; i-- {
		tj := ci.TryJob{
			SystemID:    "987654" + strconv.Itoa(9-i),
			DisplayName: "My-Test-" + strconv.Itoa(i),
			Updated:     time.Date(2019, time.August, 13, 12, 11, 50-i, 0, time.UTC),
		}

		err := f.PutTryJob(ctx, psID, tj)
		assert.NoError(t, err)
	}

	tj := ci.TryJob{
		SystemID:    "ignoreme",
		DisplayName: "Perf-Ignore",
		Updated:     time.Date(2019, time.August, 13, 12, 12, 7, 0, time.UTC),
	}
	otherPSID := tjstore.CombinedPSID{
		CL:  "1234",
		CRS: "github",
		PS:  "next",
	}
	err = f.PutTryJob(ctx, otherPSID, tj)
	assert.NoError(t, err)

	xtj, err = f.GetTryJobs(ctx, psID)
	assert.NoError(t, err)
	assert.Len(t, xtj, 4)

	for i, tj := range xtj {
		assert.Equal(t, "My-Test-"+strconv.Itoa(i+1), tj.DisplayName)
	}

	xtj, err = f.GetTryJobs(ctx, otherPSID)
	assert.NoError(t, err)
	assert.Len(t, xtj, 1)
	assert.Equal(t, tj, xtj[0])
}

// TestConsistentParamsHashing makes sure we consistently hash a Params map to the same
// value - this is vital for making sure we can re-assemble the TestResults
func TestConsistentParamsHashing(t *testing.T) {
	unittest.SmallTest(t)
	m := paramtools.Params{
		"a": "b",
		"e": "f",
		"0": "98",
		"c": "d",
	}
	expectedHash := "62ee4de905f9ebda22ac5ffc81cddfb14939844dd33cc9c70de498054740d8f8"
	h, err := hashParams(m)
	assert.NoError(t, err)
	assert.Equal(t, expectedHash, h)

	// Check in a loop to make sure it isn't flaky
	for i := 0; i < 1000; i++ {
		h, err := hashParams(m)
		assert.NoError(t, err)
		assert.Equal(t, expectedHash, h)
	}

	m["a"] = "c"
	h, err = hashParams(m)
	assert.NoError(t, err)
	assert.NotEqual(t, expectedHash, h)

	h, err = hashParams(nil)
	assert.NoError(t, err)
	assert.Equal(t, emptyParamsHash, h)

	h, err = hashParams(paramtools.Params{})
	assert.NoError(t, err)
	assert.Equal(t, emptyParamsHash, h)
}

// TestPutGetResults stores some results from three different tryjobs each either
// 5 tests (for those we care about) or 1 test (for the one patchset we don't care about)
// and makes sure we can retrieve them.
func TestPutGetResults(t *testing.T) {
	unittest.LargeTest(t)
	c, cleanup := firestore.NewClientForTesting(t)
	defer cleanup()

	f := New(c, "buildbucket")
	ctx := context.Background()

	firstTJID := "987654"
	secondTJID := "zyxwvut"
	psID := tjstore.CombinedPSID{
		CL:  "1234",
		CRS: "github",
		PS:  "abcd",
	}

	gp := paramtools.Params{
		"os":    "Android",
		"model": "crustacean",
	}
	op := paramtools.Params{
		"ext": "png",
	}

	var xtr []tjstore.TryJobResult
	for i := 0; i < 5; i++ {
		xtr = append(xtr, tjstore.TryJobResult{
			GroupParams: gp,
			Options:     op,
			ResultParams: paramtools.Params{
				types.PRIMARY_KEY_FIELD: "test-" + strconv.Itoa(i),
			},
			Digest: fakeDigest("crust", i),
		})
	}

	err := f.PutResults(ctx, psID, firstTJID, xtr)
	assert.NoError(t, err)

	gp = paramtools.Params{
		"os":    "Android",
		"model": "whale",
	}

	xtr = nil
	for i := 0; i < 4; i++ {
		xtr = append(xtr, tjstore.TryJobResult{
			GroupParams: gp,
			Options:     op,
			ResultParams: paramtools.Params{
				types.PRIMARY_KEY_FIELD: "test-" + strconv.Itoa(i),
			},
			Digest: fakeDigest("whale", i),
		})
	}
	// pretend the two tryjobs had the same output for test-4
	xtr = append(xtr, tjstore.TryJobResult{
		GroupParams: gp,
		Options:     op,
		ResultParams: paramtools.Params{
			types.PRIMARY_KEY_FIELD: "test-4",
		},
		Digest: fakeDigest("crust", 4),
	})

	err = f.PutResults(ctx, psID, secondTJID, xtr)
	assert.NoError(t, err)

	otherPSID := tjstore.CombinedPSID{
		CL:  "1234",
		CRS: "github",
		PS:  "other",
	}

	err = f.PutResults(ctx, otherPSID, "should-be-ignored", []tjstore.TryJobResult{{
		GroupParams: paramtools.Params{
			"model": "invalid",
		},
		Options: op,
		ResultParams: paramtools.Params{
			types.PRIMARY_KEY_FIELD: "test-4",
		},
		Digest: "abcdef",
	}})
	assert.NoError(t, err)

	xtr, err = f.GetResults(ctx, psID)
	assert.NoError(t, err)
	assert.Len(t, xtr, 10)

	whaleCounts := 0
	crustCounts := 0
	// Spot-check the data
	for _, tr := range xtr {
		assert.Contains(t, []string{"whale", "crustacean"}, tr.GroupParams["model"])
		if tr.GroupParams["model"] == "whale" {
			whaleCounts++
		} else if tr.GroupParams["model"] == "crustacean" {
			crustCounts++
		}
		assert.Equal(t, op, tr.Options)
		assert.Contains(t, tr.ResultParams[types.PRIMARY_KEY_FIELD], "test-")
	}
	assert.Equal(t, 5, whaleCounts)
	assert.Equal(t, 5, crustCounts)
}

// TestPutGetResultsNoOptions makes sure that options (which are optional) can be omitted
// and everything still works
func TestPutGetResultsNoOptions(t *testing.T) {
	unittest.LargeTest(t)
	c, cleanup := firestore.NewClientForTesting(t)
	defer cleanup()

	f := New(c, "buildbucket")
	ctx := context.Background()

	tryJobID := "987654"
	psID := tjstore.CombinedPSID{
		CL:  "1234",
		CRS: "github",
		PS:  "abcd",
	}

	gp := paramtools.Params{
		"os":    "Android",
		"model": "crustacean",
	}

	xtr := []tjstore.TryJobResult{
		{
			GroupParams: gp,
			Options:     nil,
			ResultParams: paramtools.Params{
				types.PRIMARY_KEY_FIELD: "test-8",
			},
			Digest: fakeDigest("crust", 8),
		},
	}

	err := f.PutResults(ctx, psID, tryJobID, xtr)
	assert.NoError(t, err)

	xtr, err = f.GetResults(ctx, psID)
	assert.NoError(t, err)
	assert.Len(t, xtr, 1)
	assert.Equal(t, tjstore.TryJobResult{
		GroupParams: gp,
		Options:     paramtools.Params{},
		ResultParams: paramtools.Params{
			types.PRIMARY_KEY_FIELD: "test-8",
		},
		Digest: fakeDigest("crust", 8),
	}, xtr[0])
}

// TestPutGetResultsBig stores enough tryjob results such that we exercise the batch logic.
func TestPutGetResultsBig(t *testing.T) {
	unittest.LargeTest(t)
	c, cleanup := firestore.NewClientForTesting(t)
	defer cleanup()

	f := New(c, "buildbucket")
	ctx := context.Background()
	N := ifirestore.MAX_TRANSACTION_DOCS + ifirestore.MAX_TRANSACTION_DOCS/2

	tryJobID := "987654"
	psID := tjstore.CombinedPSID{
		CL:  "1234",
		CRS: "github",
		PS:  "abcd",
	}

	gp := paramtools.Params{
		"os":    "Android",
		"model": "crustacean",
	}

	var xtr []tjstore.TryJobResult
	for i := 0; i < N; i++ {
		// Have N different options maps, to make sure we batch Params.
		// This is much more variance than we would see in real data.
		op := paramtools.Params{
			"ext":        "png",
			"randomizer": strconv.Itoa(i),
		}

		xtr = append(xtr, tjstore.TryJobResult{
			GroupParams: gp,
			Options:     op,
			ResultParams: paramtools.Params{
				types.PRIMARY_KEY_FIELD: "test-" + strconv.Itoa(i),
			},
			Digest: fakeDigest("crust", i),
		})
	}

	err := f.PutResults(ctx, psID, tryJobID, xtr)
	assert.NoError(t, err)

	xtr, err = f.GetResults(ctx, psID)
	assert.NoError(t, err)
	assert.Len(t, xtr, N)

	for _, tr := range xtr {
		assert.Equal(t, gp, tr.GroupParams)
		assert.Contains(t, tr.Options, "randomizer")
		expectedTest := "test-" + tr.Options["randomizer"]
		assert.Equal(t, expectedTest, tr.ResultParams[types.PRIMARY_KEY_FIELD])
		assert.Contains(t, tr.ResultParams[types.PRIMARY_KEY_FIELD], "test-")
	}
}

// fakeDigest makes a digest based on the two inputs.
func fakeDigest(s string, i int) types.Digest {
	b := fmt.Sprintf("%s%d", s, i)
	h := md5.Sum([]byte(b))
	return types.Digest(hex.EncodeToString(h[:]))
}
