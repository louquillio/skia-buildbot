package os_steps

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.skia.org/infra/go/testutils/unittest"
	"go.skia.org/infra/task_driver/go/td"
)

func TestOsSteps(t *testing.T) {
	unittest.MediumTest(t)
	tr := td.StartTestRun(t)
	defer tr.Cleanup()

	// Root-level step.
	s := tr.Root()

	// We're basically just verifying that the os utils work.
	expect := []td.StepResult{}

	// Stat the nonexistent dir.
	expect = append(expect, td.STEP_RESULT_EXCEPTION)
	dir1 := filepath.Join(tr.Dir(), "test_dir")
	fi, err := Stat(s, dir1)
	require.True(t, os.IsNotExist(err))

	// Try to remove the dir.
	expect = append(expect, td.STEP_RESULT_SUCCESS)
	err = RemoveAll(s, dir1)
	require.NoError(t, err) // os.RemoveAll doesn't return error if the dir doesn't exist.

	// Create the dir.
	expect = append(expect, td.STEP_RESULT_SUCCESS)
	err = MkdirAll(s, dir1)
	require.NoError(t, err)

	// Stat the dir.
	expect = append(expect, td.STEP_RESULT_SUCCESS)
	fi, err = Stat(s, dir1)
	require.NoError(t, err)
	require.True(t, fi.IsDir())

	// Try to create the dir again.
	expect = append(expect, td.STEP_RESULT_SUCCESS)
	err = MkdirAll(s, dir1)
	require.NoError(t, err) // os.MkdirAll doesn't return error if the dir already exists.

	// Remove the dir.
	expect = append(expect, td.STEP_RESULT_SUCCESS)
	err = RemoveAll(s, dir1)
	require.NoError(t, err)

	// Stat the dir.
	expect = append(expect, td.STEP_RESULT_EXCEPTION)
	fi, err = Stat(s, dir1)
	require.True(t, os.IsNotExist(err))

	// Ensure that we got the expected step results.
	results := tr.EndRun(false, nil)
	require.Equal(t, len(results.Steps), len(expect))
	for idx, stepResult := range results.Steps {
		require.Equal(t, stepResult.Result, expect[idx])
	}
}
