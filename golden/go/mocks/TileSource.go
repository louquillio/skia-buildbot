// Code generated by mockery v1.0.0. DO NOT EDIT.

package mocks

import mock "github.com/stretchr/testify/mock"

import tiling "go.skia.org/infra/go/tiling"

// TileSource is an autogenerated mock type for the TileSource type
type TileSource struct {
	mock.Mock
}

// GetTile provides a mock function with given fields:
func (_m *TileSource) GetTile() *tiling.Tile {
	ret := _m.Called()

	var r0 *tiling.Tile
	if rf, ok := ret.Get(0).(func() *tiling.Tile); ok {
		r0 = rf()
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*tiling.Tile)
		}
	}

	return r0
}