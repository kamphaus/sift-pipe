package types

type MultiSearch struct {
	Targets []string
	Search  []Options
}

type Options struct {
	Pattern string
	Offset  int64
	Limit   int64
}
