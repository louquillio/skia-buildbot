package psrefresh

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.skia.org/infra/go/paramtools"
	"go.skia.org/infra/go/testutils/unittest"
	"go.skia.org/infra/perf/go/btts"
	"go.skia.org/infra/perf/go/psrefresh/mocks"
)

func TestRefresher(t *testing.T) {
	unittest.SmallTest(t)

	op := &mocks.OPSProvider{}
	tileKey := btts.TileKeyFromOffset(100)
	tileKey2 := tileKey.PrevTile()
	op.On("GetLatestTile").Return(tileKey, nil)

	ps1 := paramtools.NewOrderedParamSet()
	ps1.Update(paramtools.ParamSet{
		"config": []string{"8888", "565"},
	})
	ps2 := paramtools.NewOrderedParamSet()
	ps2.Update(paramtools.ParamSet{
		"config": []string{"8888", "565", "gles"},
	})
	op.On("GetOrderedParamSet", mock.Anything, tileKey).Return(ps1, nil)
	op.On("GetOrderedParamSet", mock.Anything, tileKey2).Return(ps2, nil)

	pf := NewParamSetRefresher(op)
	err := pf.Start(time.Minute)
	assert.NoError(t, err)
	assert.Len(t, pf.Get()["config"], 3)
}

func TestRefresherFailure(t *testing.T) {
	unittest.SmallTest(t)

	op := &mocks.OPSProvider{}
	tileKey := btts.TileKeyFromOffset(100)
	op.On("GetLatestTile").Return(tileKey, fmt.Errorf("Something happened"))

	pf := NewParamSetRefresher(op)
	err := pf.Start(time.Minute)
	assert.Error(t, err)
}
