// Code generated by mockery v1.0.0. DO NOT EDIT.

package mocks

import (
	mock "github.com/stretchr/testify/mock"
	digest_counter "go.skia.org/infra/golden/go/digest_counter"

	tiling "go.skia.org/infra/go/tiling"

	types "go.skia.org/infra/golden/go/types"

	url "net/url"
)

// DigestCounter is an autogenerated mock type for the DigestCounter type
type DigestCounter struct {
	mock.Mock
}

// ByQuery provides a mock function with given fields: tile, query
func (_m *DigestCounter) ByQuery(tile *tiling.Tile, query url.Values) digest_counter.DigestCount {
	ret := _m.Called(tile, query)

	var r0 digest_counter.DigestCount
	if rf, ok := ret.Get(0).(func(*tiling.Tile, url.Values) digest_counter.DigestCount); ok {
		r0 = rf(tile, query)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(digest_counter.DigestCount)
		}
	}

	return r0
}

// ByTest provides a mock function with given fields:
func (_m *DigestCounter) ByTest() map[types.TestName]digest_counter.DigestCount {
	ret := _m.Called()

	var r0 map[types.TestName]digest_counter.DigestCount
	if rf, ok := ret.Get(0).(func() map[types.TestName]digest_counter.DigestCount); ok {
		r0 = rf()
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(map[types.TestName]digest_counter.DigestCount)
		}
	}

	return r0
}

// ByTrace provides a mock function with given fields:
func (_m *DigestCounter) ByTrace() map[tiling.TraceId]digest_counter.DigestCount {
	ret := _m.Called()

	var r0 map[tiling.TraceId]digest_counter.DigestCount
	if rf, ok := ret.Get(0).(func() map[tiling.TraceId]digest_counter.DigestCount); ok {
		r0 = rf()
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(map[tiling.TraceId]digest_counter.DigestCount)
		}
	}

	return r0
}

// MaxDigestsByTest provides a mock function with given fields:
func (_m *DigestCounter) MaxDigestsByTest() map[types.TestName]types.DigestSet {
	ret := _m.Called()

	var r0 map[types.TestName]types.DigestSet
	if rf, ok := ret.Get(0).(func() map[types.TestName]types.DigestSet); ok {
		r0 = rf()
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(map[types.TestName]types.DigestSet)
		}
	}

	return r0
}
