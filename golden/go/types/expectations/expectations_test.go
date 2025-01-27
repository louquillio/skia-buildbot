package expectations

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.skia.org/infra/go/testutils/unittest"
	"go.skia.org/infra/golden/go/types"
)

func TestSet(t *testing.T) {
	unittest.SmallTest(t)

	var e Expectations
	e.Set("a", "pos", Positive)
	e.Set("b", "neg", Negative)
	e.Set("c", "untr", Positive)
	e.Set("c", "untr", Untriaged)

	assert.Equal(t, e.Classification("a", "pos"), Positive)
	assert.Equal(t, e.Classification("b", "neg"), Negative)
	assert.Equal(t, e.Classification("c", "untr"), Untriaged)
	assert.Equal(t, e.Classification("d", "also_untriaged"), Untriaged)
	assert.Equal(t, e.Classification("a", "nope"), Untriaged)
	assert.Equal(t, e.Classification("b", "pos"), Untriaged)

	assert.Equal(t, 2, e.Len())
	assert.Equal(t, 3, e.NumTests()) // c was seen, but has all untriaged entries

	e.Set("c", "untr", Positive)
	assert.Equal(t, e.Classification("c", "untr"), Positive)
	assert.Equal(t, e.Classification("c", "nope"), Untriaged)
	assert.Equal(t, e.Classification("a", "nope"), Untriaged)

	assert.Equal(t, 3, e.Len())
	assert.Equal(t, 3, e.NumTests())

	e.Set("a", "oops", Negative)
	assert.Equal(t, e.Classification("a", "oops"), Negative)
	assert.Equal(t, 4, e.Len())
	assert.Equal(t, 3, e.NumTests())
}

func TestMerge(t *testing.T) {
	unittest.SmallTest(t)

	var e Expectations
	e.Set("a", "pos", Positive)
	e.Set("b", "neg", Positive)
	e.Set("c", "untr", Untriaged)

	f := Expectations{}         // test both ways of initialization
	f.Set("a", "neg", Negative) // creates new in existing test
	f.Set("b", "neg", Negative) // overwrites previous
	f.Set("d", "neg", Negative) // creates new test

	e.MergeExpectations(&f)
	e.MergeExpectations(nil)

	assert.Equal(t, Positive, e.Classification("a", "pos"))
	assert.Equal(t, Negative, e.Classification("a", "neg"))
	assert.Equal(t, Negative, e.Classification("b", "neg"))
	assert.Equal(t, Untriaged, e.Classification("c", "untr"))
	assert.Equal(t, Negative, e.Classification("d", "neg"))

	assert.Equal(t, 4, e.Len())

	// f should be unchanged
	assert.Equal(t, Untriaged, f.Classification("a", "pos"))
	assert.Equal(t, Negative, f.Classification("a", "neg"))
	assert.Equal(t, Negative, f.Classification("b", "neg"))
	assert.Equal(t, Untriaged, f.Classification("c", "untr"))
	assert.Equal(t, Negative, f.Classification("d", "neg"))
	assert.Equal(t, 3, f.Len())
}

func TestForAll(t *testing.T) {
	unittest.SmallTest(t)

	var e Expectations
	e.Set("a", "pos", Positive)
	e.Set("b", "neg", Negative)
	e.Set("c", "pos", Positive)
	e.Set("c", "untr", Untriaged)

	labels := map[types.TestName]map[types.Digest]Label{}
	err := e.ForAll(func(testName types.TestName, d types.Digest, l Label) error {
		if digests, ok := labels[testName]; ok {
			digests[d] = l
		} else {
			labels[testName] = map[types.Digest]Label{d: l}
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, map[types.TestName]map[types.Digest]Label{
		"a": {
			"pos": Positive,
		},
		"b": {
			"neg": Negative,
		},
		"c": {
			"pos": Positive,
		},
	}, labels)
}

// TestForAllError tests that we stop iterating through the entries when an error is returned.
func TestForAllError(t *testing.T) {
	unittest.SmallTest(t)

	var e Expectations
	e.Set("a", "pos", Positive)
	e.Set("b", "neg", Negative)
	e.Set("c", "pos", Positive)
	e.Set("c", "untr", Untriaged)

	counter := 0
	err := e.ForAll(func(testName types.TestName, d types.Digest, l Label) error {
		if counter == 2 {
			return errors.New("oops")
		}
		counter++
		return nil
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "oops")
	assert.Equal(t, 2, counter)
}

func TestDeepCopy(t *testing.T) {
	unittest.SmallTest(t)

	var e Expectations
	e.Set("a", "pos", Positive)

	f := e.DeepCopy()
	e.Set("b", "neg", Negative)
	f.Set("b", "neg", Positive)

	require.Equal(t, Positive, e.Classification("a", "pos"))
	require.Equal(t, Negative, e.Classification("b", "neg"))

	require.Equal(t, Positive, f.Classification("a", "pos"))
	require.Equal(t, Positive, f.Classification("b", "neg"))
}

func TestCounts(t *testing.T) {
	unittest.SmallTest(t)

	var e Expectations
	require.True(t, e.Empty())
	require.Equal(t, 0, e.NumTests())
	require.Equal(t, 0, e.Len())
	e.Set("a", "pos", Positive)
	e.Set("b", "neg", Negative)
	e.Set("c", "untr", Untriaged)
	e.Set("c", "pos", Positive)
	e.Set("c", "neg", Negative)

	require.False(t, e.Empty())
	assert.Equal(t, 3, e.NumTests())
	assert.Equal(t, 4, e.Len())

	// Make sure we are somewhat defensive and can handle nils gracefully
	var en *Expectations = nil
	assert.True(t, en.Empty())
	assert.Equal(t, 0, en.Len())
	assert.Equal(t, 0, en.NumTests())
}

func TestExpString(t *testing.T) {
	unittest.SmallTest(t)
	te := Expectations{
		labels: map[types.TestName]map[types.Digest]Label{
			"beta": {
				"hash1": Positive,
				"hash3": Negative,
				"hash2": Untriaged,
			},
			"alpha": {
				"hashB": Untriaged,
				"hashA": Negative,
				"hashC": Untriaged,
			},
		},
	}

	require.Equal(t, `alpha:
	hashA : negative
	hashB : untriaged
	hashC : untriaged
beta:
	hash1 : positive
	hash2 : untriaged
	hash3 : negative
`, te.String())
}

func TestAsBaseline(t *testing.T) {
	unittest.SmallTest(t)
	input := Expectations{
		labels: map[types.TestName]map[types.Digest]Label{
			"beta": {
				"hash1": Positive,
				"hash3": Negative,
				"hash2": Untriaged,
				"hash4": Positive,
			},
			"alpha": {
				"hashB": Untriaged,
				"hashA": Negative,
				"hashC": Untriaged,
			},
		},
	}

	expectedOutput := Baseline{
		"beta": {
			"hash1": Positive,
			"hash4": Positive,
		},
	}
	require.Equal(t, expectedOutput, input.AsBaseline())
}
