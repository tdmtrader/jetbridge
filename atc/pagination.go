package atc

const (
	LinkRelNext     = "next"
	LinkRelPrevious = "previous"

	PaginationQueryTimestamps = "timestamps"
	PaginationQueryFrom       = "from"
	PaginationQueryTo         = "to"
	PaginationQueryLimit      = "limit"
	PaginationWebLimit        = 100
	PaginationAPIDefaultLimit = 100

	// PaginationAPIMaxLimit caps a caller-supplied page size. The upstream
	// list endpoints (builds, job builds, resource versions) never bounded
	// limit at all; the pipeline runs API enforces this cap because its
	// listing is reachable unauthenticated on an exposed template and issues
	// one extra query per returned run. 500 = 5x the default page: generous
	// for scripted paging, small enough that one request cannot ask the DB
	// for an unbounded result set.
	PaginationAPIMaxLimit = 500
)
