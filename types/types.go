package types

type MultiSearch struct {
	Targets []string
	Search  []Options
}

type Options struct {
	Pattern         string
	ShowFilename    bool
	ShowLineNumbers bool
	ShowByteOffset  bool
	Stats           bool
	SkipBytes       int64
	Limit           int64
}
