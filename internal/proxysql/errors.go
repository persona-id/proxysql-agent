package proxysql

import "errors"

var (
	ErrDatabase            = errors.New("general database error")
	ErrCacheTimeout        = errors.New("timed out waiting for k8s caches to sync")
	ErrDrainCheckStuck     = errors.New("drain check rule still returning the drain error after reload from config")
	ErrProxySQLUnreachable = errors.New("timed out waiting for the ProxySQL admin interface")
)
