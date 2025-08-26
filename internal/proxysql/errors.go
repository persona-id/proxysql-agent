package proxysql

import "errors"

var (
	ErrDatabase     = errors.New("general database error")
	ErrCacheTimeout = errors.New("timed out waiting for k8s caches to sync")
)
