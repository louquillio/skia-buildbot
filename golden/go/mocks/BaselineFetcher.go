// Code generated by mockery v1.0.0. DO NOT EDIT.

package mocks

import baseline "go.skia.org/infra/golden/go/baseline"
import mock "github.com/stretchr/testify/mock"

// BaselineFetcher is an autogenerated mock type for the BaselineFetcher type
type BaselineFetcher struct {
	mock.Mock
}

// FetchBaseline provides a mock function with given fields: commitHash, issueID, issueOnly
func (_m *BaselineFetcher) FetchBaseline(commitHash string, issueID int64, issueOnly bool) (*baseline.Baseline, error) {
	ret := _m.Called(commitHash, issueID, issueOnly)

	var r0 *baseline.Baseline
	if rf, ok := ret.Get(0).(func(string, int64, bool) *baseline.Baseline); ok {
		r0 = rf(commitHash, issueID, issueOnly)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*baseline.Baseline)
		}
	}

	var r1 error
	if rf, ok := ret.Get(1).(func(string, int64, bool) error); ok {
		r1 = rf(commitHash, issueID, issueOnly)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}
