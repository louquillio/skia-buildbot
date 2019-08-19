// Code generated by mockery v1.0.0. DO NOT EDIT.

package mocks

import (
	mock "github.com/stretchr/testify/mock"
	digesttools "go.skia.org/infra/golden/go/digesttools"

	types "go.skia.org/infra/golden/go/types"
)

// ClosestDiffFinder is an autogenerated mock type for the ClosestDiffFinder type
type ClosestDiffFinder struct {
	mock.Mock
}

// ClosestDigest provides a mock function with given fields: test, digest, label
func (_m *ClosestDiffFinder) ClosestDigest(test types.TestName, digest types.Digest, label types.Label) *digesttools.Closest {
	ret := _m.Called(test, digest, label)

	var r0 *digesttools.Closest
	if rf, ok := ret.Get(0).(func(types.TestName, types.Digest, types.Label) *digesttools.Closest); ok {
		r0 = rf(test, digest, label)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*digesttools.Closest)
		}
	}

	return r0
}

// Precompute provides a mock function with given fields:
func (_m *ClosestDiffFinder) Precompute() {
	_m.Called()
}
