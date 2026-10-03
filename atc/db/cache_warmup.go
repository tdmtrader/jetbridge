package db

import sq "github.com/Masterminds/squirrel"

func CacheWarmUp(runner sq.Runner) error {
	return warmUpBaseResourceTypesCache(runner)
}
