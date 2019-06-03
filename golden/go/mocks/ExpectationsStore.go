// Code generated by mockery v1.0.0. DO NOT EDIT.

package mocks

import context "context"
import expstorage "go.skia.org/infra/golden/go/expstorage"
import mock "github.com/stretchr/testify/mock"
import types "go.skia.org/infra/golden/go/types"

// ExpectationsStore is an autogenerated mock type for the ExpectationsStore type
type ExpectationsStore struct {
	mock.Mock
}

// AddChange provides a mock function with given fields: ctx, changes, userId
func (_m *ExpectationsStore) AddChange(ctx context.Context, changes types.Expectations, userId string) error {
	ret := _m.Called(ctx, changes, userId)

	var r0 error
	if rf, ok := ret.Get(0).(func(context.Context, types.Expectations, string) error); ok {
		r0 = rf(ctx, changes, userId)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// Get provides a mock function with given fields:
func (_m *ExpectationsStore) Get() (types.Expectations, error) {
	ret := _m.Called()

	var r0 types.Expectations
	if rf, ok := ret.Get(0).(func() types.Expectations); ok {
		r0 = rf()
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(types.Expectations)
		}
	}

	var r1 error
	if rf, ok := ret.Get(1).(func() error); ok {
		r1 = rf()
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// QueryLog provides a mock function with given fields: ctx, offset, size, details
func (_m *ExpectationsStore) QueryLog(ctx context.Context, offset int, size int, details bool) ([]*expstorage.TriageLogEntry, int, error) {
	ret := _m.Called(ctx, offset, size, details)

	var r0 []*expstorage.TriageLogEntry
	if rf, ok := ret.Get(0).(func(context.Context, int, int, bool) []*expstorage.TriageLogEntry); ok {
		r0 = rf(ctx, offset, size, details)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]*expstorage.TriageLogEntry)
		}
	}

	var r1 int
	if rf, ok := ret.Get(1).(func(context.Context, int, int, bool) int); ok {
		r1 = rf(ctx, offset, size, details)
	} else {
		r1 = ret.Get(1).(int)
	}

	var r2 error
	if rf, ok := ret.Get(2).(func(context.Context, int, int, bool) error); ok {
		r2 = rf(ctx, offset, size, details)
	} else {
		r2 = ret.Error(2)
	}

	return r0, r1, r2
}

// UndoChange provides a mock function with given fields: ctx, changeID, userID
func (_m *ExpectationsStore) UndoChange(ctx context.Context, changeID int64, userID string) (types.Expectations, error) {
	ret := _m.Called(ctx, changeID, userID)

	var r0 types.Expectations
	if rf, ok := ret.Get(0).(func(context.Context, int64, string) types.Expectations); ok {
		r0 = rf(ctx, changeID, userID)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(types.Expectations)
		}
	}

	var r1 error
	if rf, ok := ret.Get(1).(func(context.Context, int64, string) error); ok {
		r1 = rf(ctx, changeID, userID)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}
